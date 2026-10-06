package api

import (
	"context"
	"sync"
	"time"
)

// MemorySessionStore is the in-memory implementation of SessionStore.
//
// # Design decision: denylist, not a session table
//
// The access token is a stateless HS256 JWT, which is what makes the API
// horizontally scalable and lets the WebSocket endpoints authenticate without a
// round trip. The cost is that "logout" cannot mean "forget the token" — the
// token stays cryptographically valid until it expires. §5.7 asks for short
// tokens plus refresh, and for a *server-side* invalidation path.
//
// Of the two options the task offers — a full server-side session table, or a
// denylist — this package implements the **denylist**, keyed on the JWT's jti:
//
//   - It keeps verification stateless and cheap: one map lookup, no session row
//     to load and no session lifetime to reconcile with the token lifetime.
//   - It cannot be "out of sync" the way a session table can: a token is valid
//     exactly when it is signed, unexpired and not denied.
//   - It is bounded: entries carry the token's own expiry and are purged, so
//     the store never grows without limit (a session table has the same
//     property only if you also expire rows).
//
// The tradeoffs, stated plainly so the choice is auditable:
//
//   - Storage is proportional to *logouts*, not to active users. That is the
//     right shape for this workload (a handful of admins).
//   - It is process-local, so a multi-replica deployment would need a shared
//     backend (SQLite table or Redis) implementing the same three methods.
//     The interface is already the seam for that; nothing else changes.
//   - A user's *other* sessions are not killed by one logout unless the caller
//     asks for it explicitly via RevokeAllForUser, which records a
//     per-user "revoked at" floor compared against the token's iat.
//
// The store is safe for concurrent use and safe to share across goroutines.
type MemorySessionStore struct {
	mu        sync.RWMutex
	revoked   map[string]revokedToken
	userFloor map[int64]time.Time
	now       func() time.Time
}

type revokedToken struct {
	userID    int64
	expiresAt time.Time
}

// NewMemorySessionStore builds an empty store.
func NewMemorySessionStore() *MemorySessionStore {
	return &MemorySessionStore{
		revoked:   make(map[string]revokedToken),
		userFloor: make(map[int64]time.Time),
		now:       time.Now,
	}
}

// Compile-time assertion.
var _ SessionStore = (*MemorySessionStore)(nil)

// Revoke adds jti to the denylist until expiresAt. It is idempotent.
func (s *MemorySessionStore) Revoke(_ context.Context, jti string, userID int64, expiresAt time.Time) error {
	if jti == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.revoked[jti] = revokedToken{userID: userID, expiresAt: expiresAt}
	return nil
}

// IsRevoked reports whether jti has been logged out.
//
// Two checks apply: an exact jti hit, and the per-user revocation floor (used
// by RevokeAllForUser, e.g. after a password change). The caller passes only the
// jti, so the floor is checked by the middleware against the claims' iat — see
// Principal/token handling; the exact-match check here is the primary path.
func (s *MemorySessionStore) IsRevoked(_ context.Context, jti string) (bool, error) {
	if jti == "" {
		return false, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	rec, ok := s.revoked[jti]
	if !ok {
		return false, nil
	}
	// An entry past its own expiry is meaningless: the token is dead anyway.
	if !rec.expiresAt.IsZero() && s.now().After(rec.expiresAt) {
		return false, nil
	}
	return true, nil
}

// RevokeAllForUser records a cutoff time; tokens issued before it are refused.
//
// This backs "log out everywhere" and is the right response to a password
// change. It is implemented as a floor rather than by enumerating live jtis
// because the panel never tracks them (that would be the session-table design
// it deliberately avoided).
func (s *MemorySessionStore) RevokeAllForUser(_ context.Context, userID int64, revokedAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if prev, ok := s.userFloor[userID]; !ok || revokedAt.After(prev) {
		s.userFloor[userID] = revokedAt
	}
	return nil
}

// RevokedBefore returns the user's revocation floor, if any. The auth
// middleware uses it to reject tokens issued before a mass logout.
func (s *MemorySessionStore) RevokedBefore(userID int64) (time.Time, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	t, ok := s.userFloor[userID]
	return t, ok
}

// PurgeExpired drops denylist entries whose token has expired, and user floors
// that are older than the longest plausible token lifetime.
func (s *MemorySessionStore) PurgeExpired(_ context.Context, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for jti, rec := range s.revoked {
		if !rec.expiresAt.IsZero() && now.After(rec.expiresAt) {
			delete(s.revoked, jti)
		}
	}
	// A floor only needs to survive as long as a token minted before it could
	// still be alive. 30 days is far beyond any token TTL the panel uses.
	for uid, t := range s.userFloor {
		if now.Sub(t) > 30*24*time.Hour {
			delete(s.userFloor, uid)
		}
	}
	return nil
}

// Len reports the number of denylisted tokens.
func (s *MemorySessionStore) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.revoked)
}

// Clear empties the store. Test helper.
func (s *MemorySessionStore) Clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.revoked = make(map[string]revokedToken)
	s.userFloor = make(map[int64]time.Time)
}

// setClockForTest swaps the clock.
func (s *MemorySessionStore) setClockForTest(now func() time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.now = now
}
