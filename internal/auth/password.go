// Package auth implements the panel's authentication and authorization
// primitives: HS256 JWT issuing/verification, bcrypt password hashing and the
// three-tier RBAC role model with instance-level grants.
//
// The package is deliberately dependency-light: it never talks to the store or
// the HTTP layer directly, which keeps it trivially testable and lets the API
// layer decide how sessions, denylists and audit logging are wired.
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/crypto/bcrypt"
)

// Bcrypt cost used for all hashes this package produces. The bcrypt default
// (10) is fine for a small self-hosted panel; we raise it slightly but keep it
// well under the point where a single login becomes noticeably slow.
const BcryptCost = 12

// Password policy limits. The panel is a self-hosted admin tool, so the policy
// is intentionally modest: long enough to matter, not so strict that it
// encourages password reuse on sticky notes.
const (
	// MinPasswordLength is the shortest accepted password.
	//
	// Lowered from 8 to 6 so the configured initial administrator password
	// (`default_admin_password: "adfmin"`) is representable. This is a real
	// weakening that applies to EVERY password in the panel, including the
	// in-panel change-password form, not just the seeded one: a 6-character
	// password is now accepted anywhere a password is set.
	//
	// The panel binds 0.0.0.0 by default, so this floor is the only thing
	// standing between a reachable port and a guessable credential. Raise it
	// back to 8 (and config.MinAdminPasswordLength with it — the two are
	// asserted equal by TestDefaultAdminPasswordLengthsMatchAuthPolicy) if the
	// initial password is ever changed to something longer.
	MinPasswordLength = 6
	// MaxPasswordLength caps input length. bcrypt silently truncates at 72
	// bytes, so anything longer is rejected rather than accepted-and-truncated.
	MaxPasswordLength = 72
)

// Password policy errors. Handlers map these to HTTP 422 with a
// machine-readable code.
var (
	// ErrPasswordTooShort is returned when a password is shorter than
	// MinPasswordLength.
	ErrPasswordTooShort = errors.New("auth: password is too short")
	// ErrPasswordTooLong is returned when a password exceeds
	// MaxPasswordLength bytes (bcrypt's hard limit).
	ErrPasswordTooLong = errors.New("auth: password is too long")
	// ErrPasswordTooCommon is returned for trivially guessable passwords.
	ErrPasswordTooCommon = errors.New("auth: password is too weak")
	// ErrPasswordHashInvalid is returned when a stored hash cannot be parsed.
	ErrPasswordHashInvalid = errors.New("auth: stored password hash is not a valid bcrypt hash")
	// ErrPasswordEmpty signals "no password has been set", which is the
	// first-run bootstrap state (users.password_hash = '').
	ErrPasswordEmpty = errors.New("auth: no password set")
)

// commonPasswords is a tiny, deliberately short list of passwords that show up
// at the top of every breach corpus. It is not a general strength meter; it
// just stops the most obvious first-run choices for the admin account.
var commonPasswords = map[string]struct{}{
	"password":      {},
	"password1":     {},
	"passw0rd":      {},
	"12345678":      {},
	"123456789":     {},
	"1234567890":    {},
	"qwertyui":      {},
	"qwerty123":     {},
	"iloveyou":      {},
	"admin":         {},
	"administrator": {},
	"letmein":       {},
	"welcome":       {},
	"scnetm":        {},
	"survivalcraft": {},
}

// HashPassword validates the password against the panel policy and returns a
// bcrypt hash suitable for storage in users.password_hash.
func HashPassword(password string) (string, error) {
	if err := ValidatePassword(password); err != nil {
		return "", err
	}
	h, err := bcrypt.GenerateFromPassword([]byte(password), BcryptCost)
	if err != nil {
		return "", fmt.Errorf("auth: hashing password: %w", err)
	}
	return string(h), nil
}

// ValidatePassword enforces the panel password policy without hashing.
func ValidatePassword(password string) error {
	if utf8.RuneCountInString(password) < MinPasswordLength {
		return fmt.Errorf("%w: at least %d characters required", ErrPasswordTooShort, MinPasswordLength)
	}
	if len(password) > MaxPasswordLength {
		return fmt.Errorf("%w: at most %d bytes allowed (bcrypt limit)", ErrPasswordTooLong, MaxPasswordLength)
	}
	if _, bad := commonPasswords[strings.ToLower(strings.TrimSpace(password))]; bad {
		return fmt.Errorf("%w: password appears in the common-password list", ErrPasswordTooCommon)
	}
	return nil
}

// VerifyPassword compares a plaintext password with a stored bcrypt hash.
//
// An empty hash returns ErrPasswordEmpty, which the API layer translates into
// the distinct "setup_required" first-run flow rather than a generic 401.
func VerifyPassword(hash, password string) error {
	if strings.TrimSpace(hash) == "" {
		return ErrPasswordEmpty
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)); err != nil {
		if errors.Is(err, bcrypt.ErrMismatchedHashAndPassword) {
			return ErrPasswordMismatch
		}
		return fmt.Errorf("%w: %v", ErrPasswordHashInvalid, err)
	}
	return nil
}

// ErrPasswordMismatch is returned when a password does not match the stored
// hash. It is separate from ErrPasswordHashInvalid so callers can distinguish
// "wrong password" from "corrupt database row".
var ErrPasswordMismatch = errors.New("auth: password does not match")

// NeedsRehash reports whether a stored hash was produced with a weaker cost
// than the current BcryptCost, so the caller can transparently upgrade it on a
// successful login.
func NeedsRehash(hash string) bool {
	cost, err := bcrypt.Cost([]byte(hash))
	if err != nil {
		return false
	}
	return cost < BcryptCost
}

// RandomToken returns a URL-safe, cryptographically random token with n bytes
// of entropy. It is used for JWT IDs (jti), session IDs and CSRF-ish values.
func RandomToken(n int) (string, error) {
	if n <= 0 {
		n = 32
	}
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("auth: reading random bytes: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// RandomSecret returns n bytes of random data encoded as standard base64,
// suitable for use as a JWT signing secret.
func RandomSecret(n int) (string, error) {
	if n <= 0 {
		n = 48
	}
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("auth: reading random bytes: %w", err)
	}
	return base64.StdEncoding.EncodeToString(b), nil
}

// ConstantTimeEqual compares two strings without leaking length-independent
// timing information. Used for comparing secrets and CSRF tokens.
func ConstantTimeEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// SanitizeUsername trims surrounding whitespace, lowercases the value and
// rejects control characters. Usernames are compared case-insensitively so the
// store's UNIQUE index cannot be bypassed by "Admin" vs "admin".
func SanitizeUsername(username string) (string, error) {
	u := strings.ToLower(strings.TrimSpace(username))
	if u == "" {
		return "", errors.New("auth: username is required")
	}
	if len(u) > 64 {
		return "", errors.New("auth: username must be at most 64 characters")
	}
	for _, r := range u {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return "", errors.New("auth: username must not contain whitespace or control characters")
		}
		switch r {
		case '/', '\\', ':', '*', '?', '"', '<', '>', '|', '\'', '`', '$', ';', '&':
			return "", fmt.Errorf("auth: username must not contain %q", r)
		}
	}
	return u, nil
}
