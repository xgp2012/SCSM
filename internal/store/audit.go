package store

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// AuditRepo appends to and reads audit_logs.
//
// The table is append-only by design: there is no Update and no per-row Delete,
// only Insert and the retention Prune. An audit trail that can be edited is not
// an audit trail, so the omission is deliberate rather than unfinished.
type AuditRepo struct {
	s *Store
}

const auditColumns = `id, user_id, action, target, detail, ip, ts`

// Insert appends one audit record and fills in its ID (and TS when unset).
func (r *AuditRepo) Insert(ctx context.Context, e *AuditLog) error {
	if e == nil {
		return invalidf("audit: nil entry")
	}
	if strings.TrimSpace(e.Action) == "" {
		return invalidf("audit: action must not be empty")
	}
	if e.TS.IsZero() {
		e.TS = time.Now().UTC()
	}

	res, err := r.s.exec(ctx,
		`INSERT INTO audit_logs (user_id, action, target, detail, ip, ts)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		int64PtrToDB(e.UserID), e.Action, e.Target, e.Detail, e.IP, toDB(e.TS),
	)
	if err != nil {
		return mapConstraint(err, nil)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return fmt.Errorf("audit: last insert id: %w", err)
	}
	e.ID = id
	e.TS = e.TS.UTC().Truncate(time.Second)
	return nil
}

// Log is a convenience wrapper for the common "record an action" call.
func (r *AuditRepo) Log(ctx context.Context, userID *int64, action, target, detail, ip string) error {
	return r.Insert(ctx, &AuditLog{
		UserID: userID,
		Action: action,
		Target: target,
		Detail: detail,
		IP:     ip,
		TS:     time.Now().UTC(),
	})
}

// List returns the audit entries matching filter, newest first.
//
// Zero-value filter fields mean "no restriction". The result is ordered by ts
// then id, both descending, so pagination with Limit/Offset is stable even when
// many entries share a second.
func (r *AuditRepo) List(ctx context.Context, filter AuditFilter) ([]AuditLog, error) {
	where, args := buildAuditWhere(filter)
	limit, offset := filter.normalize()

	query := `SELECT ` + auditColumns + ` FROM audit_logs` + where +
		` ORDER BY ts DESC, id DESC LIMIT ? OFFSET ?`
	args = append(args, limit, offset)

	rows, err := r.s.query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("audit: list: %w", err)
	}
	defer rows.Close()

	out := make([]AuditLog, 0, 16)
	for rows.Next() {
		var (
			e  AuditLog
			ts ScanTime
		)
		if err := rows.Scan(&e.ID, &e.UserID, &e.Action, &e.Target, &e.Detail, &e.IP, &ts); err != nil {
			return nil, fmt.Errorf("audit: list scan: %w", err)
		}
		e.TS = ts.Time
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("audit: list: %w", err)
	}
	return out, nil
}

// Count returns how many entries match filter, ignoring Limit/Offset. It lets
// the API render real pagination controls instead of guessing.
func (r *AuditRepo) Count(ctx context.Context, filter AuditFilter) (int64, error) {
	where, args := buildAuditWhere(filter)
	var n int64
	if err := r.s.queryRow(ctx, `SELECT COUNT(1) FROM audit_logs`+where, args...).Scan(&n); err != nil {
		return 0, fmt.Errorf("audit: count: %w", err)
	}
	return n, nil
}

// ListByInstance is the common case spelled out: the history of one instance.
func (r *AuditRepo) ListByInstance(ctx context.Context, instanceID int64, limit, offset int) ([]AuditLog, error) {
	id := instanceID
	return r.List(ctx, AuditFilter{InstanceID: &id, Limit: limit, Offset: offset})
}

// Prune deletes every entry older than before and reports how many went.
func (r *AuditRepo) Prune(ctx context.Context, before time.Time) (int64, error) {
	res, err := r.s.exec(ctx, `DELETE FROM audit_logs WHERE ts < ?`, toDB(before))
	if err != nil {
		return 0, fmt.Errorf("audit: prune: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("audit: prune rows affected: %w", err)
	}
	return n, nil
}

// buildAuditWhere renders the filter into a WHERE clause plus bind arguments.
//
// The clause is assembled from a fixed set of snippets selected by which fields
// are set; no caller input ever reaches the SQL text, only the args slice.
func buildAuditWhere(f AuditFilter) (string, []any) {
	var conds []string
	var args []any

	if f.InstanceID != nil {
		// Instances are matched through the "instance:<id>" target convention
		// used by the API layer, so the audit table needs no instance column.
		conds = append(conds, `target = ?`)
		args = append(args, fmt.Sprintf("instance:%d", *f.InstanceID))
	}
	if f.UserID != nil {
		conds = append(conds, `user_id = ?`)
		args = append(args, *f.UserID)
	}
	if f.Action != "" {
		conds = append(conds, `action = ?`)
		args = append(args, f.Action)
	}
	if f.From != nil {
		conds = append(conds, `ts >= ?`)
		args = append(args, toDB(*f.From))
	}
	if f.To != nil {
		conds = append(conds, `ts <= ?`)
		args = append(args, toDB(*f.To))
	}

	if len(conds) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(conds, " AND "), args
}
