// Package store is the SQLite persistence layer of scnetm: the schema
// migration (migrate.go), the row structs (models.go) and one explicit-SQL
// repository per table group.
//
// Design rules (see plan §4.1, §5.5, §7):
//
//   - Only database/sql plus modernc.org/sqlite (pure Go, no CGO). No ORM:
//     every statement is written out so it is greppable and testable.
//   - Every repository is reached through *Store and every method takes a
//     context.Context first, using the *Context query/exec variants.
//   - Timestamps are stored as RFC3339 UTC text and converted in this layer;
//     see time.go for the full rationale and the driver quirk.
//   - Referential actions are enforced in Go (in transactions), not left to
//     SQLite's ON DELETE defaults.
package store

import (
	"errors"
	"fmt"
)

// Sentinel errors returned by the repositories. Callers are expected to match
// them with errors.Is, which keeps the API layer free of driver-specific
// error strings:
//
//	inst, err := st.Instances().GetByName(ctx, name)
//	switch {
//	case errors.Is(err, store.ErrNotFound):   // -> 404
//	case errors.Is(err, store.ErrDuplicate):  // -> 409
//	}
var (
	// ErrNotFound reports that the requested row does not exist.
	ErrNotFound = errors.New("store: not found")

	// ErrDuplicate reports a UNIQUE constraint violation (instances.name,
	// users.username, user_instance_grant primary key).
	ErrDuplicate = errors.New("store: duplicate")

	// ErrConflict reports a write rejected because it would break an
	// invariant that is not a plain UNIQUE violation. ErrLastAdmin is the
	// canonical example; callers may match either.
	ErrConflict = errors.New("store: conflict")

	// ErrLastAdmin reports an attempt to delete, disable or demote the last
	// enabled admin. This is what protects single-user mode (decision D3):
	// the panel must never be left with nobody able to log in and repair it.
	ErrLastAdmin = errors.New("store: last enabled admin cannot be removed or demoted")

	// ErrInvalid reports a caller-supplied value that fails validation
	// before it ever reaches SQLite.
	ErrInvalid = errors.New("store: invalid argument")
)

// ErrNoStore reports use of a repository whose *Store is nil (a programming
// error rather than a user-facing failure).
var ErrNoStore = errors.New("store: nil store")

// notFoundf builds an ErrNotFound that still names the entity, so logs and
// API errors stay readable while errors.Is keeps working.
func notFoundf(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrNotFound, fmt.Sprintf(format, args...))
}

// duplicatef builds an ErrDuplicate naming the conflicting key.
func duplicatef(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrDuplicate, fmt.Sprintf(format, args...))
}

// conflictf builds an ErrConflict.
func conflictf(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrConflict, fmt.Sprintf(format, args...))
}

// invalidf builds an ErrInvalid.
func invalidf(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, args...))
}

// wrapLastAdmin returns err annotated with ErrLastAdmin when err is non-nil.
func wrapLastAdmin(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%w: %w", ErrLastAdmin, err)
}
