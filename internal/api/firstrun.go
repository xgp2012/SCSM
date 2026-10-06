package api

import "strings"

// This file centralises the "no password set yet" predicate.
//
// # Why this is not `hash == ""`
//
// internal/store seeds the administrator row with a sentinel rather than an empty
// string:
//
//	const FirstRunPasswordMarker = "!first-run-password-not-set"
//
// The marker is deliberately not a valid bcrypt hash, so a login attempt against
// it can never accidentally succeed — but it is also not empty, so a naive
// `password_hash == ""` check would report "setup not required" on a brand-new
// panel and the operator would be stuck at a login form that can never work.
//
// This package therefore treats three spellings as "needs setup":
//
//  1. the empty string (a store implementation that seeds a truly empty hash),
//  2. internal/store's FirstRunPasswordMarker,
//  3. any value that is not a parseable bcrypt hash — a defensive catch-all so
//     a future marker change, or a hand-edited database, still routes the
//     operator to the setup flow instead of a dead login.
//
// The last rule is what makes this robust against the sibling package changing
// its marker: the panel cannot be locked out by a cosmetic change in another
// package.

// firstRunMarkers are the literal sentinel values recognised as "no password".
// The store's marker is duplicated here rather than imported, because this
// package must compile even while internal/store is mid-edit.
var firstRunMarkers = []string{
	"",
	"!first-run-password-not-set",
	"!", "-", "unset", "changeme", "plaintext-empty",
}

// IsFirstRunHash reports whether hash means "this account has no password yet".
//
// It mirrors store.IsFirstRunHash's contract (empty string or the marker) and
// extends it with a structural check.
func IsFirstRunHash(hash string) bool {
	return isFirstRunHash(hash)
}

func isFirstRunHash(hash string) bool {
	trimmed := strings.TrimSpace(hash)
	for _, m := range firstRunMarkers {
		if trimmed == m {
			return true
		}
	}
	// Anything that is not a bcrypt hash cannot be a password: treat it as
	// "not set" so the first-run flow is reachable.
	return !looksLikeBcryptHash(trimmed)
}

// looksLikeBcryptHash performs a cheap structural check: the $2a$/$2b$/$2y$
// prefix, a two-digit cost, and the 53-character body. It intentionally does not
// call bcrypt.Cost, which would allocate.
func looksLikeBcryptHash(hash string) bool {
	if len(hash) != 60 {
		return false
	}
	if hash[0] != '$' || hash[1] != '2' {
		return false
	}
	if hash[3] != '$' || hash[6] != '$' {
		return false
	}
	switch hash[2] {
	case 'a', 'b', 'x', 'y':
	default:
		return false
	}
	if hash[4] < '0' || hash[4] > '9' || hash[5] < '0' || hash[5] > '9' {
		return false
	}
	for i := 7; i < len(hash); i++ {
		c := hash[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '.' || c == '/':
		default:
			return false
		}
	}
	return true
}
