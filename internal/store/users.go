package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// UserRepo reads and writes users and user_instance_grant.
//
// The grant half is dead weight in single-user mode (the table stays empty and
// the owner implicitly holds every permission) but it is implemented and tested
// now on purpose: decision D3 is that enabling multi-user must be a
// configuration change, not a schema migration plus a rewrite of this layer.
type UserRepo struct {
	s *Store
}

// userColumns is the canonical SELECT list, so every query that scans a User
// stays column-for-column compatible with scanUser.
const userColumns = `id, username, password_hash, role, created_at, last_login_at, disabled`

// Create inserts u and fills in its ID (and CreatedAt when unset).
//
// A duplicate username maps to ErrDuplicate.
func (r *UserRepo) Create(ctx context.Context, u *User) error {
	if u == nil {
		return invalidf("users: nil user")
	}
	username := strings.TrimSpace(u.Username)
	if username == "" {
		return invalidf("users: username must not be empty")
	}
	if u.Role == "" {
		u.Role = RoleViewer
	}
	if !ValidRole(u.Role) {
		return invalidf("users: unknown role %q", u.Role)
	}
	if u.CreatedAt.IsZero() {
		u.CreatedAt = time.Now().UTC()
	}
	u.Username = username

	res, err := r.s.exec(ctx,
		`INSERT INTO users (username, password_hash, role, created_at, last_login_at, disabled)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		u.Username, u.PasswordHash, u.Role, toDB(u.CreatedAt), toDBPtr(u.LastLoginAt), boolToInt(u.Disabled),
	)
	if err != nil {
		return mapConstraint(err, duplicatef("users: username %q already exists", u.Username))
	}
	id, err := res.LastInsertId()
	if err != nil {
		return fmt.Errorf("users: last insert id: %w", err)
	}
	u.ID = id
	u.CreatedAt = u.CreatedAt.UTC().Truncate(time.Second)
	return nil
}

// GetByID returns the user with the given id, or ErrNotFound.
func (r *UserRepo) GetByID(ctx context.Context, id int64) (*User, error) {
	row := r.s.queryRow(ctx, `SELECT `+userColumns+` FROM users WHERE id = ?`, id)
	u, err := scanUser(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, notFoundf("users: id %d", id)
	}
	if err != nil {
		return nil, fmt.Errorf("users: get %d: %w", id, err)
	}
	return u, nil
}

// GetByUsername returns the user with the given username, or ErrNotFound.
// Usernames are compared case-insensitively, matching how operators expect
// "Admin" to find the seeded "admin" row.
func (r *UserRepo) GetByUsername(ctx context.Context, username string) (*User, error) {
	row := r.s.queryRow(ctx,
		`SELECT `+userColumns+` FROM users WHERE username = ? COLLATE NOCASE`,
		strings.TrimSpace(username))
	u, err := scanUser(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, notFoundf("users: username %q", username)
	}
	if err != nil {
		return nil, fmt.Errorf("users: get %q: %w", username, err)
	}
	return u, nil
}

// List returns every user, oldest first.
func (r *UserRepo) List(ctx context.Context) ([]User, error) {
	rows, err := r.s.query(ctx, `SELECT `+userColumns+` FROM users ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("users: list: %w", err)
	}
	defer rows.Close()

	var out []User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, fmt.Errorf("users: list scan: %w", err)
		}
		out = append(out, *u)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("users: list: %w", err)
	}
	return out, nil
}

// UpdatePassword replaces the stored password hash. It is also the first-run
// path: the seeded admin has an unusable hash until this is called.
func (r *UserRepo) UpdatePassword(ctx context.Context, id int64, passwordHash string) error {
	if passwordHash == "" {
		return invalidf("users: password hash must not be empty")
	}
	res, err := r.s.exec(ctx, `UPDATE users SET password_hash = ? WHERE id = ?`, passwordHash, id)
	if err != nil {
		return fmt.Errorf("users: update password %d: %w", id, err)
	}
	return affectedOrNotFound(res, "users", id)
}

// SetRole changes a user's role.
//
// Demoting the last enabled admin is refused with ErrLastAdmin: the panel must
// never reach a state where nobody can administer it (decision D3).
func (r *UserRepo) SetRole(ctx context.Context, id int64, role string) error {
	if !ValidRole(role) {
		return invalidf("users: unknown role %q", role)
	}

	return r.s.withTx(ctx, func(tx *Store) error {
		u, err := tx.Users().GetByID(ctx, id)
		if err != nil {
			return err
		}
		if u.Role == role {
			return nil // no-op, and never a last-admin failure
		}
		if u.Role == RoleAdmin && role != RoleAdmin {
			if err := r.guardLastAdmin(ctx, tx, id); err != nil {
				return err
			}
		}
		res, err := tx.exec(ctx, `UPDATE users SET role = ? WHERE id = ?`, role, id)
		if err != nil {
			return mapConstraint(err, nil)
		}
		return affectedOrNotFound(res, "users", id)
	})
}

// SetDisabled enables or disables an account.
//
// Disabling the last enabled admin is refused with ErrLastAdmin.
func (r *UserRepo) SetDisabled(ctx context.Context, id int64, disabled bool) error {
	return r.s.withTx(ctx, func(tx *Store) error {
		u, err := tx.Users().GetByID(ctx, id)
		if err != nil {
			return err
		}
		if u.Disabled == disabled {
			return nil // no-op
		}
		if disabled {
			if err := r.guardLastAdmin(ctx, tx, id); err != nil {
				return err
			}
		}
		res, err := tx.exec(ctx, `UPDATE users SET disabled = ? WHERE id = ?`, boolToInt(disabled), id)
		if err != nil {
			return mapConstraint(err, nil)
		}
		return affectedOrNotFound(res, "users", id)
	})
}

// TouchLastLogin records a successful authentication.
func (r *UserRepo) TouchLastLogin(ctx context.Context, id int64, at time.Time) error {
	if at.IsZero() {
		at = time.Now().UTC()
	}
	res, err := r.s.exec(ctx, `UPDATE users SET last_login_at = ? WHERE id = ?`, toDB(at), id)
	if err != nil {
		return fmt.Errorf("users: touch last login %d: %w", id, err)
	}
	return affectedOrNotFound(res, "users", id)
}

// CountAdmins returns how many enabled admins exist. Callers use it to decide
// whether a destructive user operation is still safe; the repository enforces
// the same rule itself with ErrLastAdmin.
func (r *UserRepo) CountAdmins(ctx context.Context) (int64, error) {
	var n int64
	err := r.s.queryRow(ctx,
		`SELECT COUNT(1) FROM users WHERE role = ? AND disabled = 0`, RoleAdmin).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("users: count admins: %w", err)
	}
	return n, nil
}

// Delete removes a user and, by default, their instance grants.
//
// Deleting the last enabled admin returns ErrLastAdmin. The guard and the
// delete share one transaction so a concurrent demotion cannot slip between
// the check and the write.
func (r *UserRepo) Delete(ctx context.Context, id int64) error {
	return r.s.withTx(ctx, func(tx *Store) error {
		u, err := tx.Users().GetByID(ctx, id)
		if err != nil {
			return err
		}
		if u.Role == RoleAdmin && !u.Disabled {
			if err := r.guardLastAdmin(ctx, tx, id); err != nil {
				return err
			}
		}
		// Referential action in Go, not in DDL: the grant table has a
		// composite primary key and no foreign key, so orphan rows would
		// otherwise accumulate silently.
		if _, err := tx.exec(ctx, `DELETE FROM user_instance_grant WHERE user_id = ?`, id); err != nil {
			return fmt.Errorf("users: delete grants for %d: %w", id, err)
		}
		res, err := tx.exec(ctx, `DELETE FROM users WHERE id = ?`, id)
		if err != nil {
			return mapConstraint(err, nil)
		}
		return affectedOrNotFound(res, "users", id)
	})
}

// guardLastAdmin returns ErrLastAdmin when removing (deleting, disabling or
// demoting) the user would leave no enabled admin behind.
//
// It counts inside the caller's transaction, and excludes candidateID because
// that row is the one about to change.
func (r *UserRepo) guardLastAdmin(ctx context.Context, tx *Store, candidateID int64) error {
	var remaining int64
	err := tx.queryRow(ctx,
		`SELECT COUNT(1) FROM users WHERE role = ? AND disabled = 0 AND id <> ?`,
		RoleAdmin, candidateID).Scan(&remaining)
	if err != nil {
		return fmt.Errorf("users: count remaining admins: %w", err)
	}
	if remaining == 0 {
		return fmt.Errorf("%w (user id %d)", ErrLastAdmin, candidateID)
	}
	return nil
}

// --- instance-level grants (D3 extension slot) ---------------------------

// Grant gives userID the permission perm on instanceID. Re-granting an
// existing combination is idempotent rather than an error, which makes the
// operation safe to retry.
func (r *UserRepo) Grant(ctx context.Context, userID, instanceID int64, perm string) error {
	if !ValidGrantPerm(perm) {
		return invalidf("grants: unknown perm %q (want read|control|config)", perm)
	}
	_, err := r.s.exec(ctx,
		`INSERT INTO user_instance_grant (user_id, instance_id, perm) VALUES (?, ?, ?)
		 ON CONFLICT (user_id, instance_id, perm) DO NOTHING`,
		userID, instanceID, perm)
	if err != nil {
		return mapConstraint(err, duplicatef("grants: (%d, %d, %s) already exists", userID, instanceID, perm))
	}
	return nil
}

// Revoke removes a single grant. Revoking something that was never granted
// returns ErrNotFound, so a UI action that silently did nothing is detectable.
func (r *UserRepo) Revoke(ctx context.Context, userID, instanceID int64, perm string) error {
	res, err := r.s.exec(ctx,
		`DELETE FROM user_instance_grant WHERE user_id = ? AND instance_id = ? AND perm = ?`,
		userID, instanceID, perm)
	if err != nil {
		return fmt.Errorf("grants: revoke: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("grants: revoke rows affected: %w", err)
	}
	if n == 0 {
		return notFoundf("grants: (%d, %d, %s)", userID, instanceID, perm)
	}
	return nil
}

// RevokeAllForInstance drops every grant pointing at an instance. The instance
// delete cascade calls it, but it is exported because a "delete instance"
// handler may want it explicitly.
func (r *UserRepo) RevokeAllForInstance(ctx context.Context, instanceID int64) (int64, error) {
	res, err := r.s.exec(ctx, `DELETE FROM user_instance_grant WHERE instance_id = ?`, instanceID)
	if err != nil {
		return 0, fmt.Errorf("grants: revoke all for instance %d: %w", instanceID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("grants: rows affected: %w", err)
	}
	return n, nil
}

// ListForUser returns every grant held by userID, ordered for stable display.
func (r *UserRepo) ListForUser(ctx context.Context, userID int64) ([]Grant, error) {
	rows, err := r.s.query(ctx,
		`SELECT user_id, instance_id, perm FROM user_instance_grant
		 WHERE user_id = ? ORDER BY instance_id, perm`, userID)
	if err != nil {
		return nil, fmt.Errorf("grants: list for user %d: %w", userID, err)
	}
	defer rows.Close()

	var out []Grant
	for rows.Next() {
		var g Grant
		if err := rows.Scan(&g.UserID, &g.InstanceID, &g.Perm); err != nil {
			return nil, fmt.Errorf("grants: scan: %w", err)
		}
		out = append(out, g)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("grants: list: %w", err)
	}
	return out, nil
}

// ListForInstance returns every grant attached to instanceID.
func (r *UserRepo) ListForInstance(ctx context.Context, instanceID int64) ([]Grant, error) {
	rows, err := r.s.query(ctx,
		`SELECT user_id, instance_id, perm FROM user_instance_grant
		 WHERE instance_id = ? ORDER BY user_id, perm`, instanceID)
	if err != nil {
		return nil, fmt.Errorf("grants: list for instance %d: %w", instanceID, err)
	}
	defer rows.Close()

	var out []Grant
	for rows.Next() {
		var g Grant
		if err := rows.Scan(&g.UserID, &g.InstanceID, &g.Perm); err != nil {
			return nil, fmt.Errorf("grants: scan: %w", err)
		}
		out = append(out, g)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("grants: list: %w", err)
	}
	return out, nil
}

// HasPermission reports whether userID holds perm on instanceID.
//
// An admin, or the owner of the instance, implicitly holds every permission:
// that is what keeps single-user mode working with an empty grant table. A
// disabled account holds nothing.
func (r *UserRepo) HasPermission(ctx context.Context, userID, instanceID int64, perm string) (bool, error) {
	if !ValidGrantPerm(perm) {
		return false, invalidf("grants: unknown perm %q", perm)
	}

	u, err := r.GetByID(ctx, userID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return false, nil
		}
		return false, err
	}
	if u.Disabled {
		return false, nil
	}
	if u.Role == RoleAdmin {
		return true, nil
	}

	// Instance ownership (D3's owner_id) is an implicit grant to operators.
	var owner int64
	err = r.s.queryRow(ctx, `SELECT COALESCE(owner_id, 0) FROM instances WHERE id = ?`, instanceID).Scan(&owner)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, fmt.Errorf("grants: read owner of instance %d: %w", instanceID, err)
	}
	if err == nil && owner == userID {
		return true, nil
	}

	var n int
	err = r.s.queryRow(ctx,
		`SELECT COUNT(1) FROM user_instance_grant
		 WHERE user_id = ? AND instance_id = ? AND perm = ?`,
		userID, instanceID, perm).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("grants: has permission: %w", err)
	}
	return n > 0, nil
}

// --- scanning helpers ----------------------------------------------------

// scanner is satisfied by both *sql.Row and *sql.Rows.
type scanner interface{ Scan(dest ...any) error }

// scanUser reads one row of userColumns into a User.
func scanUser(sc scanner) (*User, error) {
	var (
		u         User
		createdAt ScanTime
		lastLogin ScanTime
		disabled  int64
	)
	if err := sc.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.Role,
		&createdAt, &lastLogin, &disabled); err != nil {
		return nil, err
	}
	u.CreatedAt = createdAt.Time
	u.LastLoginAt = TimePtr(lastLogin.Time)
	u.Disabled = disabled != 0
	return &u, nil
}

// affectedOrNotFound turns a zero-row RowsAffected into ErrNotFound, so that
// "update a row that is not there" is reported consistently with Get.
func affectedOrNotFound(res sql.Result, entity string, id int64) error {
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: rows affected: %w", err)
	}
	if n == 0 {
		return notFoundf("%s: id %d", entity, id)
	}
	return nil
}

// boolToInt renders a Go bool as the INTEGER 0/1 the schema stores.
func boolToInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}
