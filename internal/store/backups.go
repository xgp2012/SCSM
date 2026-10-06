package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// BackupRepo stores the backup index (§5.5 backups, §6.5 retention).
//
// The repository only tracks metadata: the archive itself lives on disk at
// Backup.Path and is owned by internal/world. Deleting a row here does not
// touch the file, which is why PruneOldest returns the removed rows: the caller
// needs their Paths to unlink.
type BackupRepo struct {
	s *Store
}

const backupColumns = `id, instance_id, world, path, size_bytes, sha256, kind, created_at, note`

// Backup kinds accepted by the repository, matching §6.5's three tiers.
var backupKinds = []string{BackupManual, BackupScheduled, BackupPreStart}

// ValidBackupKind reports whether kind is one of manual/scheduled/pre-start.
func ValidBackupKind(kind string) bool {
	for _, k := range backupKinds {
		if k == kind {
			return true
		}
	}
	return false
}

// Create records a completed backup and fills in its ID (and CreatedAt when
// unset).
func (r *BackupRepo) Create(ctx context.Context, b *Backup) error {
	if b == nil {
		return invalidf("backups: nil backup")
	}
	if b.InstanceID == 0 {
		return invalidf("backups: instance id must not be zero")
	}
	if b.Path == "" {
		return invalidf("backups: path must not be empty")
	}
	if b.Kind == "" {
		b.Kind = BackupManual
	}
	if !ValidBackupKind(b.Kind) {
		return invalidf("backups: unknown kind %q (want manual|scheduled|pre-start)", b.Kind)
	}
	if b.CreatedAt.IsZero() {
		b.CreatedAt = time.Now().UTC()
	}

	res, err := r.s.exec(ctx,
		`INSERT INTO backups (instance_id, world, path, size_bytes, sha256, kind, created_at, note)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		b.InstanceID, b.World, b.Path, b.SizeBytes, b.SHA256, b.Kind, toDB(b.CreatedAt), b.Note,
	)
	if err != nil {
		return mapConstraint(err, nil)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return fmt.Errorf("backups: last insert id: %w", err)
	}
	b.ID = id
	b.CreatedAt = b.CreatedAt.UTC().Truncate(time.Second)
	return nil
}

// Get returns one backup by id, or ErrNotFound.
func (r *BackupRepo) Get(ctx context.Context, id int64) (*Backup, error) {
	b, err := scanBackup(r.s.queryRow(ctx, `SELECT `+backupColumns+` FROM backups WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, notFoundf("backups: id %d", id)
	}
	if err != nil {
		return nil, fmt.Errorf("backups: get %d: %w", id, err)
	}
	return b, nil
}

// GetByPath returns the backup recorded at path, or ErrNotFound. It backs the
// sha256-based de-duplication in §6.5: an identical archive already on disk is
// reused instead of copied again.
func (r *BackupRepo) GetByPath(ctx context.Context, path string) (*Backup, error) {
	b, err := scanBackup(r.s.queryRow(ctx, `SELECT `+backupColumns+` FROM backups WHERE path = ?`, path))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, notFoundf("backups: path %q", path)
	}
	if err != nil {
		return nil, fmt.Errorf("backups: get by path %q: %w", path, err)
	}
	return b, nil
}

// GetBySHA256 returns the newest backup with the given content hash, or
// ErrNotFound. Used for the "same archive, do not copy twice" path.
func (r *BackupRepo) GetBySHA256(ctx context.Context, instanceID int64, sha string) (*Backup, error) {
	if sha == "" {
		return nil, invalidf("backups: empty sha256")
	}
	b, err := scanBackup(r.s.queryRow(ctx,
		`SELECT `+backupColumns+` FROM backups
		 WHERE instance_id = ? AND sha256 = ? ORDER BY created_at DESC, id DESC LIMIT 1`,
		instanceID, sha))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, notFoundf("backups: sha256 %q for instance %d", sha, instanceID)
	}
	if err != nil {
		return nil, fmt.Errorf("backups: get by sha256: %w", err)
	}
	return b, nil
}

// ListByInstance returns the backups of one instance, newest first.
func (r *BackupRepo) ListByInstance(ctx context.Context, instanceID int64) ([]Backup, error) {
	return r.list(ctx,
		`SELECT `+backupColumns+` FROM backups WHERE instance_id = ?
		 ORDER BY created_at DESC, id DESC`, instanceID)
}

// ListByInstanceKind returns the backups of one instance restricted to a single
// kind, newest first. It is the ordering PruneOldest relies on.
func (r *BackupRepo) ListByInstanceKind(ctx context.Context, instanceID int64, kind string) ([]Backup, error) {
	if !ValidBackupKind(kind) {
		return nil, invalidf("backups: unknown kind %q", kind)
	}
	return r.list(ctx,
		`SELECT `+backupColumns+` FROM backups WHERE instance_id = ? AND kind = ?
		 ORDER BY created_at DESC, id DESC`, instanceID, kind)
}

// ListOldestFirst returns every backup of an instance, oldest first.
func (r *BackupRepo) ListOldestFirst(ctx context.Context, instanceID int64) ([]Backup, error) {
	return r.list(ctx,
		`SELECT `+backupColumns+` FROM backups WHERE instance_id = ?
		 ORDER BY created_at ASC, id ASC`, instanceID)
}

func (r *BackupRepo) list(ctx context.Context, query string, args ...any) ([]Backup, error) {
	rows, err := r.s.query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("backups: list: %w", err)
	}
	defer rows.Close()

	var out []Backup
	for rows.Next() {
		b, err := scanBackup(rows)
		if err != nil {
			return nil, fmt.Errorf("backups: list scan: %w", err)
		}
		out = append(out, *b)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("backups: list: %w", err)
	}
	return out, nil
}

// Delete removes one backup row. The on-disk archive is the caller's business.
func (r *BackupRepo) Delete(ctx context.Context, id int64) error {
	res, err := r.s.exec(ctx, `DELETE FROM backups WHERE id = ?`, id)
	if err != nil {
		return mapConstraint(err, nil)
	}
	return affectedOrNotFound(res, "backups", id)
}

// TotalSize reports the summed SizeBytes of an instance's backups, which the UI
// shows next to the disk-usage warning in §6.5.
func (r *BackupRepo) TotalSize(ctx context.Context, instanceID int64) (int64, error) {
	var total sql.NullInt64
	err := r.s.queryRow(ctx,
		`SELECT SUM(size_bytes) FROM backups WHERE instance_id = ?`, instanceID).Scan(&total)
	if err != nil {
		return 0, fmt.Errorf("backups: total size for instance %d: %w", instanceID, err)
	}
	if !total.Valid {
		return 0, nil // no rows: SUM is NULL
	}
	return total.Int64, nil
}

// CountByKind reports how many backups of a kind an instance has.
func (r *BackupRepo) CountByKind(ctx context.Context, instanceID int64, kind string) (int64, error) {
	if !ValidBackupKind(kind) {
		return 0, invalidf("backups: unknown kind %q", kind)
	}
	var n int64
	err := r.s.queryRow(ctx,
		`SELECT COUNT(1) FROM backups WHERE instance_id = ? AND kind = ?`, instanceID, kind).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("backups: count kind %q: %w", kind, err)
	}
	return n, nil
}

// PruneOldest enforces the §6.5 retention rule: keep the keep newest backups of
// one kind for one instance and delete the rest, returning the deleted rows so
// the caller can unlink their files.
//
// Ordering is by created_at then id, both descending, so "newest" is total and
// deterministic even when several backups share a timestamp (which happens
// whenever a test or a fast scheduler creates them within the same second —
// RFC3339 text has one-second resolution).
//
// keep <= 0 deletes every backup of that kind. The SELECT and the DELETE run in
// one transaction so a concurrent backup cannot be deleted by a stale decision.
//
// The returned slice is oldest-first, which is the order a caller should unlink
// files in and the order a UI wants to report ("removed the 3 oldest").
func (r *BackupRepo) PruneOldest(ctx context.Context, instanceID int64, kind string, keep int) ([]Backup, error) {
	if !ValidBackupKind(kind) {
		return nil, invalidf("backups: unknown kind %q (want manual|scheduled|pre-start)", kind)
	}
	if keep < 0 {
		return nil, invalidf("backups: keep must not be negative, got %d", keep)
	}

	var deleted []Backup
	err := r.s.withTx(ctx, func(tx *Store) error {
		repo := tx.Backups()

		all, err := repo.ListByInstanceKind(ctx, instanceID, kind)
		if err != nil {
			return err
		}
		if len(all) <= keep {
			return nil // nothing to prune
		}

		// all is newest-first, so everything from index keep onwards is
		// outside the retained window. Walk it backwards so the report reads
		// oldest-first.
		victims := make([]Backup, 0, len(all)-keep)
		for i := len(all) - 1; i >= keep; i-- {
			victims = append(victims, all[i])
		}

		deleted = make([]Backup, 0, len(victims))
		for _, b := range victims {
			if err := repo.Delete(ctx, b.ID); err != nil {
				return err
			}
			deleted = append(deleted, b)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return deleted, nil
}

// PruneBefore deletes every backup of an instance older than before, returning
// the removed rows. §6.5's "keep N days" alternative to a count-based rule.
func (r *BackupRepo) PruneBefore(ctx context.Context, instanceID int64, before time.Time) ([]Backup, error) {
	var deleted []Backup
	err := r.s.withTx(ctx, func(tx *Store) error {
		repo := tx.Backups()

		rows, err := repo.list(ctx,
			`SELECT `+backupColumns+` FROM backups
			 WHERE instance_id = ? AND created_at < ?
			 ORDER BY created_at ASC, id ASC`, instanceID, toDB(before))
		if err != nil {
			return err
		}
		deleted = rows
		for _, b := range rows {
			if err := repo.Delete(ctx, b.ID); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return deleted, nil
}

// DeleteByInstance removes every backup row of an instance. It is part of the
// instance cascade and returns the removed rows so their files can be cleaned.
func (r *BackupRepo) DeleteByInstance(ctx context.Context, instanceID int64) ([]Backup, error) {
	var deleted []Backup
	err := r.s.withTx(ctx, func(tx *Store) error {
		repo := tx.Backups()
		rows, err := repo.ListByInstance(ctx, instanceID)
		if err != nil {
			return err
		}
		deleted = rows
		_, err = tx.exec(ctx, `DELETE FROM backups WHERE instance_id = ?`, instanceID)
		if err != nil {
			return fmt.Errorf("backups: delete all for instance %d: %w", instanceID, err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return deleted, nil
}

func scanBackup(sc scanner) (*Backup, error) {
	var (
		b         Backup
		createdAt ScanTime
		sizeBytes sql.NullInt64
	)
	if err := sc.Scan(&b.ID, &b.InstanceID, &b.World, &b.Path, &sizeBytes, &b.SHA256,
		&b.Kind, &createdAt, &b.Note); err != nil {
		return nil, err
	}
	if sizeBytes.Valid {
		b.SizeBytes = sizeBytes.Int64
	}
	b.CreatedAt = createdAt.Time
	return &b, nil
}
