package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// Store owns the *sql.DB and hands out the repositories. It is safe for
// concurrent use; the underlying pool is not (migrate.go pins SetMaxOpenConns(1)
// because SQLite serialises writers anyway and that keeps ":memory:" and WAL
// locking behaviour predictable).
//
// The zero value is not usable: build one with New.
type Store struct {
	db *sql.DB

	// tx is non-nil when this Store is a transactional view created by
	// WithTx. Every repository then routes through the same *sql.Tx, so a
	// handler can compose several repository calls atomically.
	tx *sql.Tx

	users     *UserRepo
	instances *InstanceRepo
	backups   *BackupRepo
	jobs      *JobRepo
	audit     *AuditRepo
	logEvents *LogEventRepo
}

// New wraps db in a Store. It does not run migrations and does not ping; the
// caller (cmd/scnetm) owns that ordering: Open -> Migrate -> New.
func New(db *sql.DB) *Store { return &Store{db: db} }

// DB returns the underlying handle. Inside a WithTx callback this returns the
// same *sql.DB as outside (with the transaction still open), so prefer the
// repository methods plus Tx() when you need to know whether you are in one.
func (s *Store) DB() *sql.DB {
	if s == nil {
		return nil
	}
	return s.db
}

// Tx returns the *sql.Tx when this Store is a transactional view (i.e. inside
// a WithTx callback), or nil otherwise. It lets code that must issue raw SQL
// stay transactional:
//
//	err := st.WithTx(ctx, func(tx *Store) error {
//	    if _, err := tx.Tx().ExecContext(ctx, "..."); err != nil { return err }
//	    return tx.Instances().Create(ctx, &inst)
//	})
func (s *Store) Tx() *sql.Tx {
	if s == nil {
		return nil
	}
	return s.tx
}

// InTx reports whether this Store is a transactional view.
func (s *Store) InTx() bool { return s != nil && s.tx != nil }

// Close closes the database handle. It is a no-op on a transactional view
// (WithTx owns that lifetime).
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	if s.tx != nil {
		// Closing the pool from inside the transaction would deadlock the
		// callback; the WithTx owner is responsible.
		return nil
	}
	return s.db.Close()
}

// Ping verifies the database is reachable.
func (s *Store) Ping(ctx context.Context) error {
	if s == nil || s.db == nil {
		return ErrNoStore
	}
	return s.db.PingContext(ctx)
}

// exec runs a statement through the transaction when there is one, otherwise
// through the pool. Every repository write goes through it.
func (s *Store) exec(ctx context.Context, query string, args ...any) (sql.Result, error) {
	if s == nil {
		return nil, ErrNoStore
	}
	if s.tx != nil {
		return s.tx.ExecContext(ctx, query, args...)
	}
	if s.db == nil {
		return nil, ErrNoStore
	}
	return s.db.ExecContext(ctx, query, args...)
}

// query mirrors exec for reads.
func (s *Store) query(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	if s == nil {
		return nil, ErrNoStore
	}
	if s.tx != nil {
		return s.tx.QueryContext(ctx, query, args...)
	}
	if s.db == nil {
		return nil, ErrNoStore
	}
	return s.db.QueryContext(ctx, query, args...)
}

// queryRow mirrors exec for single-row reads.
func (s *Store) queryRow(ctx context.Context, query string, args ...any) *sql.Row {
	if s == nil || (s.tx == nil && s.db == nil) {
		// Return a Row that fails on Scan with a clear error instead of
		// panicking on a nil receiver.
		return (&sql.DB{}).QueryRowContext(ctx, "SELECT NULL WHERE 0")
	}
	if s.tx != nil {
		return s.tx.QueryRowContext(ctx, query, args...)
	}
	return s.db.QueryRowContext(ctx, query, args...)
}

// prepare compiles a statement against the transaction when there is one,
// otherwise against the pool. The caller owns Close.
func (s *Store) prepare(ctx context.Context, query string) (*sql.Stmt, error) {
	if s == nil {
		return nil, ErrNoStore
	}
	if s.tx != nil {
		return s.tx.PrepareContext(ctx, query)
	}
	if s.db == nil {
		return nil, ErrNoStore
	}
	return s.db.PrepareContext(ctx, query)
}

// WithTx runs fn inside a transaction. The *Store passed to fn routes every
// repository method through the same *sql.Tx, so a multi-step operation (for
// example "delete an instance and cascade its dependent rows") is atomic.
//
// Returning a non-nil error rolls back and the error is returned unchanged, so
// sentinel errors survive: errors.Is(err, ErrLastAdmin) still works after a
// rollback. A panic also rolls back and is re-panicked.
//
// Nesting is rejected with an error rather than silently joining: SQLite has no
// real nested transactions, and a caller who asks for an explicit inner
// transaction usually expects an independent commit, which cannot be provided.
// Repository methods that merely need "be atomic on my own, but join the
// caller's transaction if there is one" use withTx below.
func (s *Store) WithTx(ctx context.Context, fn func(*Store) error) error {
	if s == nil || s.db == nil {
		return ErrNoStore
	}
	if s.tx != nil {
		return errors.New("store: WithTx cannot be nested")
	}
	if fn == nil {
		return invalidf("WithTx: nil callback")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin transaction: %w", err)
	}

	view := &Store{db: s.db, tx: tx}

	committed := false
	defer func() {
		if !committed {
			// Roll back on panic; a failed rollback is not actionable.
			_ = tx.Rollback()
		}
	}()

	if err := fn(view); err != nil {
		_ = tx.Rollback()
		committed = true // nothing left to roll back
		return err
	}

	if err := tx.Commit(); err != nil {
		_ = tx.Rollback()
		committed = true
		return fmt.Errorf("store: commit transaction: %w", err)
	}
	committed = true
	return nil
}

// withTx runs fn atomically, joining an already-open transaction when there is
// one. It is the helper repository methods use for their own multi-statement
// invariants (the last-admin guard, the instance cascade), so that:
//
//   - called on a plain Store they run in a transaction of their own, and
//   - called on a transactional view inside WithTx they become part of the
//     caller's transaction instead of failing as a nested one.
//
// The callback always receives a Store that routes SQL the same way the
// receiver does.
func (s *Store) withTx(ctx context.Context, fn func(*Store) error) error {
	if s.tx != nil {
		return fn(s)
	}
	return s.WithTx(ctx, fn)
}

// Repo accessors. The small repositories are cheap value holders; the
// accessors cache them so repeated calls inside a request do not allocate.

// Users returns the user repository.
func (s *Store) Users() *UserRepo {
	if s.users == nil {
		s.users = &UserRepo{s: s}
	}
	return s.users
}

// Instances returns the instance repository (instances + instance_state).
func (s *Store) Instances() *InstanceRepo {
	if s.instances == nil {
		s.instances = &InstanceRepo{s: s}
	}
	return s.instances
}

// Backups returns the backup repository.
func (s *Store) Backups() *BackupRepo {
	if s.backups == nil {
		s.backups = &BackupRepo{s: s}
	}
	return s.backups
}

// Jobs returns the scheduled-job repository.
func (s *Store) Jobs() *JobRepo {
	if s.jobs == nil {
		s.jobs = &JobRepo{s: s}
	}
	return s.jobs
}

// Audit returns the audit-log repository.
func (s *Store) Audit() *AuditRepo {
	if s.audit == nil {
		s.audit = &AuditRepo{s: s}
	}
	return s.audit
}

// LogEvents returns the log-event index repository.
func (s *Store) LogEvents() *LogEventRepo {
	if s.logEvents == nil {
		s.logEvents = &LogEventRepo{s: s}
	}
	return s.logEvents
}

// IsUniqueViolation reports whether err is a SQLite UNIQUE/PRIMARY KEY
// constraint failure. It is exported so handlers that issue their own SQL can
// map the violation to ErrDuplicate the same way the repositories do.
func IsUniqueViolation(err error) bool { return isUniqueViolation(err) }

// IsForeignKeyViolation reports whether err is a SQLite FOREIGN KEY failure.
func IsForeignKeyViolation(err error) bool { return isForeignKeyViolation(err) }

// isUniqueViolation inspects the driver error text. modernc.org/sqlite exposes
// the extended result code through a typed error, but that type is not part of
// the stable public API across versions, so the message is matched instead —
// the same approach the standard library's own SQLite users take.
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToUpper(err.Error())
	return strings.Contains(msg, "UNIQUE CONSTRAINT FAILED") ||
		strings.Contains(msg, "PRIMARY KEY") ||
		strings.Contains(msg, "CONSTRAINT FAILED: UNIQUE")
}

func isForeignKeyViolation(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToUpper(err.Error()), "FOREIGN KEY CONSTRAINT FAILED")
}

// mapConstraint converts a driver constraint error into the typed sentinel the
// API layer understands, leaving anything else untouched.
func mapConstraint(err error, duplicate error) error {
	if err == nil {
		return nil
	}
	if isUniqueViolation(err) {
		if duplicate != nil {
			return duplicate
		}
		return fmt.Errorf("%w: %v", ErrDuplicate, err)
	}
	if isForeignKeyViolation(err) {
		return fmt.Errorf("%w: %v", ErrConflict, err)
	}
	return err
}
