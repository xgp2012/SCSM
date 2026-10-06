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

// This file implements the user-management endpoints (§5.5 users,
// user_instance_grant).
//
// In single-user mode these are reachable only by the seeded admin and are
// mostly used to change the admin's own password. The handlers are nevertheless
// fully implemented against UserStore, including the instance-grant bookkeeping,
// because D3 requires the multi-user surface to be in place without a schema
// migration later.

// handleListUsers lists panel users.
func (s *Server) handleListUsers(c *gin.Context) {
	users, err := s.deps.Users.List(c.Request.Context())
	if err != nil {
		if IsNotImplemented(err) {
			FailNotImplemented(c, "user store (§5.5 users)", err)
			return
		}
		Fail(c, Classify(err, "no users found"))
		return
	}

	out := make([]UserResponse, 0, len(users))
	for i := range users {
		out = append(out, s.userResponse(&users[i]))
	}
	c.JSON(http.StatusOK, UserListResponse{Data: out, Total: len(out)})
}

// handleCreateUser creates a panel user.
func (s *Server) handleCreateUser(c *gin.Context) {
	var req CreateUserRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		Fail(c, BadRequest("invalid user request: %v", err))
		return
	}

	username, err := auth.SanitizeUsername(req.Username)
	if err != nil {
		Fail(c, ValidationFailed("%v", err).WithDetail(validationDetails("username", "invalid_username", err)))
		return
	}
	role, err := auth.ParseRole(req.Role)
	if err != nil {
		Fail(c, ValidationFailed("%v", err).WithDetail(validationDetails("role", "invalid_role", err)))
		return
	}
	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		Fail(c, passwordPolicyError(err))
		return
	}

	// Reject a duplicate explicitly so the message names the conflict.
	if existing, gerr := s.deps.Users.GetByUsername(c.Request.Context(), username); gerr == nil && existing != nil {
		Fail(c, Conflict("a user named %q already exists", username).WithDetail(gin.H{"user_id": existing.ID}))
		return
	} else if gerr != nil && !errors.Is(gerr, ErrNotFound) && !errors.Is(gerr, ErrNotImplemented) {
		Fail(c, Classify(gerr, "could not check the username"))
		return
	}

	user := &User{
		Username:     username,
		PasswordHash: hash,
		Role:         role,
		CreatedAt:    s.now().UTC(),
		Disabled:     req.Disabled,
		HasPassword:  true,
	}
	if err := s.deps.Users.Create(c.Request.Context(), user); err != nil {
		if errors.Is(err, ErrConflict) {
			Fail(c, Conflict("a user named %q already exists", username))
			return
		}
		Fail(c, Classify(err, "could not create the user"))
		return
	}

	s.applyGrants(c, user.ID, req.Grants)

	s.audit(c, "user.create", fmt.Sprintf("user:%d", user.ID), gin.H{
		"username": user.Username,
		"role":     string(user.Role),
		"disabled": user.Disabled,
		"grants":   len(req.Grants),
	})

	c.JSON(http.StatusCreated, DataResponse{Data: s.userResponse(user)})
}

// handleUpdateUser updates a panel user.
func (s *Server) handleUpdateUser(c *gin.Context) {
	id, apiErr := int64Param(c, "id", "user id")
	if apiErr != nil {
		Fail(c, apiErr)
		return
	}

	user, err := s.deps.Users.GetByID(c.Request.Context(), id)
	if err != nil {
		Fail(c, Classify(err, fmt.Sprintf("user %d not found", id)))
		return
	}

	var req UpdateUserRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		Fail(c, BadRequest("invalid user request: %v", err))
		return
	}

	p := MustPrincipal(c)
	changes := gin.H{}

	if req.Username != nil {
		clean, err := auth.SanitizeUsername(*req.Username)
		if err != nil {
			Fail(c, ValidationFailed("%v", err))
			return
		}
		if clean != user.Username {
			if existing, gerr := s.deps.Users.GetByUsername(c.Request.Context(), clean); gerr == nil && existing != nil && existing.ID != user.ID {
				Fail(c, Conflict("a user named %q already exists", clean))
				return
			}
			changes["username_from"] = user.Username
			changes["username_to"] = clean
			user.Username = clean
		}
	}

	if req.Role != nil {
		role, err := auth.ParseRole(*req.Role)
		if err != nil {
			Fail(c, ValidationFailed("%v", err))
			return
		}
		// Refuse to demote the last administrator: that would lock everyone
		// out of user management with no way back.
		if user.Role == auth.RoleAdmin && role != auth.RoleAdmin {
			count, cerr := s.deps.Users.Count(c.Request.Context())
			if cerr == nil && count <= 1 {
				Fail(c, Conflict("cannot change the role of the only user account"))
				return
			}
			if admins, aerr := s.countAdmins(c.Request.Context()); aerr == nil && admins <= 1 {
				Fail(c, Conflict("cannot demote the last administrator"))
				return
			}
		}
		changes["role_from"] = string(user.Role)
		changes["role_to"] = string(role)
		user.Role = role
	}

	if req.Disabled != nil {
		// Refusing to disable yourself prevents an accidental self-lockout.
		if *req.Disabled && p.UserID == user.ID {
			Fail(c, Conflict("you cannot disable your own account"))
			return
		}
		if *req.Disabled && user.Role == auth.RoleAdmin {
			if admins, aerr := s.countAdmins(c.Request.Context()); aerr == nil && admins <= 1 {
				Fail(c, Conflict("cannot disable the last administrator"))
				return
			}
		}
		changes["disabled"] = *req.Disabled
		user.Disabled = *req.Disabled
	}

	if req.Password != nil && *req.Password != "" {
		hash, err := auth.HashPassword(*req.Password)
		if err != nil {
			Fail(c, passwordPolicyError(err))
			return
		}
		if err := s.deps.Users.UpdatePassword(c.Request.Context(), user.ID, hash); err != nil {
			Fail(c, Classify(err, "could not update the password"))
			return
		}
		user.PasswordHash = hash
		user.HasPassword = true
		changes["password_changed"] = true
		// An administrative password reset must kill the target's sessions.
		if err := s.deps.Sessions.RevokeAllForUser(c.Request.Context(), user.ID, s.now().UTC()); err != nil {
			loggerFrom(c).Warn("could not revoke sessions after password reset", "user_id", user.ID, "err", err.Error())
		}
	}

	if err := s.deps.Users.Update(c.Request.Context(), user); err != nil {
		if errors.Is(err, ErrConflict) {
			Fail(c, Conflict("that username is already taken"))
			return
		}
		Fail(c, Classify(err, "could not update the user"))
		return
	}

	s.applyGrants(c, user.ID, req.Grants)

	s.audit(c, "user.update", fmt.Sprintf("user:%d", user.ID), changes)

	c.JSON(http.StatusOK, DataResponse{Data: s.userResponse(user)})
}

// handleDeleteUser deletes a panel user.
func (s *Server) handleDeleteUser(c *gin.Context) {
	id, apiErr := int64Param(c, "id", "user id")
	if apiErr != nil {
		Fail(c, apiErr)
		return
	}

	p := MustPrincipal(c)
	if p.UserID == id {
		Fail(c, Conflict("you cannot delete your own account"))
		return
	}

	user, err := s.deps.Users.GetByID(c.Request.Context(), id)
	if err != nil {
		Fail(c, Classify(err, fmt.Sprintf("user %d not found", id)))
		return
	}

	if user.Role == auth.RoleAdmin {
		if admins, aerr := s.countAdmins(c.Request.Context()); aerr == nil && admins <= 1 {
			Fail(c, Conflict("cannot delete the last administrator"))
			return
		}
	}

	if err := s.deps.Users.Delete(c.Request.Context(), id); err != nil {
		Fail(c, Classify(err, fmt.Sprintf("user %d not found", id)))
		return
	}
	// Kill the deleted account's tokens immediately.
	if err := s.deps.Sessions.RevokeAllForUser(c.Request.Context(), id, s.now().UTC()); err != nil {
		loggerFrom(c).Warn("could not revoke sessions for the deleted user", "user_id", id, "err", err.Error())
	}

	s.audit(c, "user.delete", fmt.Sprintf("user:%d", id), gin.H{
		"username": user.Username,
		"role":     string(user.Role),
	})

	c.JSON(http.StatusOK, DataResponse{Data: OKResponse{OK: true}})
}

// applyGrants records instance-level grants.
//
// Grants are written to the store AND to the in-process authorizer. Both are
// needed: the authorizer is what authorizes the current request stream, and the
// store is what survives a restart. Recording only in memory — which is what
// this did before — means an operator configures grants, the panel restarts,
// and every grant silently disappears while the UI still shows them.
//
// In single-user mode the authorizer ignores grants entirely and the table
// stays empty, per §5.5, so this is a documented no-op there. The store write is
// still attempted: if a deployment persists grants while running single-user,
// turning multi-user on later must not require re-entering them.
func (s *Server) applyGrants(c *gin.Context, userID int64, grants []GrantRequest) {
	if len(grants) == 0 {
		return
	}

	applied := 0
	for _, g := range grants {
		perm := auth.GrantPerm(strings.ToLower(strings.TrimSpace(g.Perm)))
		if !perm.Valid() {
			loggerFrom(c).Warn("ignoring invalid grant",
				"user_id", userID, "instance_id", g.InstanceID, "perm", g.Perm)
			continue
		}

		if s.deps.Grants != nil {
			if err := s.deps.Grants.Grant(c.Request.Context(), userID, g.InstanceID, perm); err != nil {
				// A grant that could not be persisted must not be reported as
				// applied: failing loudly is better than a grant that works
				// until the next restart.
				Fail(c, Classify(err, "could not store the instance grant"))
				return
			}
		}
		s.deps.RBAC.SetGrant(userID, g.InstanceID, perm)
		applied++
	}

	if applied > 0 && !s.deps.RBAC.SingleUser() {
		s.audit(c, "user.grants", fmt.Sprintf("user:%d", userID), gin.H{"count": applied})
	}
}

// countAdmins counts enabled administrators.
func (s *Server) countAdmins(ctx context.Context) (int, error) {
	users, err := s.deps.Users.List(ctx)
	if err != nil {
		return 0, err
	}
	n := 0
	for i := range users {
		if users[i].Role == auth.RoleAdmin && !users[i].Disabled {
			n++
		}
	}
	return n, nil
}

// ---------------------------------------------------------------------------
// Audit
// ---------------------------------------------------------------------------

// handleListAudit returns audit rows filtered by the query parameters.
func (s *Server) handleListAudit(c *gin.Context) {
	var q AuditQuery
	if err := c.ShouldBindQuery(&q); err != nil {
		Fail(c, BadRequest("invalid audit query: %v", err))
		return
	}

	filter := AuditFilter{
		InstanceID: q.InstanceID,
		UserID:     q.UserID,
		Action:     strings.TrimSpace(q.Action),
		Limit:      q.Limit,
		Offset:     q.Offset,
	}
	if filter.Limit <= 0 {
		filter.Limit = 100
	}
	if filter.Limit > 1000 {
		Fail(c, ValidationFailed("limit must be at most 1000"))
		return
	}
	if filter.Offset < 0 {
		Fail(c, ValidationFailed("offset must not be negative"))
		return
	}

	for _, spec := range []struct {
		raw   string
		field string
		dst   **time.Time
	}{
		{q.From, "from", &filter.From},
		{q.To, "to", &filter.To},
	} {
		if spec.raw == "" {
			continue
		}
		t, err := parseTimeParam(spec.raw)
		if err != nil {
			Fail(c, ValidationFailed("%s must be an RFC3339 timestamp or a Unix epoch: %v", spec.field, err))
			return
		}
		*spec.dst = &t
	}

	rows, err := s.deps.Audit.List(c.Request.Context(), filter)
	if err != nil {
		if IsNotImplemented(err) {
			FailNotImplemented(c, "audit store (§5.5 audit_logs)", err)
			return
		}
		Fail(c, Classify(err, "no audit entries found"))
		return
	}
	if rows == nil {
		rows = []AuditEntry{}
	}

	c.JSON(http.StatusOK, AuditListResponse{
		Data:   rows,
		Total:  len(rows),
		Limit:  filter.Limit,
		Offset: filter.Offset,
	})
}

// parseTimeParam accepts RFC3339 or a Unix epoch (seconds).
func parseTimeParam(raw string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t, nil
	}
	if secs, err := parseInt64(raw); err == nil {
		return time.Unix(secs, 0).UTC(), nil
	}
	return time.Time{}, fmt.Errorf("unrecognised time format")
}
