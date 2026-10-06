package store

import (
	"database/sql"
	"database/sql/driver"
	"fmt"
	"strings"
	"time"
)

// Timestamp handling — the one driver quirk every downstream caller must know.
//
// The §5.5 DDL declares the time columns as DATETIME. SQLite itself has no
// date type: a DATETIME column has NUMERIC affinity and stores whatever the
// driver hands it. modernc.org/sqlite would therefore be free to write a
// Julian day number or a Go time.Time string, and reading it back into a
// time.Time would then depend on its heuristics (it does parse RFC3339, but
// the storage format would be implicit and unportable).
//
// This package pins the convention explicitly:
//
//	Every time value is stored as RFC3339 UTC text ("2006-01-02T15:04:05Z")
//	and is converted in the repository layer, never by the driver.
//
// Why this matters beyond tidiness:
//
//   - Retention ("keep the 3 newest pre-start backups", §6.5) and audit/log
//     filtering compare timestamps. If one code path stored a Unix integer and
//     another an RFC3339 string, a lexicographic MAX()/comparison would be
//     silently wrong.
//   - "WHERE ts >= ?" on an index only uses the index when the bound is
//     text-comparable to the stored text.
//   - RFC3339 UTC is sortable lexicographically, which is why the plain
//     string comparison in the SQL below is correct.
//
// Consequences for callers:
//
//   - Values handed back are always UTC and truncated to the second (SQLite
//     text has no sub-second precision in this RFC3339 layout).
//   - A column left NULL scans as the zero time.Time; use the *Ptr helpers
//     (TimePtr) plus ErrNotFound-style checks when "never happened" must be
//     distinguished from "happened at the zero instant".
//   - Anything outside this package that writes these columns directly must
//     use the same layout, otherwise ordering breaks.
const (
	// timeLayout is the on-disk timestamp format.
	timeLayout = "2006-01-02T15:04:05Z07:00"

	// QueryLayout is the timestamp layout callers should use when building
	// their own SQL bounds for the time columns.
	QueryLayout = timeLayout
)

// toDB converts a time.Time into the canonical on-disk value. The zero time
// maps to NULL so that "never" stays distinguishable from a real instant.
func toDB(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UTC().Truncate(time.Second).Format(timeLayout)
}

// toDBPtr converts an optional time; nil stays NULL.
func toDBPtr(t *time.Time) any {
	if t == nil || t.IsZero() {
		return nil
	}
	return toDB(*t)
}

// parseTime converts a raw column value (string, []byte or time.Time,
// depending on what the driver produced) into UTC. NULL becomes the zero time.
//
// It is deliberately tolerant: even with the write convention above, a row
// could have been written by an older build or by hand, so several layouts are
// accepted. Values that cannot be parsed are an error rather than a silent
// zero, because a silently-zero timestamp inside retention logic deletes the
// wrong backups.
func parseTime(v any) (time.Time, error) {
	switch t := v.(type) {
	case nil:
		return time.Time{}, nil
	case time.Time:
		return t.UTC().Truncate(time.Second), nil
	case []byte:
		return parseTimeString(string(t))
	case string:
		return parseTimeString(t)
	case int64:
		// Defensive: a Julian day number like the pure-Go SQLite drivers
		// sometimes emit for DATETIME affinity columns.
		return julianToTime(float64(t))
	case float64:
		return julianToTime(t)
	default:
		return time.Time{}, fmt.Errorf("store: cannot parse timestamp of type %T (%v)", v, v)
	}
}

func parseTimeString(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, nil
	}
	layouts := [...]string{
		timeLayout, // 2026-10-06T03:23:53Z          (what we write)
		"2006-01-02T15:04:05.999999999Z07:00",
		"2006-01-02 15:04:05.999999999-07:00",
		"2006-01-02 15:04:05", // SQLite CURRENT_TIMESTAMP
		"2006-01-02T15:04:05", // no zone -> assumed UTC
		"2006-01-02 15:04:05Z07:00",
	}
	for _, layout := range layouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC().Truncate(time.Second), nil
		}
	}
	return time.Time{}, fmt.Errorf("store: unparsable timestamp %q", s)
}

func julianToTime(jd float64) (time.Time, error) {
	if jd == 0 {
		return time.Time{}, nil
	}
	const unixEpochJulian = 2440587.5
	secs := (jd - unixEpochJulian) * 86400.0
	return time.Unix(int64(secs), 0).UTC().Truncate(time.Second), nil
}

// TimePtr returns a pointer to a copy of t, or nil when t is the zero time.
// It is the helper to use for the nullable time fields (LastRun, StoppedAt, …)
// so that JSON marshalling of a model emits null instead of "0001-01-01T…".
//
// The copy matters: the pointer outlives the local time.Time it was built
// from, and handing out &t would alias a loop variable.
func TimePtr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	c := t
	return &c
}

// NullTime is a time.Time that scans NULL, RFC3339 text and driver-native
// time values uniformly. It exists mainly for tests and for callers that want
// to detect NULL explicitly instead of receiving the zero time.
type NullTime struct {
	Time  time.Time
	Valid bool
}

// Scan implements sql.Scanner.
func (n *NullTime) Scan(src any) error {
	t, err := parseTime(src)
	if err != nil {
		return err
	}
	n.Time, n.Valid = t, !t.IsZero()
	return nil
}

// Value implements driver.Valuer, writing the canonical RFC3339 UTC text.
func (n NullTime) Value() (driver.Value, error) {
	if !n.Valid {
		return nil, nil
	}
	return toDB(n.Time), nil
}

// ScanTime is the scan target used by every repository query in this package.
//
// It is a named type (not sql.NullTime) because it must tolerate both the
// RFC3339 text this package writes and driver-native time values, and because
// the conversion policy should live in one place. A NULL column yields the
// zero time and no error.
type ScanTime struct{ time.Time }

// Scan implements sql.Scanner.
func (s *ScanTime) Scan(src any) error {
	t, err := parseTime(src)
	if err != nil {
		return err
	}
	s.Time = t
	return nil
}

// scanTime reads a sql.Rows/Row column into a time.Time.
func scanTime(sc interface{ Scan(...any) error }) (time.Time, error) {
	var st ScanTime
	if err := sc.Scan(&st); err != nil {
		return time.Time{}, err
	}
	return st.Time, nil
}

// ensure the standard interfaces are satisfied.
var (
	_ sql.Scanner   = (*ScanTime)(nil)
	_ sql.Scanner   = (*NullTime)(nil)
	_ driver.Valuer = NullTime{}
)
