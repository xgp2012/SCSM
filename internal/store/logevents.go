package store

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// LogEventRepo maintains the log index.
//
// §5.2 keeps the log text in rolling files and stores only searchable anchors
// here, which makes InsertBatch the hottest write path in the panel: the
// supervisor emits an event per state transition, player join, crash and so on.
// The batch insert therefore prepares one statement and reuses it inside a
// single transaction instead of issuing N round trips.
type LogEventRepo struct {
	s *Store
}

const logEventColumns = `id, instance_id, ts, level, event, payload`

// InsertBatch appends many events at once.
//
// One prepared statement, one transaction: on a file-backed SQLite database
// that is roughly two orders of magnitude fewer statement compilations than a
// loop of single INSERTs, which is what makes 1000-event flushes cheap.
//
// The transaction is skipped when this Store is already a transactional view
// (WithTx), because SQLite has no nested transactions — the caller's
// transaction already provides atomicity.
func (r *LogEventRepo) InsertBatch(ctx context.Context, events []LogEvent) error {
	if len(events) == 0 {
		return nil
	}

	if r.s.InTx() {
		return r.insertBatch(ctx, events)
	}
	return r.s.withTx(ctx, func(tx *Store) error {
		return tx.LogEvents().insertBatch(ctx, events)
	})
}

func (r *LogEventRepo) insertBatch(ctx context.Context, events []LogEvent) error {
	// Validate everything up front so a bad element fails before any write.
	for i := range events {
		if events[i].InstanceID == 0 {
			return invalidf("log_events: element %d has instance id 0", i)
		}
	}

	stmt, err := r.s.prepare(ctx,
		`INSERT INTO log_events (instance_id, ts, level, event, payload) VALUES (?, ?, ?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("log_events: prepare batch insert: %w", err)
	}
	defer stmt.Close()

	now := time.Now().UTC()
	for i := range events {
		e := &events[i]
		if e.TS.IsZero() {
			e.TS = now
		}
		if e.Level == "" {
			e.Level = LevelInfo
		}
		res, err := stmt.ExecContext(ctx, e.InstanceID, toDB(e.TS), e.Level, e.Event, e.Payload)
		if err != nil {
			return mapConstraint(err, nil)
		}
		if id, err := res.LastInsertId(); err == nil {
			e.ID = id
			e.TS = e.TS.UTC().Truncate(time.Second)
		}
	}
	return nil
}

// Insert appends a single event.
func (r *LogEventRepo) Insert(ctx context.Context, e *LogEvent) error {
	if e == nil {
		return invalidf("log_events: nil event")
	}
	if e.InstanceID == 0 {
		return invalidf("log_events: instance id must not be zero")
	}
	if e.TS.IsZero() {
		e.TS = time.Now().UTC()
	}
	if e.Level == "" {
		e.Level = LevelInfo
	}

	res, err := r.s.exec(ctx,
		`INSERT INTO log_events (instance_id, ts, level, event, payload) VALUES (?, ?, ?, ?, ?)`,
		e.InstanceID, toDB(e.TS), e.Level, e.Event, e.Payload)
	if err != nil {
		return mapConstraint(err, nil)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return fmt.Errorf("log_events: last insert id: %w", err)
	}
	e.ID = id
	e.TS = e.TS.UTC().Truncate(time.Second)
	return nil
}

// Query returns the events matching filter, oldest first by default.
//
// NewestFirst flips the order, which is what "show me the last N events" needs:
// ordering descending and then limiting returns the newest N without scanning
// the whole table.
func (r *LogEventRepo) Query(ctx context.Context, filter LogEventFilter) ([]LogEvent, error) {
	where, args := buildLogEventWhere(filter)
	limit, offset := filter.normalize()

	dir := "ASC"
	if filter.NewestFirst {
		dir = "DESC"
	}
	query := `SELECT ` + logEventColumns + ` FROM log_events` + where +
		` ORDER BY ts ` + dir + `, id ` + dir + ` LIMIT ? OFFSET ?`
	args = append(args, limit, offset)

	rows, err := r.s.query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("log_events: query: %w", err)
	}
	defer rows.Close()

	out := make([]LogEvent, 0, 16)
	for rows.Next() {
		var (
			e  LogEvent
			ts ScanTime
		)
		if err := rows.Scan(&e.ID, &e.InstanceID, &ts, &e.Level, &e.Event, &e.Payload); err != nil {
			return nil, fmt.Errorf("log_events: query scan: %w", err)
		}
		e.TS = ts.Time
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("log_events: query: %w", err)
	}
	return out, nil
}

// Count returns how many events match filter, ignoring Limit/Offset.
func (r *LogEventRepo) Count(ctx context.Context, filter LogEventFilter) (int64, error) {
	where, args := buildLogEventWhere(filter)
	var n int64
	if err := r.s.queryRow(ctx, `SELECT COUNT(1) FROM log_events`+where, args...).Scan(&n); err != nil {
		return 0, fmt.Errorf("log_events: count: %w", err)
	}
	return n, nil
}

// RecentByInstance returns the newest limit events of one instance.
func (r *LogEventRepo) RecentByInstance(ctx context.Context, instanceID int64, limit int) ([]LogEvent, error) {
	id := instanceID
	return r.Query(ctx, LogEventFilter{InstanceID: &id, Limit: limit, NewestFirst: true})
}

// Prune deletes every event older than before and reports how many went.
//
// Retention is by age because the index is a cache of the log files: once the
// text has rotated away, the anchor is no longer useful.
func (r *LogEventRepo) Prune(ctx context.Context, before time.Time) (int64, error) {
	res, err := r.s.exec(ctx, `DELETE FROM log_events WHERE ts < ?`, toDB(before))
	if err != nil {
		return 0, fmt.Errorf("log_events: prune: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("log_events: prune rows affected: %w", err)
	}
	return n, nil
}

// PruneInstanceBefore prunes one instance's events older than before.
func (r *LogEventRepo) PruneInstanceBefore(ctx context.Context, instanceID int64, before time.Time) (int64, error) {
	res, err := r.s.exec(ctx,
		`DELETE FROM log_events WHERE instance_id = ? AND ts < ?`, instanceID, toDB(before))
	if err != nil {
		return 0, fmt.Errorf("log_events: prune instance %d: %w", instanceID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("log_events: prune rows affected: %w", err)
	}
	return n, nil
}

// buildLogEventWhere renders the filter into a WHERE clause plus bind args.
func buildLogEventWhere(f LogEventFilter) (string, []any) {
	var conds []string
	var args []any

	if f.InstanceID != nil {
		conds = append(conds, `instance_id = ?`)
		args = append(args, *f.InstanceID)
	}
	if f.From != nil {
		conds = append(conds, `ts >= ?`)
		args = append(args, toDB(*f.From))
	}
	if f.To != nil {
		conds = append(conds, `ts <= ?`)
		args = append(args, toDB(*f.To))
	}
	if f.Level != "" {
		conds = append(conds, `level = ?`)
		args = append(args, f.Level)
	}
	if len(f.Levels) > 0 {
		conds = append(conds, `level IN (`+placeholders(len(f.Levels))+`)`)
		for _, l := range f.Levels {
			args = append(args, l)
		}
	}
	if f.Event != "" {
		conds = append(conds, `event = ?`)
		args = append(args, f.Event)
	}
	if len(f.Events) > 0 {
		conds = append(conds, `event IN (`+placeholders(len(f.Events))+`)`)
		for _, e := range f.Events {
			args = append(args, e)
		}
	}

	if len(conds) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(conds, " AND "), args
}

// placeholders returns "?, ?, ?" for n values. n is always len() of a slice the
// caller built, never user text, so the generated SQL is still parameterised.
func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?, ", n), ", ")
}
