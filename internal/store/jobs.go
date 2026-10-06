package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// JobRepo stores the cron entries the scheduler loads at boot and after every
// mutation (§6.7).
type JobRepo struct {
	s *Store
}

const jobColumns = `id, instance_id, type, cron, payload, enabled, last_run, last_result`

// jobTypes is the closed set from §5.5's comment.
var jobTypes = []string{JobBackup, JobRestart, JobCommand}

// ValidJobType reports whether typ is one of backup/restart/command.
func ValidJobType(typ string) bool {
	for _, t := range jobTypes {
		if t == typ {
			return true
		}
	}
	return false
}

// Create inserts a job and fills in its ID.
//
// Enabled defaults to true: a job the operator just created is almost always
// one they want to run, and the alternative (silently inert) is a support
// burden.
func (r *JobRepo) Create(ctx context.Context, j *Job) error {
	if j == nil {
		return invalidf("jobs: nil job")
	}
	if !ValidJobType(j.Type) {
		return invalidf("jobs: unknown type %q (want backup|restart|command)", j.Type)
	}
	if j.Cron == "" {
		return invalidf("jobs: cron expression must not be empty")
	}
	if j.InstanceID == 0 {
		return invalidf("jobs: instance id must not be zero")
	}
	if j.Payload == "" {
		j.Payload = "{}"
	}

	res, err := r.s.exec(ctx,
		`INSERT INTO jobs (instance_id, type, cron, payload, enabled, last_run, last_result)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		j.InstanceID, j.Type, j.Cron, j.Payload, boolToInt(j.Enabled), toDBPtr(j.LastRun), j.LastResult,
	)
	if err != nil {
		return mapConstraint(err, nil)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return fmt.Errorf("jobs: last insert id: %w", err)
	}
	j.ID = id
	return nil
}

// Get returns one job by id, or ErrNotFound.
func (r *JobRepo) Get(ctx context.Context, id int64) (*Job, error) {
	j, err := scanJob(r.s.queryRow(ctx, `SELECT `+jobColumns+` FROM jobs WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, notFoundf("jobs: id %d", id)
	}
	if err != nil {
		return nil, fmt.Errorf("jobs: get %d: %w", id, err)
	}
	return j, nil
}

// List returns every job, oldest first.
func (r *JobRepo) List(ctx context.Context) ([]Job, error) {
	return r.list(ctx, `SELECT `+jobColumns+` FROM jobs ORDER BY id`)
}

// ListByInstance returns the jobs of one instance, oldest first.
func (r *JobRepo) ListByInstance(ctx context.Context, instanceID int64) ([]Job, error) {
	return r.list(ctx,
		`SELECT `+jobColumns+` FROM jobs WHERE instance_id = ? ORDER BY id`, instanceID)
}

// ListEnabled returns every enabled job. This is what the scheduler loads: the
// query is filtered in SQL rather than in Go so a large job table does not have
// to be materialised on every reload.
func (r *JobRepo) ListEnabled(ctx context.Context) ([]Job, error) {
	return r.list(ctx, `SELECT `+jobColumns+` FROM jobs WHERE enabled = 1 ORDER BY id`)
}

func (r *JobRepo) list(ctx context.Context, query string, args ...any) ([]Job, error) {
	rows, err := r.s.query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("jobs: list: %w", err)
	}
	defer rows.Close()

	var out []Job
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, fmt.Errorf("jobs: list scan: %w", err)
		}
		out = append(out, *j)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("jobs: list: %w", err)
	}
	return out, nil
}

// Update writes the mutable columns of j back. last_run/last_result are
// deliberately not touched here: only RecordRun owns them, so an edit from the
// UI cannot rewind the scheduler's bookkeeping.
func (r *JobRepo) Update(ctx context.Context, j *Job) error {
	if j == nil {
		return invalidf("jobs: nil job")
	}
	if !ValidJobType(j.Type) {
		return invalidf("jobs: unknown type %q", j.Type)
	}
	if j.Cron == "" {
		return invalidf("jobs: cron expression must not be empty")
	}
	if j.InstanceID == 0 {
		return invalidf("jobs: instance id must not be zero")
	}
	if j.Payload == "" {
		j.Payload = "{}"
	}

	res, err := r.s.exec(ctx,
		`UPDATE jobs SET instance_id = ?, type = ?, cron = ?, payload = ?, enabled = ?
		 WHERE id = ?`,
		j.InstanceID, j.Type, j.Cron, j.Payload, boolToInt(j.Enabled), j.ID,
	)
	if err != nil {
		return mapConstraint(err, nil)
	}
	return affectedOrNotFound(res, "jobs", j.ID)
}

// Delete removes a job.
func (r *JobRepo) Delete(ctx context.Context, id int64) error {
	res, err := r.s.exec(ctx, `DELETE FROM jobs WHERE id = ?`, id)
	if err != nil {
		return mapConstraint(err, nil)
	}
	return affectedOrNotFound(res, "jobs", id)
}

// SetEnabled turns a job on or off without touching its schedule.
func (r *JobRepo) SetEnabled(ctx context.Context, id int64, enabled bool) error {
	res, err := r.s.exec(ctx, `UPDATE jobs SET enabled = ? WHERE id = ?`, boolToInt(enabled), id)
	if err != nil {
		return mapConstraint(err, nil)
	}
	return affectedOrNotFound(res, "jobs", id)
}

// RecordRun stores the outcome of one execution.
//
// result is the human-readable summary shown in the UI ("ok: 12 files",
// "backup failed: disk full"); at is the wall-clock time the run finished.
func (r *JobRepo) RecordRun(ctx context.Context, id int64, result string, at time.Time) error {
	if at.IsZero() {
		at = time.Now().UTC()
	}
	res, err := r.s.exec(ctx,
		`UPDATE jobs SET last_run = ?, last_result = ? WHERE id = ?`,
		toDB(at), result, id)
	if err != nil {
		return mapConstraint(err, nil)
	}
	return affectedOrNotFound(res, "jobs", id)
}

func scanJob(sc scanner) (*Job, error) {
	var (
		j       Job
		enabled int64
		lastRun ScanTime
	)
	if err := sc.Scan(&j.ID, &j.InstanceID, &j.Type, &j.Cron, &j.Payload,
		&enabled, &lastRun, &j.LastResult); err != nil {
		return nil, err
	}
	j.Enabled = enabled != 0
	j.LastRun = TimePtr(lastRun.Time)
	return &j, nil
}
