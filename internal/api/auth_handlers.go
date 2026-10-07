package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"scnetm/internal/auth"
)

// This file implements authentication: the first-run bootstrap, login, logout
// and the current-user endpoint.
//
// # Session design (documented decision)
//
// See session.go for the full rationale. In short: tokens are stateless HS256
// JWTs, and logout is enforced by a **server-side denylist keyed on the JWT's
// jti**, held in a MemorySessionStore that satisfies the SessionStore interface.
// The alternative — a server-side session *table* of live sessions — was
// rejected because it makes every request stateful and adds a second lifetime
// to reconcile, while giving up the ability to authenticate a WebSocket without
// a database round trip. The denylist store is deliberately behind an interface
// so a multi-replica deployment can swap in a SQLite/Redis-backed one without
// touching this file.
//
// # First-run flow (D3 / §5.7 "避免默认密码")
//
// The store seeds a single administrator row with `password_hash = ''`. That
// state is not "wrong password": it is "the panel has no password yet". So:
//
//   - GET  /auth/setup-required → {setup_required: true} (unauthenticated)
//   - POST /auth/setup          → sets the admin password, exactly once
//   - POST /auth/login          → 409 with code "setup_required" while the hash
//     is empty, so the UI routes to the setup form instead of
//     showing "wrong password"
//
// The one-shot property is enforced by UserStore.SetInitialPassword, which must
// only apply when the stored hash is empty. A second call therefore fails with
// ErrConflict → 409, and the endpoint cannot be used to reset a live password
// by an unauthenticated caller.

// loginRateKey builds the rate-limit key for a login attempt. It is IP-scoped:
// the per-account lockout is the other half of the defense.
func (s *Server) handleLogin(c *gin.Context) {
	var req LoginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		Fail(c, BadRequest("登录请求格式无效：%v", err))
		return
	}

	username, err := auth.SanitizeUsername(req.Username)
	if err != nil {
		// Deliberately vague: do not confirm whether the username exists.
		Fail(c, InvalidCredentials())
		return
	}
	ip := ClientIP(c)
	lockKey := "user:" + username

	// Account lockout after repeated failures (§5.7).
	if locked, retry := s.lockouts.Locked(lockKey); locked {
		s.auditRaw(c, "auth.login_locked", "user:"+username, gin.H{"reason": "account locked"})
		c.Header("Retry-After", itoa(int64(retry)))
		Fail(c, AccountLocked(retry).WithDetail(gin.H{
			"username":            username,
			"retry_after_seconds": retry,
		}))
		return
	}

	user, err := s.deps.Users.GetByUsername(c.Request.Context(), username)
	if err != nil {
		if errors.Is(err, ErrNotImplemented) {
			FailNotImplemented(c, "user store", err)
			return
		}
		if errors.Is(err, ErrNotFound) {
			// Count the failure so a username-enumeration campaign is also
			// rate limited, then answer identically to a wrong password.
			s.lockouts.Fail(lockKey)
			s.auditRaw(c, "auth.login_failed", "user:"+username, gin.H{"reason": "unknown user", "ip": ip})
			Fail(c, InvalidCredentials())
			return
		}
		Fail(c, Classify(err, "user not found"))
		return
	}

	// First-run: the seeded admin has no password yet. The predicate is
	// isFirstRunHash (see firstrun.go) rather than an emptiness check, because
	// internal/store seeds FirstRunPasswordMarker, not "".
	if isFirstRunHash(user.PasswordHash) {
		s.auditRaw(c, "auth.login_setup_required", "user:"+username, gin.H{"ip": ip})
		Fail(c, SetupRequired().WithDetail(gin.H{
			"username":            user.Username,
			"min_password_length": auth.MinPasswordLength,
			"endpoint":            "POST /api/v1/auth/setup",
		}))
		return
	}

	if user.Disabled {
		s.auditRaw(c, "auth.login_disabled", "user:"+username, gin.H{"ip": ip})
		Fail(c, Forbidden("该账号已被停用"))
		return
	}

	if err := auth.VerifyPassword(user.PasswordHash, req.Password); err != nil {
		failures, nowLocked := s.lockouts.Fail(lockKey)
		detail := gin.H{
			"username":        username,
			"failed_attempts": failures,
			"ip":              ip,
		}
		if nowLocked {
			detail["reason"] = "account locked after repeated failures"
		}
		s.auditRaw(c, "auth.login_failed", "user:"+username, detail)

		if nowLocked {
			retry := int(s.deps.ConfigHTTP.LoginLockoutFor.Seconds())
			if locked, r := s.lockouts.Locked(lockKey); locked {
				retry = r
			}
			c.Header("Retry-After", itoa(int64(retry)))
			Fail(c, AccountLocked(retry).WithDetail(detail))
			return
		}
		Fail(c, InvalidCredentials())
		return
	}

	// Success: clear the failure counter and mint the token.
	s.lockouts.Reset(lockKey)

	ttl := s.deps.Tokens.AccessTTL()
	if req.RememberMe {
		// "Remember me" extends the token, never the privileges.
		ttl *= 4
	}
	token, claims, err := s.mintToken(user, ttl)
	if err != nil {
		Fail(c, Internal(err))
		return
	}

	now := s.now().UTC()
	if err := s.deps.Users.TouchLastLogin(c.Request.Context(), user.ID, now); err != nil {
		// Not fatal: the login itself succeeded.
		loggerFrom(c).Warn("could not record last login", "user_id", user.ID, "err", err.Error())
	}

	s.auditRaw(c, "auth.login", fmt.Sprintf("user:%d", user.ID), gin.H{
		"username":   user.Username,
		"role":       string(user.Role),
		"ip":         ip,
		"jti":        claims.ID,
		"user_agent": truncate(c.Request.UserAgent(), 200),
	})

	// Publish so an open dashboard can show the sign-in immediately.
	s.deps.Events.Publish(Event{
		Type:      EventAudit,
		Timestamp: now,
		Data:      gin.H{"action": "auth.login", "user": user.Username},
	})

	c.JSON(http.StatusOK, DataResponse{Data: LoginResponse{
		Token:     token,
		TokenType: "Bearer",
		ExpiresIn: int64(ttl.Seconds()),
		ExpiresAt: claims.Expiry(),
		User:      s.userResponse(user),
	}})
}

// mintToken issues a JWT with a per-request TTL override.
//
// auth.TokenIssuer is immutable and shared, so a "remember me" token is minted
// by a derived issuer rather than by mutating shared state.
func (s *Server) mintToken(user *User, ttl time.Duration) (string, *auth.Claims, error) {
	issuer := s.deps.Tokens
	if ttl > 0 && ttl != issuer.AccessTTL() {
		derived, err := auth.NewTokenIssuerFromIssuer(issuer, auth.WithAccessTTL(ttl))
		if err != nil {
			return "", nil, err
		}
		issuer = derived
	}
	return issuer.Issue(user.ID, user.Username, user.Role)
}

// handleLogout revokes the current token (and optionally every token for the
// user) by adding its jti to the denylist.
func (s *Server) handleLogout(c *gin.Context) {
	p := MustPrincipal(c)

	var req LogoutRequest
	// A logout with no body must succeed: the frontend fires it with
	// navigator.sendBeacon during unload.
	if c.Request.ContentLength > 0 {
		if err := c.ShouldBindJSON(&req); err != nil {
			// Tolerate a malformed body rather than refusing to log out.
			loggerFrom(c).Warn("ignoring malformed logout body", "err", err.Error())
			req = LogoutRequest{}
		}
	}

	now := s.now().UTC()
	expiry := p.ExpiresAt
	if expiry.IsZero() {
		expiry = now.Add(s.deps.Tokens.AccessTTL())
	}

	if req.AllSessions {
		if err := s.deps.Sessions.RevokeAllForUser(c.Request.Context(), p.UserID, now); err != nil {
			Fail(c, Unavailable("无法吊销会话").WithCause(err))
			return
		}
	}
	if err := s.deps.Sessions.Revoke(c.Request.Context(), p.TokenID, p.UserID, expiry); err != nil {
		Fail(c, Unavailable("无法吊销会话").WithCause(err))
		return
	}

	s.auditRaw(c, "auth.logout", fmt.Sprintf("user:%d", p.UserID), gin.H{
		"jti":          p.TokenID,
		"all_sessions": req.AllSessions,
		"via_query":    p.ViaQueryToken,
	})

	c.JSON(http.StatusOK, DataResponse{Data: OKResponse{OK: true}})
}

// handleMe returns the current user, their permissions and their grants.
func (s *Server) handleMe(c *gin.Context) {
	p := MustPrincipal(c)

	resp := MeResponse{
		User: UserResponse{
			ID:          p.UserID,
			Username:    p.Username,
			Role:        p.Role,
			Permissions: permStrings(p.Permissions),
			HasPassword: true,
		},
		InstanceGrants: []InstanceGrant{},
		SingleUser:     s.deps.RBAC.SingleUser(),
	}

	// Enrich from the store when available so created_at/last_login are real.
	if u, err := s.deps.Users.GetByID(c.Request.Context(), p.UserID); err == nil && u != nil {
		resp.User = s.userResponse(u)
	} else if err != nil && !errors.Is(err, ErrNotFound) && !errors.Is(err, ErrNotImplemented) {
		Fail(c, Classify(err, "user not found"))
		return
	}

	c.JSON(http.StatusOK, DataResponse{Data: resp})
}

// handleChangePassword lets an authenticated user rotate their own password.
//
// Rotating invalidates every other session (RevokeAllForUser), because a
// password change is the canonical "something may be compromised" signal.
func (s *Server) handleChangePassword(c *gin.Context) {
	p := MustPrincipal(c)

	var req struct {
		CurrentPassword string `json:"current_password" binding:"required"`
		NewPassword     string `json:"new_password" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		Fail(c, BadRequest("修改密码请求格式无效：%v", err))
		return
	}

	user, err := s.deps.Users.GetByID(c.Request.Context(), p.UserID)
	if err != nil {
		Fail(c, Classify(err, "user not found"))
		return
	}
	if err := auth.VerifyPassword(user.PasswordHash, req.CurrentPassword); err != nil {
		s.auditRaw(c, "auth.password_change_failed", fmt.Sprintf("user:%d", p.UserID), gin.H{
			"reason": "current password did not match",
		})
		Fail(c, Forbidden("当前密码不正确"))
		return
	}
	hash, err := auth.HashPassword(req.NewPassword)
	if err != nil {
		Fail(c, passwordPolicyError(err))
		return
	}
	if err := s.deps.Users.UpdatePassword(c.Request.Context(), p.UserID, hash); err != nil {
		Fail(c, Classify(err, "user not found"))
		return
	}
	// Every existing token for this user stops working, including this one.
	now := s.now().UTC()
	if err := s.deps.Sessions.RevokeAllForUser(c.Request.Context(), p.UserID, now); err != nil {
		loggerFrom(c).Warn("could not revoke sessions after password change", "err", err.Error())
	}
	s.auditRaw(c, "auth.password_change", fmt.Sprintf("user:%d", p.UserID), gin.H{"sessions_revoked": true})

	c.JSON(http.StatusOK, DataResponse{Data: OKResponse{OK: true}})
}

// handleSetupRequired reports whether the panel still needs its first-run
// password. It is unauthenticated by design: the login screen calls it before
// anyone can log in.
func (s *Server) handleSetupRequired(c *gin.Context) {
	resp := SetupStatusResponse{MinPasswordLength: auth.MinPasswordLength}

	users, err := s.deps.Users.List(c.Request.Context())
	if err != nil {
		if errors.Is(err, ErrNotImplemented) {
			// The panel is running without a user store: report "setup not
			// required" so the UI shows the login form, and let the login
			// attempt surface the 501 with its machine-readable code.
			resp.Required = false
			resp.Reason = "user store is not available in this build"
			c.JSON(http.StatusOK, DataResponse{Data: resp})
			return
		}
		Fail(c, Classify(err, "could not read users"))
		return
	}

	// Setup is required when there is no usable administrator credential at
	// all: either no users exist, or none of them has a password.
	hasAdmin := false
	needsPassword := true
	for i := range users {
		u := &users[i]
		if u.Role == auth.RoleAdmin && !hasAdmin {
			resp.Username = u.Username
		}
		if !isFirstRunHash(u.PasswordHash) && strings.TrimSpace(u.PasswordHash) != "" {
			needsPassword = false
		}
		if u.Role == auth.RoleAdmin && !isFirstRunHash(u.PasswordHash) {
			hasAdmin = true
		}
	}

	switch {
	case len(users) == 0:
		resp.Required = true
		resp.Reason = "no panel users exist yet"
	case needsPassword:
		resp.Required = true
		resp.Reason = "the administrator account has no password set"
	default:
		resp.Required = false
	}
	// An admin with a password means the panel is initialised even if some
	// other account is password-less.
	if hasAdmin {
		resp.Required = false
	}

	c.JSON(http.StatusOK, DataResponse{Data: resp})
}

// handleSetup sets the administrator password exactly once.
//
// It is unauthenticated (nobody can log in yet) and therefore guarded by two
// independent conditions:
//
//  1. The target account must currently have an empty password hash.
//  2. UserStore.SetInitialPassword must be atomic, so two concurrent requests
//     cannot both succeed.
//
// After the first success the endpoint returns 409 forever, so it can never be
// used to take over an initialised panel.
func (s *Server) handleSetup(c *gin.Context) {
	var req SetupRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		Fail(c, BadRequest("初始化请求格式无效：%v", err))
		return
	}
	if req.ConfirmPassword != "" && req.ConfirmPassword != req.Password {
		Fail(c, ValidationFailed("两次输入的密码不一致").WithDetail(gin.H{
			"issues": []ValidationIssue{{
				Field:    "confirm_password",
				Code:     "password_mismatch",
				Message:  "confirm_password 必须与 password 一致",
				Severity: "error",
			}},
		}))
		return
	}

	// Validate the policy before touching the store.
	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		Fail(c, passwordPolicyError(err))
		return
	}

	users, err := s.deps.Users.List(c.Request.Context())
	if err != nil {
		if errors.Is(err, ErrNotImplemented) {
			FailNotImplemented(c, "user store", err)
			return
		}
		Fail(c, Classify(err, "could not read users"))
		return
	}
	if len(users) == 0 {
		Fail(c, Conflict("没有可用于初始化的面板用户；数据库必须预置一名管理员"))
		return
	}

	// Pick the target: the requested username if given, else the first
	// password-less admin, else the first password-less user.
	target := selectSetupTarget(users, req.Username)
	if target == nil {
		Fail(c, Conflict("面板初始化已完成"))
		return
	}
	if !isFirstRunHash(target.PasswordHash) {
		Fail(c, Conflict("面板初始化已完成"))
		return
	}

	// Renaming is optional and only allowed during bootstrap.
	if req.Username != "" && req.Username != target.Username {
		clean, err := auth.SanitizeUsername(req.Username)
		if err != nil {
			Fail(c, ValidationFailed("%v", err))
			return
		}
		target.Username = clean
		if err := s.deps.Users.Update(c.Request.Context(), target); err != nil {
			if errors.Is(err, ErrConflict) {
				Fail(c, Conflict("该用户名已被占用"))
				return
			}
			Fail(c, Classify(err, "could not update the user"))
			return
		}
	}

	if err := s.deps.Users.SetInitialPassword(c.Request.Context(), target.ID, hash); err != nil {
		if errors.Is(err, ErrConflict) {
			Fail(c, Conflict("面板初始化已完成"))
			return
		}
		Fail(c, Classify(err, "could not set the administrator password"))
		return
	}

	// Reflect the change for the response and for the audit detail.
	target.PasswordHash = hash
	target.HasPassword = true

	now := s.now().UTC()
	s.auditRaw(c, "auth.setup", fmt.Sprintf("user:%d", target.ID), gin.H{
		"username": target.Username,
		"role":     string(target.Role),
		"ip":       ClientIP(c),
	})

	// Issue a token immediately so the operator lands logged in after setup
	// rather than being bounced to the login form.
	token, claims, err := s.deps.Tokens.Issue(target.ID, target.Username, target.Role)
	if err != nil {
		// Setup succeeded; only the convenience login failed.
		c.JSON(http.StatusOK, DataResponse{Data: gin.H{
			"setup_complete": true,
			"login_required": true,
			"username":       target.Username,
		}})
		return
	}
	if err := s.deps.Users.TouchLastLogin(c.Request.Context(), target.ID, now); err != nil {
		loggerFrom(c).Warn("could not record last login", "user_id", target.ID, "err", err.Error())
	}

	c.JSON(http.StatusOK, DataResponse{Data: gin.H{
		"setup_complete": true,
		"token":          token,
		"token_type":     "Bearer",
		"expires_in":     int64(s.deps.Tokens.AccessTTL().Seconds()),
		"expires_at":     claims.Expiry(),
		"user":           s.userResponse(target),
	}})
}

// selectSetupTarget picks the account the bootstrap password applies to.
func selectSetupTarget(users []User, requested string) *User {
	passwordless := func(u *User) bool { return isFirstRunHash(u.PasswordHash) }

	if requested != "" {
		clean, err := auth.SanitizeUsername(requested)
		if err != nil {
			return nil
		}
		for i := range users {
			if users[i].Username == clean {
				return &users[i]
			}
		}
		return nil
	}
	// Prefer a password-less admin, then any password-less account.
	for i := range users {
		if users[i].Role == auth.RoleAdmin && passwordless(&users[i]) {
			return &users[i]
		}
	}
	for i := range users {
		if passwordless(&users[i]) {
			return &users[i]
		}
	}
	return nil
}

// passwordPolicyError converts an auth policy error into a 422 with per-field
// details, so the UI can point at the offending input.
func passwordPolicyError(err error) *APIError {
	code := "password_policy"
	switch {
	case errors.Is(err, auth.ErrPasswordTooShort):
		code = "password_too_short"
	case errors.Is(err, auth.ErrPasswordTooLong):
		code = "password_too_long"
	case errors.Is(err, auth.ErrPasswordTooCommon):
		code = "password_too_common"
	}
	return ValidationFailed("%v", err).WithDetail(gin.H{
		"issues": []ValidationIssue{{
			Field:    "password",
			Code:     code,
			Message:  err.Error(),
			Severity: "error",
		}},
		"min_length": auth.MinPasswordLength,
		"max_length": auth.MaxPasswordLength,
	})
}

// userResponse projects a User onto its public shape.
func (s *Server) userResponse(u *User) UserResponse {
	return UserResponse{
		ID:          u.ID,
		Username:    u.Username,
		Role:        u.Role,
		CreatedAt:   u.CreatedAt,
		LastLoginAt: u.LastLoginAt,
		Disabled:    u.Disabled,
		HasPassword: !isFirstRunHash(u.PasswordHash) && strings.TrimSpace(u.PasswordHash) != "",
		Permissions: permStrings(auth.NewPermissionSet(u.Role)),
	}
}

func permStrings(set auth.PermissionSet) []string {
	list := set.List()
	out := make([]string, 0, len(list))
	for _, p := range list {
		out = append(out, string(p))
	}
	return out
}

// auditRaw writes an audit row outside the auth context.
//
// Authentication failures happen before a Principal exists, so this variant
// takes the actor explicitly (from the request body) and always succeeds
// silently on error — an audit write must never turn a 401 into a 500.
func (s *Server) auditRaw(c *gin.Context, action, target string, detail any) {
	p, _ := PrincipalFrom(c)
	entry := &AuditEntry{
		Action:    action,
		Target:    target,
		IP:        ClientIP(c),
		Detail:    renderDetail(detail),
		Timestamp: s.now().UTC(),
	}
	if p != nil {
		entry.UserID = p.UserID
	}

	// Detach from the request context: logging out cancels nothing, but a
	// client disconnect must not lose the audit row.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(c.Request.Context()), 5*time.Second)
	defer cancel()

	if err := s.deps.Audit.Write(ctx, entry); err != nil {
		loggerFrom(c).Error("audit write failed", "action", action, "target", target, "err", err.Error())
	}
}
