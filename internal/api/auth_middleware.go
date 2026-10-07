package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"scnetm/internal/auth"
)

// This file implements the authentication and authorization middleware.
//
// # Authentication
//
// The primary channel is `Authorization: Bearer <jwt>`. Two additional channels
// exist, both deliberate:
//
//   - `?token=<jwt>` — browsers cannot set headers on a WebSocket handshake, so
//     the console/events sockets accept the token as a query parameter. The
//     request is flagged ViaQueryToken so it is visible in logs and audits.
//   - The same query parameter is accepted on plain HTTP routes only when
//     allowQueryToken is passed (currently: download endpoints, where the
//     frontend uses an <a href> / window.open that also cannot set headers).
//
// # Logout
//
// The token is stateless, so logout is enforced by a server-side denylist keyed
// on jti (see SessionStore and auth_handlers.go). The middleware consults it on
// every request; a revoked jti yields 401 with code "unauthorized".

// authConfig bundles what the auth middleware needs.
type authConfig struct {
	issuer *auth.TokenIssuer
	users  UserStore
	rbac   *auth.Authorizer
	sess   SessionStore
	log    Logger
}

// authenticate validates the request's token and loads the principal.
//
// It returns the principal on success or an *APIError otherwise; it never
// writes the response itself, so it can be reused by the WebSocket upgrader.
func authenticate(c *gin.Context, cfg authConfig, allowQueryToken bool) (*Principal, *APIError) {
	token, viaQuery := extractToken(c, allowQueryToken)
	if token == "" {
		return nil, Unauthorized("缺少认证令牌")
	}

	claims, err := cfg.issuer.Parse(token)
	if err != nil {
		if errors.Is(err, auth.ErrTokenRevoked) {
			return nil, Unauthorized("会话已注销")
		}
		return nil, Unauthorized("令牌无效或已过期").WithCause(err)
	}

	// Denylist check: a logged-out token must stop working immediately.
	revoked, err := cfg.sess.IsRevoked(c.Request.Context(), claims.ID)
	if err != nil {
		// A broken session store must not silently fail open; treat it as an
		// unavailable dependency.
		return nil, Unavailable("会话存储不可用").WithCause(err)
	}
	if revoked {
		return nil, Unauthorized("会话已注销")
	}

	userID, err := claims.UserID()
	if err != nil {
		return nil, Unauthorized("令牌主体无效").WithCause(err)
	}

	// Per-user revocation floor: "log out everywhere" and a password reset do
	// not know the jti of every live token, so they record a timestamp instead.
	// Any token issued at or before that instant is dead. Without this check
	// those two operations would appear to succeed while leaving every existing
	// session live — exactly the case where an operator resets a compromised
	// account's password and believes the attacker has been evicted.
	if floor, ok := cfg.sess.RevokedBefore(userID); ok {
		// JWT timestamps are whole seconds, so a token minted in the same
		// second as the revocation would otherwise compare as "issued after the
		// floor" and survive. Truncating the floor to the second and requiring
		// the token to be issued strictly later closes that window.
		issued := claims.IssuedAt
		if issued == nil || !issued.Time.Truncate(time.Second).After(floor.Truncate(time.Second)) {
			return nil, Unauthorized("该会话因密码变更或批量登出而结束")
		}
	}

	p := &Principal{
		UserID:        userID,
		Username:      claims.Username,
		Role:          claims.Role,
		Permissions:   auth.NewPermissionSet(claims.Role),
		TokenID:       claims.ID,
		ExpiresAt:     claims.Expiry(),
		ViaQueryToken: viaQuery,
	}

	// Best-effort enrichment from the user row: this catches a disabled
	// account or a role change that happened after the token was minted.
	// A missing user store (nop) is not fatal — the token remains the source
	// of truth — but an explicit ErrNotFound means the account is gone.
	if u, err := cfg.users.GetByID(c.Request.Context(), userID); err == nil && u != nil {
		if u.Disabled {
			return nil, Forbidden("该账号已被停用")
		}
		p.Username = u.Username
		p.Role = u.Role
		p.Permissions = auth.NewPermissionSet(u.Role)
	} else if errors.Is(err, ErrNotFound) {
		return nil, Unauthorized("该账号已不存在")
	}

	return p, nil
}

// extractToken pulls the bearer token from the Authorization header, falling
// back to ?token= when allowed.
func extractToken(c *gin.Context, allowQueryToken bool) (token string, viaQuery bool) {
	if h := c.GetHeader("Authorization"); h != "" {
		if len(h) > 7 && strings.EqualFold(h[:7], "bearer ") {
			return strings.TrimSpace(h[7:]), false
		}
		// Tolerate a bare token in the header; some clients do this.
		return strings.TrimSpace(h), false
	}
	if allowQueryToken {
		if q := strings.TrimSpace(c.Query("token")); q != "" {
			return q, true
		}
	}
	return "", false
}

// RequireAuth rejects unauthenticated requests with the standard envelope.
func RequireAuth(cfg authConfig) gin.HandlerFunc {
	return func(c *gin.Context) {
		p, apiErr := authenticate(c, cfg, false)
		if apiErr != nil {
			Fail(c, apiErr)
			return
		}
		c.Set(ctxKeyPrincipal, p)
		c.Next()
	}
}

// RequireAuthQueryToken behaves like RequireAuth but also accepts ?token=.
// It is used for download endpoints driven by browser navigation.
func RequireAuthQueryToken(cfg authConfig) gin.HandlerFunc {
	return func(c *gin.Context) {
		p, apiErr := authenticate(c, cfg, true)
		if apiErr != nil {
			Fail(c, apiErr)
			return
		}
		c.Set(ctxKeyPrincipal, p)
		c.Next()
	}
}

// RequirePermission enforces a panel-level permission.
//
// Permission checks are evaluated against the role's permission set, which for
// a viewer never contains a mutating permission — so a viewer cannot start or
// stop an instance even if the grant table were corrupted (D3: the RBAC
// middleware is wired from day one even though single-user mode makes the
// instance-grant half a no-op).
func RequirePermission(perm auth.Permission) gin.HandlerFunc {
	return func(c *gin.Context) {
		p, ok := PrincipalFrom(c)
		if !ok {
			Fail(c, Unauthorized("需要认证"))
			return
		}
		if !p.Can(perm) {
			Fail(c, Forbidden("角色 %q 无权执行 %s", p.Role, perm))
			return
		}
		c.Next()
	}
}

// requireInstance loads the :id instance into the context, verifying both its
// existence and the caller's instance-scoped permission.
//
// This is the instance-level grant middleware §5.7 asks for. In single-user
// mode the grant half short-circuits (owner or single-user), but the code path
// is exercised, so flipping to multi-user needs no handler changes.
func (s *Server) requireInstance(perm auth.Permission) gin.HandlerFunc {
	return func(c *gin.Context) {
		p, ok := PrincipalFrom(c)
		if !ok {
			Fail(c, Unauthorized("需要认证"))
			return
		}

		id, idErr := instanceIDParam(c)
		if idErr != nil {
			Fail(c, idErr)
			return
		}

		inst, err := s.deps.Instances.GetByID(c.Request.Context(), id)
		if err != nil {
			Fail(c, Classify(err, "instance not found").WithDetail(gin.H{"instance_id": id}))
			return
		}

		if err := s.deps.RBAC.AuthorizeInstance(p.Role, p.UserID, inst.OwnerID, inst.ID, perm); err != nil {
			Fail(c, Forbidden("%s", err.Error()).WithDetail(gin.H{
				"instance_id": inst.ID,
				"permission":  string(perm),
			}))
			return
		}

		c.Set(ctxKeyInstance, inst)
		c.Set(ctxKeyInstanceID, inst.ID)
		c.Next()
	}
}

// instanceIDParam parses the :id path parameter into a positive int64.
func instanceIDParam(c *gin.Context) (int64, *APIError) {
	raw := c.Param("id")
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return 0, ValidationFailed("实例 id 必须为正整数，实际为 %q", raw)
	}
	return id, nil
}

// auditAction records a mutating operation.
//
// It is best-effort by design: the business operation has already succeeded, so
// a failing audit insert is logged and swallowed rather than turning a 200 into
// a 500. Every mutating handler calls this exactly once.
func (s *Server) audit(c *gin.Context, action, target string, detail any) {
	p, _ := PrincipalFrom(c)

	entry := &AuditEntry{
		Action:    action,
		Target:    target,
		IP:        ClientIP(c),
		Timestamp: s.now().UTC(),
	}
	if p != nil {
		entry.UserID = p.UserID
	}
	entry.Detail = renderDetail(detail)

	// Never let an audit failure mask a successful operation, but do make it
	// loud in the logs.
	if err := s.deps.Audit.Write(context.WithoutCancel(c.Request.Context()), entry); err != nil {
		loggerFrom(c).Error("audit write failed",
			"action", action,
			"target", target,
			"err", err.Error(),
		)
	}
	// Mirror the event onto the global event stream so an open dashboard can
	// show activity without polling the audit log.
	s.deps.Events.Publish(Event{
		Type:       EventAudit,
		InstanceID: entry.InstanceIDFromTarget(),
		Timestamp:  entry.Timestamp,
		Data: gin.H{
			"action": action,
			"target": target,
			"user":   usernameOf(p),
		},
	})
}

// InstanceIDFromTarget is a small helper for events: it extracts a leading
// "instance:<id>" target prefix.
func (e *AuditEntry) InstanceIDFromTarget() int64 {
	const prefix = "instance:"
	if !strings.HasPrefix(e.Target, prefix) {
		return 0
	}
	rest := e.Target[len(prefix):]
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		rest = rest[:i]
	}
	id, err := strconv.ParseInt(rest, 10, 64)
	if err != nil {
		return 0
	}
	return id
}

func usernameOf(p *Principal) string {
	if p == nil {
		return ""
	}
	return p.Username
}

// renderDetail turns a detail value into the string stored in
// audit_logs.detail. Maps are rendered as JSON so the column stays greppable
// and the diff summary survives a round trip.
func renderDetail(detail any) string {
	switch v := detail.(type) {
	case nil:
		return ""
	case string:
		return v
	case []byte:
		return string(v)
	default:
		return toJSONString(v)
	}
}

// noopHandler is a placeholder used when a route is registered but not wired;
// it always answers 501 with the standard envelope.
func noopHandler(feature string) gin.HandlerFunc {
	return func(c *gin.Context) {
		FailNotImplemented(c, feature, nil)
	}
}

// methodNotAllowed produces the envelope for unmatched methods on a known path.
func methodNotAllowed(feature string) gin.HandlerFunc {
	return func(c *gin.Context) {
		Fail(c, &APIError{
			Status:  http.StatusMethodNotAllowed,
			Code:    "method_not_allowed",
			Message: "该资源不支持此请求方法：" + feature,
		})
	}
}
