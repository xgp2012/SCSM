package auth

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// JWT configuration defaults. Access tokens are short-lived (see §5.7 "JWT
// 短期 + 刷新"); the panel additionally keeps a server-side denylist so logout
// is immediate rather than "eventually, when the token expires".
const (
	// DefaultAccessTTL is the lifetime of an access token.
	DefaultAccessTTL = 12 * time.Hour
	// DefaultIssuer identifies tokens minted by this panel.
	DefaultIssuer = "scnetm"
	// MinSecretLength is the shortest signing secret we accept. HS256 with a
	// short secret is trivially brute-forceable offline.
	MinSecretLength = 32
)

// Claims is the JWT payload used by the panel. It satisfies jwt.Claims via the
// embedded RegisteredClaims while adding the panel's own role field.
type Claims struct {
	// Role is the user's RBAC role (admin/operator/viewer).
	Role Role `json:"role"`
	// Username is carried for convenience so the UI can render it without an
	// extra round trip. It is advisory only; authorization always uses Sub.
	Username string `json:"username,omitempty"`
	jwt.RegisteredClaims
}

// Token pair vocabulary.
var (
	// ErrTokenInvalid is returned for any token that fails validation:
	// bad signature, expired, wrong issuer/audience, malformed.
	ErrTokenInvalid = errors.New("auth: token is invalid or expired")
	// ErrTokenRevoked is returned when a token is structurally valid but has
	// been invalidated by logout (denylist hit).
	ErrTokenRevoked = errors.New("auth: token has been revoked")
	// ErrTokenAlgorithm is returned when a token is not signed with HS256
	// (blocks the classic alg=none / RS256-confusion attacks).
	ErrTokenAlgorithm = errors.New("auth: unexpected signing algorithm")
	// ErrSecretTooShort is returned when a configured JWT secret is too weak.
	ErrSecretTooShort = errors.New("auth: jwt secret must be at least 32 bytes")
)

// TokenIssuer mints and verifies HS256 access tokens.
//
// It is safe for concurrent use: all fields are immutable after construction.
type TokenIssuer struct {
	secret    []byte
	issuer    string
	audience  string
	accessTTL time.Duration
	now       func() time.Time
}

// TokenIssuerOption customises a TokenIssuer.
type TokenIssuerOption func(*TokenIssuer)

// WithAccessTTL overrides the access token lifetime.
func WithAccessTTL(d time.Duration) TokenIssuerOption {
	return func(t *TokenIssuer) {
		if d > 0 {
			t.accessTTL = d
		}
	}
}

// WithAudience sets the JWT "aud" claim.
func WithAudience(a string) TokenIssuerOption { return func(t *TokenIssuer) { t.audience = a } }

// WithIssuer overrides the JWT "iss" claim.
func WithIssuer(i string) TokenIssuerOption {
	return func(t *TokenIssuer) {
		if i != "" {
			t.issuer = i
		}
	}
}

// WithClock injects a clock. It exists so callers (notably tests, and a
// deployment that needs to mint a token against a specific instant) can control
// time without mutating a shared issuer.
func WithClock(now func() time.Time) TokenIssuerOption { return withClock(now) }

// SetClockForTest replaces the issuer's clock in place.
//
// It is exported so an integration harness can hold the issuer and the server
// on the same clock. Without that, a token's `iat` comes from wall-clock time
// while anything derived from the server's clock (notably the per-user
// revocation floor) uses the injected one, and the two disagree.
func (t *TokenIssuer) SetClockForTest(now func() time.Time) {
	if t == nil || now == nil {
		return
	}
	t.now = now
}

// withClock injects a clock for deterministic tests.
func withClock(now func() time.Time) TokenIssuerOption {
	return func(t *TokenIssuer) {
		if now != nil {
			t.now = now
		}
	}
}

// NewTokenIssuer builds a TokenIssuer from a raw secret.
//
// The secret must be at least MinSecretLength bytes; callers that do not have
// one should use LoadOrCreateSecret, which generates and persists it.
func NewTokenIssuer(secret string, opts ...TokenIssuerOption) (*TokenIssuer, error) {
	if len(secret) < MinSecretLength {
		return nil, fmt.Errorf("%w (got %d bytes)", ErrSecretTooShort, len(secret))
	}
	t := &TokenIssuer{
		secret:    []byte(secret),
		issuer:    DefaultIssuer,
		accessTTL: DefaultAccessTTL,
		now:       time.Now,
	}
	for _, opt := range opts {
		opt(t)
	}
	return t, nil
}

// AccessTTL returns the configured access-token lifetime.
func (t *TokenIssuer) AccessTTL() time.Duration { return t.accessTTL }

// NewTokenIssuerFromIssuer derives a second issuer from an existing one,
// sharing its secret, issuer name and audience but allowing overrides (notably
// a different access TTL for a "remember me" login).
//
// Deriving rather than mutating keeps TokenIssuer immutable and therefore safe
// to share across concurrent requests.
func NewTokenIssuerFromIssuer(base *TokenIssuer, opts ...TokenIssuerOption) (*TokenIssuer, error) {
	if base == nil {
		return nil, errors.New("auth: base issuer is nil")
	}
	derived := &TokenIssuer{
		secret:    base.secret,
		issuer:    base.issuer,
		audience:  base.audience,
		accessTTL: base.accessTTL,
		now:       base.now,
	}
	for _, opt := range opts {
		opt(derived)
	}
	return derived, nil
}

// Issue mints a signed access token for the given identity.
//
// The resulting token carries sub, role, exp, iat, nbf, iss and jti — the
// claims required by §5.7. The jti is what the denylist keys on.
func (t *TokenIssuer) Issue(userID int64, username string, role Role) (token string, claims *Claims, err error) {
	if userID <= 0 {
		return "", nil, errors.New("auth: user id must be positive")
	}
	if !role.Valid() {
		return "", nil, fmt.Errorf("auth: unknown role %q", role)
	}
	jti, err := RandomToken(16)
	if err != nil {
		return "", nil, err
	}
	now := t.now().UTC()
	c := &Claims{
		Role:     role,
		Username: username,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   strconv.FormatInt(userID, 10),
			Issuer:    t.issuer,
			Audience:  audienceList(t.audience),
			ExpiresAt: jwt.NewNumericDate(now.Add(t.accessTTL)),
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now.Add(-30 * time.Second)), // small skew allowance
			ID:        jti,
		},
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, c)
	signed, err := tok.SignedString(t.secret)
	if err != nil {
		return "", nil, fmt.Errorf("auth: signing token: %w", err)
	}
	return signed, c, nil
}

func audienceList(a string) jwt.ClaimStrings {
	if a == "" {
		return nil
	}
	return jwt.ClaimStrings{a}
}

// Parse validates a token string and returns its claims.
//
// Verification is pinned to HS256: the keyfunc rejects any other algorithm
// before the signature is even considered, which defeats alg=none and RSA/HMAC
// confusion attacks.
func (t *TokenIssuer) Parse(tokenString string) (*Claims, error) {
	tokenString = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(tokenString), "Bearer "))
	if tokenString == "" {
		return nil, ErrTokenInvalid
	}

	claims := &Claims{}
	parser := jwt.NewParser(
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithIssuer(t.issuer),
		jwt.WithExpirationRequired(),
		jwt.WithTimeFunc(func() time.Time { return t.now() }),
	)
	if t.audience != "" {
		parser = jwt.NewParser(
			jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
			jwt.WithIssuer(t.issuer),
			jwt.WithAudience(t.audience),
			jwt.WithExpirationRequired(),
			jwt.WithTimeFunc(func() time.Time { return t.now() }),
		)
	}

	_, err := parser.ParseWithClaims(tokenString, claims, func(tok *jwt.Token) (any, error) {
		if tok.Method.Alg() != jwt.SigningMethodHS256.Alg() {
			return nil, fmt.Errorf("%w: %s", ErrTokenAlgorithm, tok.Method.Alg())
		}
		return t.secret, nil
	})
	if err != nil {
		if strings.Contains(err.Error(), ErrTokenAlgorithm.Error()) {
			return nil, fmt.Errorf("%w: %v", ErrTokenAlgorithm, err)
		}
		return nil, fmt.Errorf("%w: %v", ErrTokenInvalid, err)
	}

	if claims.ID == "" || claims.Subject == "" {
		return nil, fmt.Errorf("%w: missing sub or jti", ErrTokenInvalid)
	}
	if !claims.Role.Valid() {
		return nil, fmt.Errorf("%w: unknown role %q", ErrTokenInvalid, claims.Role)
	}
	return claims, nil
}

// UserID parses the numeric subject from the claims.
func (c *Claims) UserID() (int64, error) {
	id, err := strconv.ParseInt(c.Subject, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%w: subject %q is not numeric", ErrTokenInvalid, c.Subject)
	}
	return id, nil
}

// Expiry returns the token expiry, or the zero time if unset.
func (c *Claims) Expiry() time.Time {
	if c.ExpiresAt == nil {
		return time.Time{}
	}
	return c.ExpiresAt.Time
}

// LoadOrCreateSecret reads a JWT signing secret from path, generating and
// persisting a fresh one (mode 0600, parent dir 0700) when the file is missing
// or empty.
//
// This is the "generated-and-persisted on first run" behaviour required by
// §5.7: restarting the panel must not invalidate every existing session, and
// the secret must never be world-readable.
func LoadOrCreateSecret(path string) (secret string, created bool, err error) {
	if path == "" {
		return "", false, errors.New("auth: secret path is required")
	}

	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		s := strings.TrimSpace(string(data))
		if len(s) >= MinSecretLength {
			return s, false, nil
		}
		// Present but weak/truncated: fall through and replace it. A short
		// secret is worse than a rotation.
	case !os.IsNotExist(err):
		return "", false, fmt.Errorf("auth: reading jwt secret %s: %w", path, err)
	}

	dir := filepath.Dir(path)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return "", false, fmt.Errorf("auth: creating secret directory %s: %w", dir, err)
		}
	}

	fresh, err := RandomSecret(48)
	if err != nil {
		return "", false, err
	}
	// Write with restrictive permissions from the start. O_TRUNC + 0600 means
	// there is never a window where the file exists world-readable.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return "", false, fmt.Errorf("auth: writing jwt secret %s: %w", path, err)
	}
	defer f.Close()
	if _, err := f.WriteString(fresh + "\n"); err != nil {
		return "", false, fmt.Errorf("auth: writing jwt secret %s: %w", path, err)
	}
	if err := f.Sync(); err != nil {
		return "", false, fmt.Errorf("auth: syncing jwt secret %s: %w", path, err)
	}
	// Belt and braces: OpenFile's mode is masked by umask, so chmod explicitly.
	if err := os.Chmod(path, 0o600); err != nil {
		return "", false, fmt.Errorf("auth: chmod jwt secret %s: %w", path, err)
	}
	return fresh, true, nil
}
