package auth

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func TestHashAndVerifyPassword(t *testing.T) {
	t.Parallel()

	hash, err := HashPassword("correct-horse-battery")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if !strings.HasPrefix(hash, "$2") {
		t.Fatalf("expected a bcrypt hash, got %q", hash)
	}
	if err := VerifyPassword(hash, "correct-horse-battery"); err != nil {
		t.Fatalf("VerifyPassword(correct): %v", err)
	}
	if err := VerifyPassword(hash, "wrong"); !errors.Is(err, ErrPasswordMismatch) {
		t.Fatalf("VerifyPassword(wrong) = %v, want ErrPasswordMismatch", err)
	}
}

func TestVerifyPasswordEmptyHashIsSetupRequired(t *testing.T) {
	t.Parallel()

	// The first-run bootstrap state: the seeded admin row has password_hash=''.
	for _, hash := range []string{"", "   ", "\n"} {
		if err := VerifyPassword(hash, "anything"); !errors.Is(err, ErrPasswordEmpty) {
			t.Errorf("VerifyPassword(%q) = %v, want ErrPasswordEmpty", hash, err)
		}
	}
}

func TestValidatePasswordPolicy(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		password string
		wantErr  error
	}{
		{"valid", "s3cure-panel-pass", nil},
		{"too short", "short", ErrPasswordTooShort},
		{"seven chars", "abcdefg", ErrPasswordTooShort},
		{"exactly eight", "abcdefgh", nil},
		{"too long", strings.Repeat("a", 73), ErrPasswordTooLong},
		{"exactly 72", strings.Repeat("a", 72), nil},
		{"common", "password", ErrPasswordTooCommon},
		{"common long", "password1", ErrPasswordTooCommon},
		{"common mixed case", "PassWord1", ErrPasswordTooCommon},
		{"common with padding", "  letmein  ", ErrPasswordTooCommon},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidatePassword(tc.password)
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("ValidatePassword(%q) = %v, want nil", tc.password, err)
				}
				return
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("ValidatePassword(%q) = %v, want %v", tc.password, err, tc.wantErr)
			}
		})
	}
}

func TestHashPasswordRejectsWeakInput(t *testing.T) {
	t.Parallel()

	if _, err := HashPassword("short"); !errors.Is(err, ErrPasswordTooShort) {
		t.Fatalf("got %v, want ErrPasswordTooShort", err)
	}
}

func TestNeedsRehash(t *testing.T) {
	t.Parallel()

	if NeedsRehash("not-a-hash") {
		t.Error("garbage hash should not report needing rehash")
	}
	if NeedsRehash("") {
		t.Error("empty hash should not report needing rehash")
	}
	// A cost-4 hash is weaker than BcryptCost and should be flagged.
	weak := "$2a$04$123456789012345678901u3oSZ7R8m1kYQ0m4GkS5sBO8f4k1qQkS"
	if !NeedsRehash(weak) {
		t.Error("cost-4 hash should report needing rehash")
	}
	strong, err := HashPassword("a-good-password-here")
	if err != nil {
		t.Fatal(err)
	}
	if NeedsRehash(strong) {
		t.Errorf("freshly hashed password at cost %d should not need rehash", BcryptCost)
	}
}

func TestSanitizeUsername(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{in: "Admin", want: "admin"},
		{in: "  Operator  ", want: "operator"},
		{in: "view-er_1", want: "view-er_1"},
		{in: "", wantErr: true},
		{in: "   ", wantErr: true},
		{in: "has space", wantErr: true},
		{in: "has\ttab", wantErr: true},
		{in: "bad/slash", wantErr: true},
		{in: "bad\\slash", wantErr: true},
		{in: "inject;rm -rf", wantErr: true},
		{in: "sub$var", wantErr: true},
		{in: strings.Repeat("a", 65), wantErr: true},
		{in: strings.Repeat("a", 64), want: strings.Repeat("a", 64)},
	}
	for _, tc := range tests {
		got, err := SanitizeUsername(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("SanitizeUsername(%q) = %q, want error", tc.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("SanitizeUsername(%q): %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("SanitizeUsername(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestRandomTokenAndConstantTimeEqual(t *testing.T) {
	t.Parallel()

	a, err := RandomToken(16)
	if err != nil {
		t.Fatal(err)
	}
	b, err := RandomToken(16)
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatal("two random tokens collided")
	}
	if len(a) < 16 {
		t.Fatalf("token %q too short", a)
	}
	if !ConstantTimeEqual(a, a) {
		t.Error("ConstantTimeEqual should match identical strings")
	}
	if ConstantTimeEqual(a, b) {
		t.Error("ConstantTimeEqual should not match different strings")
	}
	if ConstantTimeEqual("", "x") {
		t.Error("ConstantTimeEqual(\"\", \"x\") should be false")
	}
}

// --- JWT -------------------------------------------------------------------

func newTestIssuer(t *testing.T, opts ...TokenIssuerOption) *TokenIssuer {
	t.Helper()
	iss, err := NewTokenIssuer("test-secret-that-is-long-enough-0123456789", opts...)
	if err != nil {
		t.Fatalf("NewTokenIssuer: %v", err)
	}
	return iss
}

func TestNewTokenIssuerRejectsShortSecret(t *testing.T) {
	t.Parallel()

	if _, err := NewTokenIssuer("too-short"); !errors.Is(err, ErrSecretTooShort) {
		t.Fatalf("got %v, want ErrSecretTooShort", err)
	}
}

func TestIssueAndParseRoundTrip(t *testing.T) {
	t.Parallel()

	iss := newTestIssuer(t)
	tok, want, err := iss.Issue(1, "admin", RoleAdmin)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	got, err := iss.Parse(tok)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got.Subject != "1" {
		t.Errorf("sub = %q, want 1", got.Subject)
	}
	if got.Role != RoleAdmin {
		t.Errorf("role = %q, want admin", got.Role)
	}
	if got.Username != "admin" {
		t.Errorf("username = %q, want admin", got.Username)
	}
	if got.ID != want.ID || got.ID == "" {
		t.Errorf("jti = %q, want %q (non-empty)", got.ID, want.ID)
	}
	if got.Issuer != DefaultIssuer {
		t.Errorf("iss = %q, want %q", got.Issuer, DefaultIssuer)
	}
	if got.IssuedAt == nil {
		t.Error("iat claim missing")
	}
	if got.ExpiresAt == nil {
		t.Fatal("exp claim missing")
	}
	uid, err := got.UserID()
	if err != nil || uid != 1 {
		t.Errorf("UserID() = %d, %v; want 1, nil", uid, err)
	}
	if got.Expiry().Before(time.Now()) {
		t.Errorf("token already expired: %v", got.Expiry())
	}
}

func TestParseAcceptsBearerPrefix(t *testing.T) {
	t.Parallel()

	iss := newTestIssuer(t)
	tok, _, err := iss.Issue(7, "op", RoleOperator)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := iss.Parse("Bearer " + tok); err != nil {
		t.Fatalf("Parse with Bearer prefix: %v", err)
	}
	if _, err := iss.Parse("  Bearer   " + tok + "  "); err != nil {
		t.Fatalf("Parse with padded Bearer prefix: %v", err)
	}
}

func TestParseRejectsExpiredToken(t *testing.T) {
	t.Parallel()

	clock := time.Now()
	iss := newTestIssuer(t,
		WithAccessTTL(time.Minute),
		withClock(func() time.Time { return clock }),
	)
	tok, _, err := iss.Issue(1, "admin", RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}
	// Advance well past expiry (plus the 30s nbf skew allowance).
	iss.now = func() time.Time { return clock.Add(2 * time.Minute) }
	if _, err := iss.Parse(tok); !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("expired token Parse = %v, want ErrTokenInvalid", err)
	}
}

func TestParseRejectsTamperedSignature(t *testing.T) {
	t.Parallel()

	iss := newTestIssuer(t)
	tok, _, err := iss.Issue(1, "admin", RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}

	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		t.Fatalf("expected 3 JWT segments, got %d", len(parts))
	}

	t.Run("flipped signature byte", func(t *testing.T) {
		sig := []byte(parts[2])
		if sig[0] == 'A' {
			sig[0] = 'B'
		} else {
			sig[0] = 'A'
		}
		bad := parts[0] + "." + parts[1] + "." + string(sig)
		if _, err := iss.Parse(bad); err == nil {
			t.Fatal("tampered signature accepted")
		}
	})

	t.Run("tampered payload keeps old signature", func(t *testing.T) {
		// Re-encode a payload claiming role=admin with the original signature.
		evil := jwt.NewWithClaims(jwt.SigningMethodHS256, &Claims{
			Role: RoleAdmin,
			RegisteredClaims: jwt.RegisteredClaims{
				Subject:   "999",
				Issuer:    DefaultIssuer,
				ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
				IssuedAt:  jwt.NewNumericDate(time.Now()),
				ID:        "forged",
			},
		})
		// Hand-sign with the WRONG key to simulate an attacker.
		evilStr, err := evil.SignedString([]byte("wrong-secret-wrong-secret-wrong!!"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := iss.Parse(evilStr); !errors.Is(err, ErrTokenInvalid) {
			t.Fatalf("wrong-key token Parse = %v, want ErrTokenInvalid", err)
		}
	})

	t.Run("cross-secret issuer", func(t *testing.T) {
		other, err := NewTokenIssuer("another-secret-that-is-long-enough-1234")
		if err != nil {
			t.Fatal(err)
		}
		otherTok, _, err := other.Issue(1, "admin", RoleAdmin)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := iss.Parse(otherTok); err == nil {
			t.Fatal("token signed by a different secret accepted")
		}
	})
}

func TestParseRejectsAlgNone(t *testing.T) {
	t.Parallel()

	iss := newTestIssuer(t)

	// Classic alg=none forgery: header {"alg":"none","typ":"JWT"}, empty sig.
	claims := &Claims{
		Role: RoleAdmin,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   "1",
			Issuer:    DefaultIssuer,
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			ID:        "none-attack",
		},
	}
	noneTok, err := jwt.NewWithClaims(jwt.SigningMethodNone, claims).
		SignedString(jwt.UnsafeAllowNoneSignatureType)
	if err != nil {
		t.Fatalf("building alg=none token: %v", err)
	}
	if _, err := iss.Parse(noneTok); !errors.Is(err, ErrTokenAlgorithm) && !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("alg=none Parse = %v, want algorithm rejection", err)
	}

	// Also assert the raw algorithm pinning: an RS256 header must not cause the
	// HMAC secret to be used as a public key.
	rs := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	_ = rs
	if _, err := iss.Parse(noneTok + "x"); err == nil {
		t.Fatal("malformed token accepted")
	}
}

func TestParseRejectsMissingClaims(t *testing.T) {
	t.Parallel()

	iss := newTestIssuer(t)

	tests := []struct {
		name   string
		claims jwt.Claims
	}{
		{
			name: "no jti",
			claims: &Claims{Role: RoleAdmin, RegisteredClaims: jwt.RegisteredClaims{
				Subject: "1", Issuer: DefaultIssuer,
				ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
			}},
		},
		{
			name: "no sub",
			claims: &Claims{Role: RoleAdmin, RegisteredClaims: jwt.RegisteredClaims{
				Issuer: DefaultIssuer, ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)), ID: "x",
			}},
		},
		{
			name: "unknown role",
			claims: &Claims{Role: Role("superuser"), RegisteredClaims: jwt.RegisteredClaims{
				Subject: "1", Issuer: DefaultIssuer,
				ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)), ID: "x",
			}},
		},
		{
			name: "no expiry",
			claims: &Claims{Role: RoleAdmin, RegisteredClaims: jwt.RegisteredClaims{
				Subject: "1", Issuer: DefaultIssuer, ID: "x",
			}},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tok, err := jwt.NewWithClaims(jwt.SigningMethodHS256, tc.claims).
				SignedString([]byte("test-secret-that-is-long-enough-0123456789"))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := iss.Parse(tok); err == nil {
				t.Fatalf("token with %s accepted", tc.name)
			}
		})
	}
}

func TestParseRejectsEmptyAndGarbage(t *testing.T) {
	t.Parallel()

	iss := newTestIssuer(t)
	for _, in := range []string{"", "   ", "not.a.jwt", "a.b.c", "Bearer", "Bearer ", "...."} {
		if _, err := iss.Parse(in); err == nil {
			t.Errorf("Parse(%q) accepted garbage", in)
		}
	}
}

func TestIssueRejectsBadInput(t *testing.T) {
	t.Parallel()

	iss := newTestIssuer(t)
	if _, _, err := iss.Issue(0, "admin", RoleAdmin); err == nil {
		t.Error("Issue with userID=0 should fail")
	}
	if _, _, err := iss.Issue(-1, "admin", RoleAdmin); err == nil {
		t.Error("Issue with userID=-1 should fail")
	}
	if _, _, err := iss.Issue(1, "admin", Role("nope")); err == nil {
		t.Error("Issue with invalid role should fail")
	}
}

func TestAccessTTLAndAudience(t *testing.T) {
	t.Parallel()

	iss := newTestIssuer(t, WithAccessTTL(90*time.Minute), WithAudience("scnetm-web"), WithIssuer("custom"))
	if iss.AccessTTL() != 90*time.Minute {
		t.Errorf("AccessTTL() = %v, want 90m", iss.AccessTTL())
	}
	tok, _, err := iss.Issue(1, "admin", RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}
	got, err := iss.Parse(tok)
	if err != nil {
		t.Fatal(err)
	}
	if got.Issuer != "custom" {
		t.Errorf("iss = %q, want custom", got.Issuer)
	}
	if len(got.Audience) != 1 || got.Audience[0] != "scnetm-web" {
		t.Errorf("aud = %v, want [scnetm-web]", got.Audience)
	}
}

func TestWithAccessTTLIgnoresNonPositive(t *testing.T) {
	t.Parallel()

	iss := newTestIssuer(t, WithAccessTTL(0), WithAccessTTL(-time.Hour))
	if iss.AccessTTL() != DefaultAccessTTL {
		t.Errorf("AccessTTL() = %v, want default %v", iss.AccessTTL(), DefaultAccessTTL)
	}
}

// --- secret persistence ----------------------------------------------------

func TestLoadOrCreateSecret(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "jwt.secret")

	first, created, err := LoadOrCreateSecret(path)
	if err != nil {
		t.Fatalf("LoadOrCreateSecret: %v", err)
	}
	if !created {
		t.Error("first call should report created=true")
	}
	if len(first) < MinSecretLength {
		t.Errorf("secret too short: %d bytes", len(first))
	}

	// Permissions must be 0600 — never world readable.
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := st.Mode().Perm(); perm != 0o600 {
		t.Errorf("secret file mode = %o, want 600", perm)
	}

	// Second call must return the SAME secret (persistence, not rotation).
	second, created, err := LoadOrCreateSecret(path)
	if err != nil {
		t.Fatalf("second LoadOrCreateSecret: %v", err)
	}
	if created {
		t.Error("second call should report created=false")
	}
	if second != first {
		t.Error("secret changed between calls; sessions would break on restart")
	}
}

func TestLoadOrCreateSecretReplacesWeakSecret(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "jwt.secret")
	if err := os.WriteFile(path, []byte("weak\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, created, err := LoadOrCreateSecret(path)
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Error("a too-short existing secret should be rotated (created=true)")
	}
	if len(got) < MinSecretLength {
		t.Errorf("rotated secret too short: %d", len(got))
	}
}

func TestLoadOrCreateSecretRequiresPath(t *testing.T) {
	t.Parallel()

	if _, _, err := LoadOrCreateSecret(""); err == nil {
		t.Fatal("empty path should error")
	}
}

// --- RBAC ------------------------------------------------------------------

func TestRoleValidityAndRanking(t *testing.T) {
	t.Parallel()

	for _, r := range AllRoles {
		if !r.Valid() {
			t.Errorf("%q should be valid", r)
		}
	}
	if Role("root").Valid() {
		t.Error("unknown role reported valid")
	}
	if !RoleAdmin.AtLeast(RoleViewer) || !RoleAdmin.AtLeast(RoleAdmin) {
		t.Error("admin should be at least admin and viewer")
	}
	if RoleViewer.AtLeast(RoleOperator) {
		t.Error("viewer should not be at least operator")
	}
	if Role("bogus").AtLeast(RoleViewer) {
		t.Error("unknown role should not satisfy AtLeast")
	}
}

func TestParseRole(t *testing.T) {
	t.Parallel()

	for _, in := range []string{"admin", "ADMIN", " Admin ", "operator", "viewer"} {
		if _, err := ParseRole(in); err != nil {
			t.Errorf("ParseRole(%q): %v", in, err)
		}
	}
	if _, err := ParseRole("root"); !errors.Is(err, ErrUnknownRole) {
		t.Errorf("ParseRole(root) = %v, want ErrUnknownRole", err)
	}
}

// TestRBACMatrix is the required role/permission matrix: viewers can read but
// never control; operators can control instances but not manage users; admins
// can do everything.
func TestRBACMatrix(t *testing.T) {
	t.Parallel()

	tests := []struct {
		role Role
		perm Permission
		want bool
	}{
		// viewer: read-only
		{RoleViewer, PermInstanceRead, true},
		{RoleViewer, PermSystemRead, true},
		{RoleViewer, PermInstanceControl, false},
		{RoleViewer, PermInstanceWrite, false},
		{RoleViewer, PermUserManage, false},
		{RoleViewer, PermJobManage, false},
		{RoleViewer, PermPanelAdmin, false},
		// viewer holds the *:read half of every resource and NOT the write half
		{RoleViewer, PermInstanceConfigRead, true},
		{RoleViewer, PermInstanceConfig, false},
		{RoleViewer, PermInstanceFileRead, true},
		{RoleViewer, PermInstanceFile, false},
		{RoleViewer, PermInstanceBackupRead, true},
		{RoleViewer, PermInstanceBackup, false},
		{RoleViewer, PermInstanceLogRead, true},

		// operator: control + config, no user management
		{RoleOperator, PermInstanceRead, true},
		{RoleOperator, PermInstanceControl, true},
		{RoleOperator, PermInstanceWrite, true},
		{RoleOperator, PermInstanceConfig, true},
		{RoleOperator, PermInstanceConfigRead, true},
		{RoleOperator, PermInstanceFile, true},
		{RoleOperator, PermInstanceFileRead, true},
		{RoleOperator, PermInstanceBackup, true},
		{RoleOperator, PermInstanceBackupRead, true},
		{RoleOperator, PermInstanceLogRead, true},
		{RoleOperator, PermJobManage, true},
		{RoleOperator, PermUserManage, false},
		{RoleOperator, PermPanelAdmin, false},

		// admin: everything
		{RoleAdmin, PermInstanceControl, true},
		{RoleAdmin, PermInstanceWrite, true},
		{RoleAdmin, PermUserManage, true},
		{RoleAdmin, PermPanelAdmin, true},
		{RoleAdmin, PermAuditRead, true},
	}
	for _, tc := range tests {
		t.Run(string(tc.role)+"/"+string(tc.perm), func(t *testing.T) {
			if got := tc.role.Can(tc.perm); got != tc.want {
				t.Errorf("%s.Can(%s) = %v, want %v", tc.role, tc.perm, got, tc.want)
			}
		})
	}
}

func TestViewerNeverGetsMutatingPermissions(t *testing.T) {
	t.Parallel()

	set := NewPermissionSet(RoleViewer)
	if !set.IsReadOnly() {
		t.Error("viewer permission set should be read-only")
	}
	for _, p := range set.List() {
		if !readOnlyPermissions[p] {
			t.Errorf("viewer granted mutating permission %q", p)
		}
	}
	for _, role := range []Role{RoleAdmin, RoleOperator} {
		if NewPermissionSet(role).IsReadOnly() {
			t.Errorf("%s permission set should not be read-only", role)
		}
	}
}

func TestAuthorizerSingleUserBypassesGrants(t *testing.T) {
	t.Parallel()

	a := NewAuthorizer(true)
	if !a.SingleUser() {
		t.Fatal("expected single-user mode")
	}
	// No explicit grant needed: the owner implicitly holds everything.
	if err := a.AuthorizeInstance(RoleAdmin, 1, 1, 42, PermInstanceControl); err != nil {
		t.Errorf("single-user admin should control any instance: %v", err)
	}
	if g, ok := a.GrantsFor(1, 42); !ok || g != GrantConfig {
		t.Errorf("GrantsFor in single-user mode = %q,%v; want config,true", g, ok)
	}
	// SetGrant stays a no-op so the table remains empty (§5.5).
	a.SetGrant(1, 42, GrantRead)
	if len(a.grants) != 0 {
		t.Error("single-user mode must not populate the grant table")
	}
}

func TestAuthorizerInstanceGrants(t *testing.T) {
	t.Parallel()

	a := NewAuthorizer(false)

	// Operator with a grant on instance 10 only.
	a.SetGrant(2, 10, GrantControl)

	if err := a.AuthorizeInstance(RoleOperator, 2, 1, 10, PermInstanceControl); err != nil {
		t.Errorf("granted control should be allowed: %v", err)
	}
	// No grant on instance 11.
	if err := a.AuthorizeInstance(RoleOperator, 2, 1, 11, PermInstanceControl); !errors.Is(err, ErrForbidden) {
		t.Errorf("ungranted instance = %v, want ErrForbidden", err)
	}
	// control does not imply config.
	if err := a.AuthorizeInstance(RoleOperator, 2, 1, 10, PermInstanceConfig); !errors.Is(err, ErrForbidden) {
		t.Errorf("control should not satisfy config: %v", err)
	}
	// The role check still applies first: a viewer can never control.
	if err := a.AuthorizeInstance(RoleViewer, 3, 1, 10, PermInstanceControl); !errors.Is(err, ErrForbidden) {
		t.Errorf("viewer + control grant = %v, want ErrForbidden", err)
	}
	// Admins bypass grants entirely.
	if err := a.AuthorizeInstance(RoleAdmin, 9, 1, 77, PermInstanceConfig); err != nil {
		t.Errorf("admin should bypass grants: %v", err)
	}
	// Owners always have full access to their own instance.
	if err := a.AuthorizeInstance(RoleOperator, 5, 5, 99, PermInstanceConfig); err != nil {
		t.Errorf("owner should have full access: %v", err)
	}
	// A panel-level permission is never satisfiable by an instance grant.
	if err := a.AuthorizeInstance(RoleOperator, 2, 1, 10, PermJobManage); !errors.Is(err, ErrForbidden) {
		t.Errorf("instance grant must not satisfy panel-level perm: %v", err)
	}
}

func TestAuthorizeRoleLevel(t *testing.T) {
	t.Parallel()

	a := NewAuthorizer(true)
	if err := a.Authorize(RoleAdmin, PermUserManage); err != nil {
		t.Errorf("admin user manage: %v", err)
	}
	if err := a.Authorize(RoleViewer, PermInstanceControl); !errors.Is(err, ErrForbidden) {
		t.Errorf("viewer control = %v, want ErrForbidden", err)
	}
	if err := a.Authorize(Role("bogus"), PermInstanceRead); !errors.Is(err, ErrForbidden) {
		t.Errorf("unknown role = %v, want ErrForbidden", err)
	}
}

func TestSetGrantIsCumulative(t *testing.T) {
	t.Parallel()

	a := NewAuthorizer(false)
	a.SetGrant(1, 5, GrantControl)
	a.SetGrant(1, 5, GrantRead) // must not downgrade
	if g, _ := a.GrantsFor(1, 5); g != GrantControl {
		t.Errorf("grant downgraded to %q, want control", g)
	}
	a.SetGrant(1, 5, GrantConfig) // upgrade
	if g, _ := a.GrantsFor(1, 5); g != GrantConfig {
		t.Errorf("grant = %q, want config", g)
	}
}

func TestGrantPermValid(t *testing.T) {
	t.Parallel()

	for _, g := range []GrantPerm{GrantRead, GrantControl, GrantConfig} {
		if !g.Valid() {
			t.Errorf("%q should be valid", g)
		}
	}
	if GrantPerm("execute").Valid() {
		t.Error("unknown grant reported valid")
	}
}

func TestPermissionSetList(t *testing.T) {
	t.Parallel()

	set := NewPermissionSet(RoleAdmin)
	list := set.List()
	if len(list) == 0 {
		t.Fatal("admin permission set is empty")
	}
	for i := 1; i < len(list); i++ {
		if list[i-1] >= list[i] {
			t.Fatalf("permission list not sorted: %v", list)
		}
	}
	if set.Role() != RoleAdmin {
		t.Errorf("Role() = %q, want admin", set.Role())
	}
}
