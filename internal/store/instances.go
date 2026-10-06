package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// InstanceRepo reads and writes instances together with their runtime snapshot
// (instance_state) and their dependent rows.
type InstanceRepo struct {
	s *Store
}

// instanceColumns is the canonical SELECT list for Instance.
const instanceColumns = `id, name, dir, port, dotnet_path, server_jar, owner_id,
	auto_start, auto_restart, max_restart, stop_timeout_sec, term, color_mode,
	created_at, memo`

// instanceStateColumns is the canonical SELECT list for InstanceState.
const instanceStateColumns = `instance_id, state, pid, started_at, stopped_at,
	exit_code, last_error, online_players`

// Create inserts inst and fills in its ID (and CreatedAt when unset).
//
// Defaults: a zero MaxRestart/StopTimeoutSec/Term/ColorMode/OwnerID is replaced
// by the §5.5 DEFAULT so that a partially-filled struct from an API handler
// produces the same row as an INSERT that omits the column. A duplicate name
// maps to ErrDuplicate.
func (r *InstanceRepo) Create(ctx context.Context, inst *Instance) error {
	if inst == nil {
		return invalidf("instances: nil instance")
	}
	name := strings.TrimSpace(inst.Name)
	if name == "" {
		return invalidf("instances: name must not be empty")
	}
	if strings.TrimSpace(inst.Dir) == "" {
		return invalidf("instances: dir must not be empty")
	}
	if inst.Port <= 0 || inst.Port > 65535 {
		return invalidf("instances: port %d out of range", inst.Port)
	}

	inst.Name = name
	applyInstanceDefaults(inst)
	if inst.CreatedAt.IsZero() {
		inst.CreatedAt = time.Now().UTC()
	}

	res, err := r.s.exec(ctx,
		`INSERT INTO instances
		   (name, dir, port, dotnet_path, server_jar, owner_id,
		    auto_start, auto_restart, max_restart, stop_timeout_sec,
		    term, color_mode, created_at, memo)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		inst.Name, inst.Dir, inst.Port, inst.Dotnet, inst.ServerDL, inst.OwnerID,
		boolToInt(inst.AutoStart), boolToInt(inst.AutoRestart), inst.MaxRestart, inst.StopTimeoutSec,
		inst.Term, inst.ColorMode, toDB(inst.CreatedAt), inst.Memo,
	)
	if err != nil {
		return mapConstraint(err, duplicatef("instances: name %q already exists", inst.Name))
	}
	id, err := res.LastInsertId()
	if err != nil {
		return fmt.Errorf("instances: last insert id: %w", err)
	}
	inst.ID = id
	inst.CreatedAt = inst.CreatedAt.UTC().Truncate(time.Second)
	return nil
}

// applyInstanceDefaults fills the columns that carry a DEFAULT in §5.5.
func applyInstanceDefaults(inst *Instance) {
	if inst.OwnerID == 0 {
		inst.OwnerID = DefaultOwnerID
	}
	if inst.MaxRestart == 0 {
		inst.MaxRestart = DefaultMaxRestart
	}
	if inst.StopTimeoutSec == 0 {
		inst.StopTimeoutSec = DefaultStopTimeoutSec
	}
	if inst.Term == "" {
		// D1: the PTY TERM decides how much colour the server emits. An
		// empty value must not silently disable colour.
		inst.Term = DefaultTerm
	}
	if inst.ColorMode == "" {
		inst.ColorMode = DefaultColorMode
	}
}

// Get returns the instance with the given id, or ErrNotFound.
func (r *InstanceRepo) Get(ctx context.Context, id int64) (*Instance, error) {
	inst, err := scanInstance(r.s.queryRow(ctx,
		`SELECT `+instanceColumns+` FROM instances WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, notFoundf("instances: id %d", id)
	}
	if err != nil {
		return nil, fmt.Errorf("instances: get %d: %w", id, err)
	}
	return inst, nil
}

// GetByName returns the instance with the given name, or ErrNotFound.
func (r *InstanceRepo) GetByName(ctx context.Context, name string) (*Instance, error) {
	inst, err := scanInstance(r.s.queryRow(ctx,
		`SELECT `+instanceColumns+` FROM instances WHERE name = ?`, strings.TrimSpace(name)))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, notFoundf("instances: name %q", name)
	}
	if err != nil {
		return nil, fmt.Errorf("instances: get by name %q: %w", name, err)
	}
	return inst, nil
}

// List returns every instance, oldest first.
func (r *InstanceRepo) List(ctx context.Context) ([]Instance, error) {
	return r.list(ctx, `SELECT `+instanceColumns+` FROM instances ORDER BY id`)
}

// ListByOwner returns the instances owned by userID. In single-user mode every
// instance belongs to user 1, so this is often the whole list.
func (r *InstanceRepo) ListByOwner(ctx context.Context, ownerID int64) ([]Instance, error) {
	return r.list(ctx,
		`SELECT `+instanceColumns+` FROM instances WHERE owner_id = ? ORDER BY id`, ownerID)
}

// ListAutoStart returns the instances flagged auto_start, which the supervisor
// starts on boot (§6.7's "start on panel boot" behaviour).
func (r *InstanceRepo) ListAutoStart(ctx context.Context) ([]Instance, error) {
	return r.list(ctx,
		`SELECT `+instanceColumns+` FROM instances WHERE auto_start = 1 ORDER BY id`)
}

func (r *InstanceRepo) list(ctx context.Context, query string, args ...any) ([]Instance, error) {
	rows, err := r.s.query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("instances: list: %w", err)
	}
	defer rows.Close()

	var out []Instance
	for rows.Next() {
		inst, err := scanInstance(rows)
		if err != nil {
			return nil, fmt.Errorf("instances: list scan: %w", err)
		}
		out = append(out, *inst)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("instances: list: %w", err)
	}
	return out, nil
}

// Update writes every mutable column of inst back. Name changes are checked
// against the UNIQUE constraint.
func (r *InstanceRepo) Update(ctx context.Context, inst *Instance) error {
	if inst == nil {
		return invalidf("instances: nil instance")
	}
	if strings.TrimSpace(inst.Name) == "" {
		return invalidf("instances: name must not be empty")
	}
	if strings.TrimSpace(inst.Dir) == "" {
		return invalidf("instances: dir must not be empty")
	}
	if inst.Port <= 0 || inst.Port > 65535 {
		return invalidf("instances: port %d out of range", inst.Port)
	}
	applyInstanceDefaults(inst)

	res, err := r.s.exec(ctx,
		`UPDATE instances SET
		   name = ?, dir = ?, port = ?, dotnet_path = ?, server_jar = ?, owner_id = ?,
		   auto_start = ?, auto_restart = ?, max_restart = ?, stop_timeout_sec = ?,
		   term = ?, color_mode = ?, memo = ?
		 WHERE id = ?`,
		inst.Name, inst.Dir, inst.Port, inst.Dotnet, inst.ServerDL, inst.OwnerID,
		boolToInt(inst.AutoStart), boolToInt(inst.AutoRestart), inst.MaxRestart, inst.StopTimeoutSec,
		inst.Term, inst.ColorMode, inst.Memo, inst.ID,
	)
	if err != nil {
		return mapConstraint(err, duplicatef("instances: name %q already exists", inst.Name))
	}
	return affectedOrNotFound(res, "instances", inst.ID)
}

// SetOwner reassigns an instance to another user (the D3 ownership slot).
func (r *InstanceRepo) SetOwner(ctx context.Context, id, ownerID int64) error {
	res, err := r.s.exec(ctx, `UPDATE instances SET owner_id = ? WHERE id = ?`, ownerID, id)
	if err != nil {
		return mapConstraint(err, nil)
	}
	return affectedOrNotFound(res, "instances", id)
}

// SetAutoStart toggles the boot-start flag without touching the rest of the row,
// so the supervisor's boot sweep does not race with a concurrent full Update.
func (r *InstanceRepo) SetAutoStart(ctx context.Context, id int64, autoStart bool) error {
	res, err := r.s.exec(ctx, `UPDATE instances SET auto_start = ? WHERE id = ?`, boolToInt(autoStart), id)
	if err != nil {
		return mapConstraint(err, nil)
	}
	return affectedOrNotFound(res, "instances", id)
}

// Delete removes an instance and, with it, every dependent row:
// instance_state, log_events, backups, jobs and user_instance_grant.
//
// The cascade is written in Go inside a single transaction rather than relying
// on ON DELETE CASCADE. Three reasons: §5.5's DDL declares no foreign keys, the
// delete must be atomic with its cleanup, and tests can assert exactly which
// tables were touched. The instance row is deleted last so a failure halfway
// through leaves the instance (and therefore a consistent, repairable state)
// rather than orphans pointing at nothing.
func (r *InstanceRepo) Delete(ctx context.Context, id int64) error {
	return r.s.withTx(ctx, func(tx *Store) error {
		// Existence check first, so deleting a missing id is ErrNotFound
		// rather than a silent no-op.
		if _, err := tx.Instances().Get(ctx, id); err != nil {
			return err
		}
		return tx.Instances().deleteCascade(ctx, id)
	})
}

// DeleteKeepingData removes only the instance row and its runtime snapshot,
// leaving log_events, backups and jobs behind.
//
// This is the "unregister the instance but keep the history and the archives"
// path a UI may offer. It is still one transaction, but it deliberately does
// not touch the tables that hold user-visible history.
func (r *InstanceRepo) DeleteKeepingData(ctx context.Context, id int64) error {
	return r.s.withTx(ctx, func(tx *Store) error {
		if _, err := tx.Instances().Get(ctx, id); err != nil {
			return err
		}
		if _, err := tx.exec(ctx, `DELETE FROM instance_state WHERE instance_id = ?`, id); err != nil {
			return fmt.Errorf("instances: delete state for %d: %w", id, err)
		}
		res, err := tx.exec(ctx, `DELETE FROM instances WHERE id = ?`, id)
		if err != nil {
			return mapConstraint(err, nil)
		}
		return affectedOrNotFound(res, "instances", id)
	})
}

// deleteCascade is the shared body of the cascade, already inside a
// transaction. It returns the number of dependent rows removed per table so
// callers (and tests) can report what happened.
func (r *InstanceRepo) deleteCascade(ctx context.Context, id int64) error {
	_, err := r.DeleteCascadeCounts(ctx, id)
	return err
}

// DeleteCascadeCounts is deleteCascade with a report. It must be called inside
// an existing transaction (WithTx) or the individual deletes will not be atomic;
// it is exported for the API layer's "deleted instance X and N backups" message.
func (r *InstanceRepo) DeleteCascadeCounts(ctx context.Context, id int64) (map[string]int64, error) {
	tables := []string{
		"instance_state",
		"log_events",
		"backups",
		"jobs",
		"user_instance_grant",
	}
	counts := make(map[string]int64, len(tables)+1)

	for _, table := range tables {
		// Table names are a closed set defined here, never caller input.
		res, err := r.s.exec(ctx, `DELETE FROM `+table+` WHERE instance_id = ?`, id)
		if err != nil {
			return nil, fmt.Errorf("instances: cascade delete from %s for %d: %w", table, id, err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return nil, fmt.Errorf("instances: cascade rows affected in %s: %w", table, err)
		}
		counts[table] = n
	}

	res, err := r.s.exec(ctx, `DELETE FROM instances WHERE id = ?`, id)
	if err != nil {
		return nil, mapConstraint(err, nil)
	}
	if err := affectedOrNotFound(res, "instances", id); err != nil {
		return nil, err
	}
	counts["instances"] = 1
	return counts, nil
}

// --- runtime snapshot (instance_state) -----------------------------------

// GetState returns the runtime snapshot for instanceID.
//
// A missing row is ErrNotFound: an instance that has never run has no snapshot,
// which the caller should render as "stopped" rather than as an error state.
func (r *InstanceRepo) GetState(ctx context.Context, instanceID int64) (*InstanceState, error) {
	st, err := scanInstanceState(r.s.queryRow(ctx,
		`SELECT `+instanceStateColumns+` FROM instance_state WHERE instance_id = ?`, instanceID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, notFoundf("instance_state: instance %d", instanceID)
	}
	if err != nil {
		return nil, fmt.Errorf("instance_state: get %d: %w", instanceID, err)
	}
	return st, nil
}

// SaveState writes the runtime snapshot, creating the row on first write.
//
// This is an UPSERT because the supervisor calls it for instances that have
// never been started: there is no row yet, and a plain UPDATE would silently
// do nothing while the UI kept showing "unknown" forever.
func (r *InstanceRepo) SaveState(ctx context.Context, state InstanceState) error {
	if state.InstanceID == 0 {
		return invalidf("instance_state: instance id must not be zero")
	}
	if state.State == "" {
		state.State = StateUnknown
	}
	_, err := r.s.exec(ctx,
		`INSERT INTO instance_state
		   (instance_id, state, pid, started_at, stopped_at, exit_code, last_error, online_players)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT (instance_id) DO UPDATE SET
		   state = excluded.state,
		   pid = excluded.pid,
		   started_at = excluded.started_at,
		   stopped_at = excluded.stopped_at,
		   exit_code = excluded.exit_code,
		   last_error = excluded.last_error,
		   online_players = excluded.online_players`,
		state.InstanceID, state.State, state.PID,
		toDBPtr(state.StartedAt), toDBPtr(state.StoppedAt), int64PtrToDB(state.ExitCode),
		state.LastError, state.OnlinePlayers,
	)
	if err != nil {
		return mapConstraint(err, nil)
	}
	return nil
}

// Upsert is an alias for SaveState, matching the name used elsewhere in this
// package's vocabulary.
func (r *InstanceRepo) Upsert(ctx context.Context, state InstanceState) error {
	return r.SaveState(ctx, state)
}

// ListAllStates returns every runtime snapshot, ordered by instance id.
func (r *InstanceRepo) ListAllStates(ctx context.Context) ([]InstanceState, error) {
	rows, err := r.s.query(ctx,
		`SELECT `+instanceStateColumns+` FROM instance_state ORDER BY instance_id`)
	if err != nil {
		return nil, fmt.Errorf("instance_state: list: %w", err)
	}
	defer rows.Close()

	var out []InstanceState
	for rows.Next() {
		st, err := scanInstanceState(rows)
		if err != nil {
			return nil, fmt.Errorf("instance_state: list scan: %w", err)
		}
		out = append(out, *st)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("instance_state: list: %w", err)
	}
	return out, nil
}

// DeleteState removes the snapshot for an instance (used when an instance is
// unregistered but its history is kept).
func (r *InstanceRepo) DeleteState(ctx context.Context, instanceID int64) error {
	_, err := r.s.exec(ctx, `DELETE FROM instance_state WHERE instance_id = ?`, instanceID)
	if err != nil {
		return fmt.Errorf("instance_state: delete %d: %w", instanceID, err)
	}
	return nil
}

// --- scanning ------------------------------------------------------------

func scanInstance(sc scanner) (*Instance, error) {
	var (
		inst      Instance
		createdAt ScanTime
		autoStart int64
		autoRest  int64
	)
	if err := sc.Scan(
		&inst.ID, &inst.Name, &inst.Dir, &inst.Port, &inst.Dotnet, &inst.ServerDL, &inst.OwnerID,
		&autoStart, &autoRest, &inst.MaxRestart, &inst.StopTimeoutSec,
		&inst.Term, &inst.ColorMode, &createdAt, &inst.Memo,
	); err != nil {
		return nil, err
	}
	inst.AutoStart = autoStart != 0
	inst.AutoRestart = autoRest != 0
	inst.CreatedAt = createdAt.Time
	return &inst, nil
}

func scanInstanceState(sc scanner) (*InstanceState, error) {
	var (
		st        InstanceState
		startedAt ScanTime
		stoppedAt ScanTime
		exitCode  sql.NullInt64
		players   sql.NullInt64
	)
	if err := sc.Scan(&st.InstanceID, &st.State, &st.PID, &startedAt, &stoppedAt,
		&exitCode, &st.LastError, &players); err != nil {
		return nil, err
	}
	st.StartedAt = TimePtr(startedAt.Time)
	st.StoppedAt = TimePtr(stoppedAt.Time)
	if exitCode.Valid {
		v := exitCode.Int64
		st.ExitCode = &v
	}
	if players.Valid {
		st.OnlinePlayers = players.Int64
	}
	return &st, nil
}

// int64PtrToDB renders an optional int as a nullable column value.
func int64PtrToDB(v *int64) any {
	if v == nil {
		return nil
	}
	return *v
}
