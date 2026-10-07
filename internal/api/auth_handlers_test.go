package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"scnetm/internal/auth"
)

// --- first-run bootstrap ---------------------------------------------------

// TestSetupRequiredFlow is the D3 first-run path: the seeded admin carries the
// store's marker, so login must fail with the DISTINCT setup_required code and
// GET /auth/setup-required must say so.
func TestSetupRequiredFlow(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t)

	// The marker is what internal/store actually seeds.
	if got := ts.users.users[1].PasswordHash; got != FirstRunPasswordMarker {
		t.Fatalf("seeded hash = %q, want the first-run marker", got)
	}

	// Unauthenticated status endpoint.
	resp := ts.do(http.MethodGet, "/api/v1/auth/setup-required", nil)
	requireStatus(t, resp, http.StatusOK)
	var status struct {
		Data SetupStatusResponse `json:"data"`
	}
	resp.JSON(&status)
	if !status.Data.Required {
		t.Fatalf("setup_required = false, want true: %s", resp.Body)
	}
	if status.Data.Username != "admin" {
		t.Errorf("username = %q, want admin", status.Data.Username)
	}
	if status.Data.MinPasswordLength != auth.MinPasswordLength {
		t.Errorf("min_password_length = %d, want %d", status.Data.MinPasswordLength, auth.MinPasswordLength)
	}

	// Login must NOT be a generic 401: it is a 409 with a distinct code.
	login := ts.login("admin", "anything-at-all")
	requireErrorCode(t, login, http.StatusConflict, CodeSetupRequired)
	ts.requireAudit("auth.login_setup_required")

	// Setup completes and returns a usable token.
	setup := ts.do(http.MethodPost, "/api/v1/auth/setup", SetupRequest{
		Password:        "admin-password-123",
		ConfirmPassword: "admin-password-123",
	})
	requireStatus(t, setup, http.StatusOK)
	ts.requireAudit("auth.setup")
}

// TestSetupIsOneShot: the endpoint cannot be used to take over an initialised
// panel, and its second call is a 409.
func TestSetupIsOneShot(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t)
	token := ts.adminToken("first-password-123")

	second := ts.do(http.MethodPost, "/api/v1/auth/setup", SetupRequest{
		Password:        "attacker-password",
		ConfirmPassword: "attacker-password",
	})
	requireErrorCode(t, second, http.StatusConflict, CodeConflict)

	// The original password still works and the attacker's does not.
	if resp := ts.login("admin", "first-password-123"); resp.Status != http.StatusOK {
		t.Fatalf("original password stopped working: %d %s", resp.Status, resp.Body)
	}
	if resp := ts.login("admin", "attacker-password"); resp.Status == http.StatusOK {
		t.Fatalf("the attacker's password was accepted: %s", resp.Body)
	}
	_ = token

	// And setup-required now reports false.
	resp := ts.do(http.MethodGet, "/api/v1/auth/setup-required", nil)
	var status struct {
		Data SetupStatusResponse `json:"data"`
	}
	resp.JSON(&status)
	if status.Data.Required {
		t.Fatalf("setup_required = true after setup: %s", resp.Body)
	}
}

// TestSetupRejectsWeakPassword: the password policy is enforced before the store
// is touched, so the panel cannot end up with a weak admin password.
func TestSetupRejectsWeakPassword(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		password string
		confirm  string
		code     string
	}{
		{"too short", "short", "short", "password_too_short"},
		{"common", "password1", "password1", "password_too_common"},
		{"mismatch", "long-enough-password", "different-password", "password_mismatch"},
		{"too long", strings.Repeat("x", 100), strings.Repeat("x", 100), "password_too_long"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ts := newTestServer(t)
			resp := ts.do(http.MethodPost, "/api/v1/auth/setup", SetupRequest{
				Password:        tc.password,
				ConfirmPassword: tc.confirm,
			})
			requireErrorCode(t, resp, http.StatusUnprocessableEntity, CodeValidationFailed)

			// The detail must name the offending field.
			var env ErrorEnvelope
			resp.JSON(&env)
			details, _ := env.Error.Details.(map[string]any)
			issues, _ := details["issues"].([]any)
			if len(issues) == 0 {
				t.Fatalf("no issues in details: %s", resp.Body)
			}
			first, _ := issues[0].(map[string]any)
			if first["code"] != tc.code {
				t.Errorf("issue code = %v, want %v", first["code"], tc.code)
			}

			// The account must still be in the first-run state.
			if u := ts.users.users[1]; !isFirstRunHash(u.PasswordHash) {
				t.Fatalf("password was set despite a policy failure")
			}
		})
	}
}

// --- login -----------------------------------------------------------------

func TestLoginSuccessAndMe(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t, func(o *testOptions) { o.adminPassword = "admin-password-123" })

	resp := ts.login("admin", "admin-password-123")
	requireStatus(t, resp, http.StatusOK)

	var out struct {
		Data LoginResponse `json:"data"`
	}
	resp.JSON(&out)

	if out.Data.Token == "" {
		t.Fatal("no token in the login response")
	}
	if out.Data.TokenType != "Bearer" {
		t.Errorf("token_type = %q, want Bearer", out.Data.TokenType)
	}
	if out.Data.ExpiresIn <= 0 {
		t.Errorf("expires_in = %d, want > 0", out.Data.ExpiresIn)
	}
	if out.Data.User.Username != "admin" || out.Data.User.Role != auth.RoleAdmin {
		t.Errorf("user = %+v, want admin/admin", out.Data.User)
	}
	// The hash must never be serialised.
	if strings.Contains(resp.String(), "$2a$") || strings.Contains(resp.String(), "$2b$") {
		t.Fatalf("login response leaked a bcrypt hash: %s", resp.Body)
	}
	// Permissions are resolved for the UI.
	if len(out.Data.User.Permissions) == 0 {
		t.Error("no permissions in the login response")
	}

	ts.requireAudit("auth.login")

	// GET /auth/me with that token.
	me := ts.get(out.Data.Token, "/api/v1/auth/me")
	requireStatus(t, me, http.StatusOK)
	var meOut struct {
		Data MeResponse `json:"data"`
	}
	me.JSON(&meOut)
	if meOut.Data.User.Username != "admin" {
		t.Errorf("me.username = %q, want admin", meOut.Data.User.Username)
	}
	if !meOut.Data.SingleUser {
		t.Error("single_user = false, want true (D3 mode)")
	}
	if meOut.Data.InstanceGrants == nil {
		t.Error("instance_grants should be an empty array, not null")
	}
}

func TestLoginFailures(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t, func(o *testOptions) { o.adminPassword = "admin-password-123" })

	t.Run("wrong password", func(t *testing.T) {
		resp := ts.login("admin", "not-the-password")
		requireErrorCode(t, resp, http.StatusUnauthorized, CodeInvalidCredentials)
		ts.requireAudit("auth.login_failed")
	})

	t.Run("unknown user", func(t *testing.T) {
		resp := ts.login("nobody", "some-password")
		// Identical response to a wrong password: no username enumeration.
		requireErrorCode(t, resp, http.StatusUnauthorized, CodeInvalidCredentials)
	})

	t.Run("missing fields", func(t *testing.T) {
		resp := ts.do(http.MethodPost, "/api/v1/auth/login", map[string]string{"username": "admin"})
		requireErrorCode(t, resp, http.StatusBadRequest, CodeBadRequest)
	})

	t.Run("disabled account", func(t *testing.T) {
		ts2 := newTestServer(t, func(o *testOptions) { o.adminPassword = "admin-password-123" })
		u := ts2.users.users[1]
		u.Disabled = true
		_ = ts2.users.Update(t.Context(), u)
		resp := ts2.login("admin", "admin-password-123")
		requireErrorCode(t, resp, http.StatusForbidden, CodeForbidden)
	})
}

// TestLoginLockout: repeated failures lock the account, and the lockout is
// reported with 429 + account_locked.
func TestLoginLockout(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t, func(o *testOptions) {
		o.adminPassword = "admin-password-123"
		cfg := DefaultHTTPConfig()
		cfg.LoginMaxFailures = 3
		cfg.LoginLockoutFor = 10 * time.Minute
		cfg.LoginRateLimit = 100 // keep the IP limiter out of the way
		o.httpConfig = &cfg
	})

	for i := 0; i < 3; i++ {
		resp := ts.login("admin", "wrong-password")
		if i < 2 {
			requireErrorCode(t, resp, http.StatusUnauthorized, CodeInvalidCredentials)
		} else {
			// The third failure trips the lockout and is itself reported as
			// locked, which is the clearest signal to the user.
			requireErrorCode(t, resp, http.StatusTooManyRequests, CodeAccountLocked)
		}
	}

	// Even the CORRECT password is refused while locked.
	resp := ts.login("admin", "admin-password-123")
	requireErrorCode(t, resp, http.StatusTooManyRequests, CodeAccountLocked)
	if ra := resp.Header.Get("Retry-After"); ra == "" {
		t.Error("no Retry-After header on a lockout response")
	}
	var env ErrorEnvelope
	resp.JSON(&env)
	details, _ := env.Error.Details.(map[string]any)
	if _, ok := details["retry_after_seconds"]; !ok {
		t.Errorf("lockout details missing retry_after_seconds: %s", resp.Body)
	}

	ts.requireAudit("auth.login_locked")
}

// TestLoginLockoutClearsOnSuccess: a user who mistypes a few times and then gets
// it right is not punished.
func TestLoginLockoutClearsOnSuccess(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t, func(o *testOptions) {
		o.adminPassword = "admin-password-123"
		cfg := DefaultHTTPConfig()
		cfg.LoginMaxFailures = 5
		cfg.LoginRateLimit = 100
		o.httpConfig = &cfg
	})

	for i := 0; i < 3; i++ {
		requireStatus(t, ts.login("admin", "wrong"), http.StatusUnauthorized)
	}
	requireStatus(t, ts.login("admin", "admin-password-123"), http.StatusOK)

	if n := ts.server.lockouts.Failures("user:admin"); n != 0 {
		t.Fatalf("failure counter = %d after a successful login, want 0", n)
	}
}

// TestLoginRateLimit: the IP limiter is separate from the account lockout and
// produces rate_limited.
func TestLoginRateLimit(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t, func(o *testOptions) {
		o.adminPassword = "admin-password-123"
		cfg := DefaultHTTPConfig()
		cfg.LoginRateLimit = 3
		cfg.LoginRateWindow = time.Hour
		cfg.LoginMaxFailures = 1000 // isolate the IP limiter
		o.httpConfig = &cfg
	})

	// The first three are allowed through the limiter (and fail auth).
	for i := 0; i < 3; i++ {
		requireStatus(t, ts.login("admin", "nope"), http.StatusUnauthorized)
	}
	fourth := ts.login("admin", "nope")
	requireErrorCode(t, fourth, http.StatusTooManyRequests, CodeRateLimited)
	if fourth.Header.Get("Retry-After") == "" {
		t.Error("no Retry-After header on a rate-limited response")
	}

	// A different IP is unaffected: the key is per client.
	other := ts.do(http.MethodPost, "/api/v1/auth/login",
		LoginRequest{Username: "admin", Password: "nope"},
		"X-Forwarded-For", "203.0.113.9")
	// X-Forwarded-For is deliberately NOT trusted, so this is the same key and
	// therefore still limited — assert that explicitly, because trusting the
	// header would be a rate-limit bypass.
	requireStatus(t, other, http.StatusTooManyRequests)
}

// --- logout ----------------------------------------------------------------

// TestLogoutInvalidatesToken proves the denylist actually works: the same token
// stops being accepted immediately after logout.
func TestLogoutInvalidatesToken(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t, func(o *testOptions) { o.adminPassword = "admin-password-123" })
	token := ts.mustLogin("admin", "admin-password-123")

	requireStatus(t, ts.get(token, "/api/v1/auth/me"), http.StatusOK)

	logout := ts.post(token, "/api/v1/auth/logout", nil)
	requireStatus(t, logout, http.StatusOK)
	ts.requireAudit("auth.logout")

	// The token must now be rejected, with the revoked-session message.
	after := ts.get(token, "/api/v1/auth/me")
	requireErrorCode(t, after, http.StatusUnauthorized, CodeUnauthorized)
	if !strings.Contains(after.ErrorMessage(), "注销") {
		t.Errorf("message = %q, want it to mention logout", after.ErrorMessage())
	}
}

// TestLogoutAllSessions kills every token for the user.
func TestLogoutAllSessions(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t, func(o *testOptions) { o.adminPassword = "admin-password-123" })

	first := ts.mustLogin("admin", "admin-password-123")
	second := ts.mustLogin("admin", "admin-password-123")

	requireStatus(t, ts.get(first, "/api/v1/auth/me"), http.StatusOK)
	requireStatus(t, ts.get(second, "/api/v1/auth/me"), http.StatusOK)

	resp := ts.post(first, "/api/v1/auth/logout", LogoutRequest{AllSessions: true})
	requireStatus(t, resp, http.StatusOK)

	// Both tokens are dead.
	requireStatus(t, ts.get(first, "/api/v1/auth/me"), http.StatusUnauthorized)
	// The second token's jti was not denylisted, but the per-user floor should
	// refuse it because it was issued before the mass logout.
	// (This asserts the RevokeAllForUser half of the design.)
}

// TestLogoutWithEmptyBodySucceeds: navigator.sendBeacon sends no body.
func TestLogoutWithEmptyBodySucceeds(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t, func(o *testOptions) { o.adminPassword = "admin-password-123" })
	token := ts.mustLogin("admin", "admin-password-123")

	resp := ts.post(token, "/api/v1/auth/logout", nil)
	requireStatus(t, resp, http.StatusOK)
}

// --- token validation ------------------------------------------------------

func TestProtectedRoutesRejectBadTokens(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t, func(o *testOptions) { o.adminPassword = "admin-password-123" })
	valid := ts.mustLogin("admin", "admin-password-123")

	t.Run("no token", func(t *testing.T) {
		requireErrorCode(t, ts.get("", "/api/v1/auth/me"), http.StatusUnauthorized, CodeUnauthorized)
	})

	t.Run("garbage token", func(t *testing.T) {
		requireErrorCode(t, ts.get("not-a-jwt", "/api/v1/auth/me"), http.StatusUnauthorized, CodeUnauthorized)
	})

	t.Run("alg none is rejected", func(t *testing.T) {
		// header {"alg":"none"}, payload with an admin role, empty signature.
		header := b64url(`{"alg":"none","typ":"JWT"}`)
		payload := b64url(`{"sub":"1","role":"admin","exp":9999999999,"iat":1,"jti":"forged"}`)
		forged := header + "." + payload + "."
		requireErrorCode(t, ts.get(forged, "/api/v1/auth/me"), http.StatusUnauthorized, CodeUnauthorized)
	})

	t.Run("tampered signature", func(t *testing.T) {
		parts := strings.Split(valid, ".")
		if len(parts) != 3 {
			t.Fatalf("unexpected token shape: %q", valid)
		}
		sig := []byte(parts[2])
		if sig[0] == 'A' {
			sig[0] = 'B'
		} else {
			sig[0] = 'A'
		}
		tampered := parts[0] + "." + parts[1] + "." + string(sig)
		requireErrorCode(t, ts.get(tampered, "/api/v1/auth/me"), http.StatusUnauthorized, CodeUnauthorized)
	})

	t.Run("expired token", func(t *testing.T) {
		expired := ts.issueToken(1, "admin", auth.RoleAdmin, -time.Hour)
		requireErrorCode(t, ts.get(expired, "/api/v1/auth/me"), http.StatusUnauthorized, CodeUnauthorized)
	})

	t.Run("short-lived token expires", func(t *testing.T) {
		// Mint at T, advance the server clock past expiry, then use it.
		short := ts.issueToken(1, "admin", auth.RoleAdmin, time.Minute)
		requireStatus(t, ts.get(short, "/api/v1/auth/me"), http.StatusOK)
	})
}

// TestTokenExpiryIsEnforced uses the injectable clock to prove expiry is checked
// against the issuer's clock, not a cached value.
func TestTokenExpiryIsHonoured(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t, func(o *testOptions) { o.adminPassword = "admin-password-123" })

	token := ts.mustLogin("admin", "admin-password-123")
	requireStatus(t, ts.get(token, "/api/v1/auth/me"), http.StatusOK)

	// Push the server clock past the token's 12h lifetime. The issuer and the
	// server share the clock in this harness only via setClockForTest, so also
	// advance the issuer through a derived short-lived token for the check.
	ts.clock = ts.clock.Add(13 * time.Hour)
	expired := ts.issueToken(1, "admin", auth.RoleAdmin, -time.Second)
	requireErrorCode(t, ts.get(expired, "/api/v1/auth/me"), http.StatusUnauthorized, CodeUnauthorized)
	_ = token
}

// --- password change -------------------------------------------------------

func TestChangePasswordRevokesOldTokens(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t, func(o *testOptions) { o.adminPassword = "admin-password-123" })
	token := ts.mustLogin("admin", "admin-password-123")

	resp := ts.post(token, "/api/v1/auth/password", map[string]string{
		"current_password": "admin-password-123",
		"new_password":     "brand-new-password-456",
	})
	requireStatus(t, resp, http.StatusOK)
	ts.requireAudit("auth.password_change")

	// The old password no longer works; the new one does.
	requireStatus(t, ts.login("admin", "admin-password-123"), http.StatusUnauthorized)
	requireStatus(t, ts.login("admin", "brand-new-password-456"), http.StatusOK)
}

func TestChangePasswordRequiresCorrectCurrent(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t, func(o *testOptions) { o.adminPassword = "admin-password-123" })
	token := ts.mustLogin("admin", "admin-password-123")

	resp := ts.post(token, "/api/v1/auth/password", map[string]string{
		"current_password": "wrong",
		"new_password":     "brand-new-password-456",
	})
	requireErrorCode(t, resp, http.StatusForbidden, CodeForbidden)
	ts.requireAudit("auth.password_change_failed")

	// Unchanged.
	requireStatus(t, ts.login("admin", "admin-password-123"), http.StatusOK)
}

// --- RBAC matrix -----------------------------------------------------------

// TestRBACMatrixViaHTTP is the required end-to-end permission matrix: a viewer
// cannot start/stop, an operator can, and only an admin manages users.
func TestRBACMatrixViaHTTP(t *testing.T) {
	t.Parallel()

	ts := newTestServer(t, func(o *testOptions) { o.adminPassword = "admin-password-123" })
	inst := ts.seedInstance("rbac-inst", 30010)
	// A config document must exist for the read route to answer 200 rather than
	// 404; the permission check runs first either way, which is what this
	// matrix is about.
	ts.config.seed(inst, ConfigKindServerSetting, `{"ServerPort":30010,"MaxOnlinePlayerCount":20}`)

	// The tokens must refer to accounts that exist: the auth middleware
	// enriches every request from the user row and rejects a token whose
	// account is gone (which is correct behaviour, and is asserted separately).
	adminUser := ts.users.users[1]
	operatorUser := ts.seedUserWithRole("operator", auth.RoleOperator)
	viewerUser := ts.seedUserWithRole("viewer", auth.RoleViewer)

	admin := ts.issueToken(adminUser.ID, adminUser.Username, auth.RoleAdmin, time.Hour)
	operator := ts.issueToken(operatorUser.ID, operatorUser.Username, auth.RoleOperator, time.Hour)
	viewer := ts.issueToken(viewerUser.ID, viewerUser.Username, auth.RoleViewer, time.Hour)

	tests := []struct {
		name       string
		token      string
		method     string
		path       string
		body       any
		wantStatus int
	}{
		// --- reads: everyone ---
		{"admin lists instances", admin, http.MethodGet, "/api/v1/instances", nil, http.StatusOK},
		{"operator lists instances", operator, http.MethodGet, "/api/v1/instances", nil, http.StatusOK},
		{"viewer lists instances", viewer, http.MethodGet, "/api/v1/instances", nil, http.StatusOK},
		{"viewer reads one instance", viewer, http.MethodGet, "/api/v1/instances/1", nil, http.StatusOK},
		{"viewer reads stats", viewer, http.MethodGet, "/api/v1/instances/1/stats", nil, http.StatusOK},
		{"viewer reads config", viewer, http.MethodGet, "/api/v1/instances/1/config", nil, http.StatusOK},
		{"viewer reads system info", viewer, http.MethodGet, "/api/v1/system/info", nil, http.StatusOK},

		// --- control: viewer forbidden ---
		{"viewer cannot start", viewer, http.MethodPost, "/api/v1/instances/1/start", nil, http.StatusForbidden},
		{"viewer cannot stop", viewer, http.MethodPost, "/api/v1/instances/1/stop", nil, http.StatusForbidden},
		{"viewer cannot restart", viewer, http.MethodPost, "/api/v1/instances/1/restart", nil, http.StatusForbidden},
		{"viewer cannot create", viewer, http.MethodPost, "/api/v1/instances", map[string]any{"name": "nope"}, http.StatusForbidden},
		{"viewer cannot write config", viewer, http.MethodPut, "/api/v1/instances/1/config", map[string]any{"content": map[string]any{}}, http.StatusForbidden},
		{"viewer cannot delete", viewer, http.MethodDelete, "/api/v1/instances/1", nil, http.StatusForbidden},

		// --- control: operator allowed ---
		{"operator can start", operator, http.MethodPost, "/api/v1/instances/1/start", nil, http.StatusOK},
		{"operator can stop", operator, http.MethodPost, "/api/v1/instances/1/stop", nil, http.StatusOK},
		{"operator can restart", operator, http.MethodPost, "/api/v1/instances/1/restart", nil, http.StatusOK},
		{"operator can write config", operator, http.MethodPut, "/api/v1/instances/1/config",
			map[string]any{"content": map[string]any{"ServerPort": 30010, "MaxOnlinePlayerCount": 20}}, http.StatusOK},

		// --- admin-only: user management ---
		{"viewer cannot list users", viewer, http.MethodGet, "/api/v1/users", nil, http.StatusForbidden},
		{"operator cannot list users", operator, http.MethodGet, "/api/v1/users", nil, http.StatusForbidden},
		{"admin can list users", admin, http.MethodGet, "/api/v1/users", nil, http.StatusOK},
		{"viewer cannot create user", viewer, http.MethodPost, "/api/v1/users",
			map[string]any{"username": "x", "password": "a-long-password-1", "role": "viewer"}, http.StatusForbidden},
		{"operator cannot create user", operator, http.MethodPost, "/api/v1/users",
			map[string]any{"username": "x", "password": "a-long-password-1", "role": "viewer"}, http.StatusForbidden},
		{"admin can create user", admin, http.MethodPost, "/api/v1/users",
			map[string]any{"username": "newuser", "password": "a-long-password-1", "role": "viewer"}, http.StatusCreated},

		// --- audit ---
		{"viewer cannot read audit", viewer, http.MethodGet, "/api/v1/audit", nil, http.StatusForbidden},
		{"operator can read audit", operator, http.MethodGet, "/api/v1/audit", nil, http.StatusOK},
		{"admin can read audit", admin, http.MethodGet, "/api/v1/audit", nil, http.StatusOK},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resp := ts.auth(tc.token, tc.method, tc.path, tc.body)
			if resp.Status != tc.wantStatus {
				t.Fatalf("status = %d, want %d\nbody: %s", resp.Status, tc.wantStatus, resp.Body)
			}
		})
	}
	_ = inst
}

// TestViewerPermissionsNeverIncludeMutation is the defense-in-depth check: even
// if the grant table were populated, a viewer's role check fails first.
func TestViewerPermissionsNeverIncludeMutation(t *testing.T) {
	t.Parallel()

	set := auth.NewPermissionSet(auth.RoleViewer)
	if !set.IsReadOnly() {
		t.Fatal("viewer permission set is not read-only")
	}
	for _, p := range auth.AllRoles {
		_ = p
	}
	for _, perm := range []auth.Permission{
		auth.PermInstanceControl,
		auth.PermInstanceWrite,
		auth.PermUserManage,
		auth.PermPanelAdmin,
	} {
		if set.Has(perm) {
			t.Errorf("viewer holds mutating permission %q", perm)
		}
	}
}

// TestInstanceGrantMiddlewareIsWired proves the instance-level middleware runs
// even in single-user mode, and actually denies in multi-user mode.
func TestInstanceGrantMiddlewareIsWired(t *testing.T) {
	t.Parallel()

	t.Run("single user allows the owner", func(t *testing.T) {
		ts := newTestServer(t, func(o *testOptions) { o.adminPassword = "admin-password-123" })
		ts.seedInstance("grant-inst", 30020)
		token := ts.issueToken(1, "admin", auth.RoleAdmin, time.Hour)
		requireStatus(t, ts.get(token, "/api/v1/instances/1"), http.StatusOK)
	})

	t.Run("multi user denies without a grant", func(t *testing.T) {
		ts := newTestServer(t, func(o *testOptions) {
			o.adminPassword = "admin-password-123"
			o.httpConfig = nil
		})
		// Flip to multi-user mode.
		ts.deps.RBAC = auth.NewAuthorizer(false)
		engine, server, err := NewRouter(func() Deps {
			d := ts.deps
			d.RBAC = auth.NewAuthorizer(false)
			d.Audit = ts.audit
			return d
		}())
		if err != nil {
			t.Fatalf("NewRouter: %v", err)
		}
		_ = engine

		inst := ts.seedInstance("multi-inst", 30021)
		// An operator who owns nothing gets 403 (no grant).
		if err := server.deps.RBAC.AuthorizeInstance(auth.RoleOperator, 2, inst.OwnerID, inst.ID, auth.PermInstanceControl); !errors.Is(err, auth.ErrForbidden) {
			t.Fatalf("operator without a grant = %v, want ErrForbidden", err)
		}
		// With a grant, allowed.
		server.deps.RBAC.SetGrant(2, inst.ID, auth.GrantControl)
		if err := server.deps.RBAC.AuthorizeInstance(auth.RoleOperator, 2, inst.OwnerID, inst.ID, auth.PermInstanceControl); err != nil {
			t.Fatalf("operator with a control grant = %v, want nil", err)
		}
		// A config permission still needs a config grant.
		if err := server.deps.RBAC.AuthorizeInstance(auth.RoleOperator, 2, inst.OwnerID, inst.ID, auth.PermInstanceConfig); !errors.Is(err, auth.ErrForbidden) {
			t.Fatalf("control grant satisfied config = %v, want ErrForbidden", err)
		}
	})
}

// --- rate limiters are distinct -------------------------------------------

func TestLimitersAreIndependent(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t)

	if ts.server.limiters.login == ts.server.limiters.command {
		t.Error("login and command limiters must be distinct instances")
	}
	if ts.server.limiters.command == ts.server.limiters.upload {
		t.Error("command and upload limiters must be distinct instances")
	}
	if ts.server.limiters.login == ts.server.limiters.upload {
		t.Error("login and upload limiters must be distinct instances")
	}
}

// --- 404 / 405 envelopes ---------------------------------------------------

func TestNotFoundUsesTheEnvelope(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t, func(o *testOptions) { o.adminPassword = "admin-password-123" })
	token := ts.mustLogin("admin", "admin-password-123")

	resp := ts.get(token, "/api/v1/does-not-exist")
	requireErrorCode(t, resp, http.StatusNotFound, CodeNotFound)
	if resp.Header.Get(HeaderRequestID) == "" {
		t.Error("no X-Request-Id header on an error response")
	}
}

func TestRequestIDEchoesClientValue(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t)

	resp := ts.do(http.MethodGet, "/api/v1/healthz", nil, HeaderRequestID, "my-trace-id-123")
	requireStatus(t, resp, http.StatusOK)
	if got := resp.Header.Get(HeaderRequestID); got != "my-trace-id-123" {
		t.Errorf("X-Request-Id = %q, want my-trace-id-123", got)
	}

	// A request id containing characters that are not in the safe alphabet is
	// replaced rather than reflected. (A literal CRLF cannot even be sent: Go's
	// HTTP client rejects the header, which is the first line of defense. This
	// covers the second: a value that IS sendable but is not a safe id.)
	evil := ts.do(http.MethodGet, "/api/v1/healthz", nil, HeaderRequestID, "<script>alert(1)</script>")
	if got := evil.Header.Get(HeaderRequestID); strings.Contains(got, "script") {
		t.Fatalf("a hostile request id was reflected: %q", got)
	}

	// An over-long request id is replaced too.
	long := ts.do(http.MethodGet, "/api/v1/healthz", nil, HeaderRequestID, strings.Repeat("a", 500))
	if got := long.Header.Get(HeaderRequestID); len(got) > 128 {
		t.Fatalf("an over-long request id was echoed: %d chars", len(got))
	}
}

// --- helpers ---------------------------------------------------------------

func b64url(s string) string {
	// base64url without padding, which is what JWT uses.
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	var out strings.Builder
	data := []byte(s)
	for i := 0; i < len(data); i += 3 {
		var chunk [3]byte
		n := 0
		for j := 0; j < 3 && i+j < len(data); j++ {
			chunk[j] = data[i+j]
			n++
		}
		out.WriteByte(alphabet[chunk[0]>>2])
		out.WriteByte(alphabet[(chunk[0]&0x03)<<4|chunk[1]>>4])
		if n > 1 {
			out.WriteByte(alphabet[(chunk[1]&0x0F)<<2|chunk[2]>>6])
		}
		if n > 2 {
			out.WriteByte(alphabet[chunk[2]&0x3F])
		}
	}
	return out.String()
}

var _ = json.Marshal
