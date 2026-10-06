package store

import "time"

// This file mirrors the §5.5 schema one struct per table. Column names are
// the plan's verbatim names; the Go fields are the exported spelling.
//
// Conventions:
//
//   - INTEGER PRIMARY KEY -> int64 (0 means "not yet persisted").
//   - TEXT NOT NULL -> string.
//   - Booleans are INTEGER 0/1 in SQLite and bool in Go; the repositories
//     convert, so callers never see 0/1.
//   - DATETIME -> time.Time, stored as RFC3339 UTC text (see time.go). NULL
//     scans as the zero time; TestTimestampRoundTripUTC pins this down.
//   - The owner_id / term / color_mode columns are not decoration: they carry
//     decision D3 (multi-user readiness) and D1 (PTY colour). They must stay
//     in the model and in the INSERT/UPDATE statements even while single-user
//     mode keeps every grant table empty.

// Role names for users.role. Kept as plain strings here on purpose: the store
// validates nothing but membership of this set, so the API layer can keep
// using auth.Role without creating an import cycle
// (auth -> store would be the natural direction for token issuance).
const (
	// RoleAdmin manages everything, including users.
	RoleAdmin = "admin"
	// RoleOperator controls instances but does not manage users.
	RoleOperator = "operator"
	// RoleViewer is read-only.
	RoleViewer = "viewer"
)

// ValidRole reports whether r is one of the three RBAC tiers in §5.5.
func ValidRole(r string) bool {
	switch r {
	case RoleAdmin, RoleOperator, RoleViewer:
		return true
	}
	return false
}

// User is a row of the users table (plan §5.5).
//
// Single-user mode (D3) keeps exactly one row: id=1, username="admin",
// role=RoleAdmin, with an empty PasswordHash so the API can force first-run
// password setup. The schema and this struct are already multi-user shaped.
type User struct {
	ID           int64      `json:"id"`
	Username     string     `json:"username"`
	PasswordHash string     `json:"-"` // never serialised to clients
	Role         string     `json:"role"`
	CreatedAt    time.Time  `json:"createdAt"`
	LastLoginAt  *time.Time `json:"lastLoginAt,omitempty"`
	Disabled     bool       `json:"disabled"`
}

// NeedsPasswordSetup reports whether the account has no usable password yet,
// which is how the API detects the first-run state of the seeded admin row.
//
// It delegates to IsFirstRunHash (migrate.go) so that the two spellings of
// "no password yet" — the empty string and FirstRunPasswordMarker — are decided
// in exactly one place. The marker is not a valid bcrypt hash, so a login
// attempt against it can never succeed.
func (u User) NeedsPasswordSetup() bool { return IsFirstRunHash(u.PasswordHash) }

// Enabled is the inverse of Disabled, for readability at call sites.
func (u User) Enabled() bool { return !u.Disabled }

// Grant is a row of user_instance_grant. The table is empty in single-user
// mode; the rows exist so enabling multi-user needs no migration.
type Grant struct {
	UserID     int64  `json:"userId"`
	InstanceID int64  `json:"instanceId"`
	Perm       string `json:"perm"` // read | control | config
}

// Grant permission levels, matching §5.5's comment verbatim.
const (
	// PermRead allows viewing the instance.
	PermRead = "read"
	// PermControl allows start/stop/restart/console.
	PermControl = "control"
	// PermConfig allows configuration and file mutation.
	PermConfig = "config"
)

// ValidGrantPerm reports whether p is one of read/control/config.
func ValidGrantPerm(p string) bool {
	switch p {
	case PermRead, PermControl, PermConfig:
		return true
	}
	return false
}

// Defaults for the instance columns that carry a DEFAULT in §5.5. They are
// applied in Go as well as in SQL so that the zero-valued struct produces the
// same row as an INSERT that omits the columns.
const (
	// DefaultOwnerID is the owner of the seeded admin user (D3 extension slot).
	DefaultOwnerID int64 = 1
	// DefaultMaxRestart caps automatic restarts before the supervisor gives up.
	DefaultMaxRestart = 5
	// DefaultStopTimeoutSec is the graceful-stop budget before SIGKILL.
	DefaultStopTimeoutSec = 30
	// DefaultTerm is the PTY TERM value (D1). Changing it changes how much
	// colour the server emits.
	DefaultTerm = "xterm-256color"
	// DefaultColorMode is "enhanced" | "basic"; downgraded values are recorded.
	DefaultColorMode = "enhanced"
)

// Terminal colour modes (D1).
const (
	// ColorEnhanced means the server is assumed to emit full ANSI colour.
	ColorEnhanced = "enhanced"
	// ColorBasic is recorded when colour had to be downgraded.
	ColorBasic = "basic"
)

// Instance is a row of the instances table (plan §5.5, §6.1).
type Instance struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"` // UNIQUE
	Dir      string `json:"dir"`  // absolute working directory
	Port     int64  `json:"port"` // UDP
	Dotnet   string `json:"dotnetPath"`
	ServerDL string `json:"serverJar"` // Survivalcraft.dll

	// OwnerID is the D3 extension slot. It defaults to 1 (the seeded admin)
	// and is enforced nowhere yet, but the column and this field exist so
	// multi-user needs no data migration.
	OwnerID int64 `json:"ownerId"`

	AutoStart      bool  `json:"autoStart"`
	AutoRestart    bool  `json:"autoRestart"`
	MaxRestart     int64 `json:"maxRestart"`
	StopTimeoutSec int64 `json:"stopTimeoutSec"`

	// Term is the D1 PTY TERM value; ColorMode records enhanced|basic.
	Term      string `json:"term"`
	ColorMode string `json:"colorMode"`

	CreatedAt time.Time `json:"createdAt"`
	Memo      string    `json:"memo"`
}

// Instance state values shared with the supervisor state machine (§5.1).
const (
	// StateStopped means no process is running.
	StateStopped = "stopped"
	// StateStarting means the process exists but the world is not ready.
	StateStarting = "starting"
	// StateRunning means the ready anchor was seen in the log.
	StateRunning = "running"
	// StateStopping means a graceful stop is in flight.
	StateStopping = "stopping"
	// StateRestarting means the supervisor is cycling the process.
	StateRestarting = "restarting"
	// StateCrashed means the process died on its own.
	StateCrashed = "crashed"
	// StateUnknown is the state before the first observation.
	StateUnknown = "unknown"
)

// InstanceState is a row of instance_state: the runtime snapshot that lets the
// UI recover its display after a panel restart.
type InstanceState struct {
	InstanceID    int64      `json:"instanceId"`
	State         string     `json:"state"`
	PID           int64      `json:"pid"`
	StartedAt     *time.Time `json:"startedAt,omitempty"`
	StoppedAt     *time.Time `json:"stoppedAt,omitempty"`
	ExitCode      *int64     `json:"exitCode,omitempty"`
	LastError     string     `json:"lastError"`
	OnlinePlayers int64      `json:"onlinePlayers"`
}

// Backup kinds (§6.5's three tiers). PruneOldest is per instance AND per kind,
// so these values are part of the retention contract.
const (
	// BackupManual is a user-triggered backup, managed by hand.
	BackupManual = "manual"
	// BackupScheduled is the cron-driven backup (default 04:00 daily).
	BackupScheduled = "scheduled"
	// BackupPreStart is taken before each start; keep the newest 3.
	BackupPreStart = "pre-start"
)

// Backup is a row of the backups table (plan §5.5, §6.5).
type Backup struct {
	ID         int64     `json:"id"`
	InstanceID int64     `json:"instanceId"`
	World      string    `json:"world"`
	Path       string    `json:"path"`
	SizeBytes  int64     `json:"sizeBytes"`
	SHA256     string    `json:"sha256"`
	Kind       string    `json:"kind"` // manual | scheduled | pre-start
	CreatedAt  time.Time `json:"createdAt"`
	Note       string    `json:"note"`
}

// Job types (§6.7).
const (
	// JobBackup runs a scheduled backup.
	JobBackup = "backup"
	// JobRestart restarts an instance on a schedule.
	JobRestart = "restart"
	// JobCommand broadcasts a console command on a schedule.
	JobCommand = "command"
)

// Job is a row of the jobs table: a cron entry plus its last outcome.
type Job struct {
	ID         int64      `json:"id"`
	InstanceID int64      `json:"instanceId"`
	Type       string     `json:"type"` // backup | restart | command
	Cron       string     `json:"cron"`
	Payload    string     `json:"payload"` // JSON, type-specific
	Enabled    bool       `json:"enabled"`
	LastRun    *time.Time `json:"lastRun,omitempty"`
	LastResult string     `json:"lastResult"`
}

// AuditLog is a row of audit_logs. The table is append-only: there is no
// update or delete method, only Insert and the retention Prune.
type AuditLog struct {
	ID     int64     `json:"id"`
	UserID *int64    `json:"userId,omitempty"`
	Action string    `json:"action"`
	Target string    `json:"target"`
	Detail string    `json:"detail"`
	IP     string    `json:"ip"`
	TS     time.Time `json:"ts"`
}

// LogEvent is a row of log_events: an index entry only. The log text itself
// lives in the rolling files (§5.2); the row carries the searchable anchors
// plus an optional JSON payload.
type LogEvent struct {
	ID         int64     `json:"id"`
	InstanceID int64     `json:"instanceId"`
	TS         time.Time `json:"ts"`
	Level      string    `json:"level"` // info | warn | error | debug
	Event      string    `json:"event"` // e.g. state_ready, player_join, crash
	Payload    string    `json:"payload"`
}

// Log levels used by LogEvent.Level.
const (
	// LevelDebug is verbose diagnostic output.
	LevelDebug = "debug"
	// LevelInfo is normal operation.
	LevelInfo = "info"
	// LevelWarn is a recoverable problem.
	LevelWarn = "warn"
	// LevelError is a failure worth surfacing.
	LevelError = "error"
)

// AuditFilter narrows AuditRepo.List. Every field is optional; the zero value
// means "everything, newest first". InstanceID and UserID are pointers so that
// "no filter" and "filter on 0" stay distinguishable.
type AuditFilter struct {
	InstanceID *int64
	UserID     *int64
	Action     string
	From       *time.Time
	To         *time.Time
	Limit      int
	Offset     int
}

// LogEventFilter narrows LogEventRepo.Query. Fields are optional; Levels and
// Events are OR-ed within themselves and AND-ed with the rest.
type LogEventFilter struct {
	InstanceID *int64
	From       *time.Time
	To         *time.Time
	Level      string
	Levels     []string
	Event      string
	Events     []string
	Limit      int
	Offset     int
	// NewestFirst flips the default ordering. The default (false) is
	// chronological, which is what a log view wants; set it for "latest N".
	NewestFirst bool
}

// DefaultLimit is applied when a filter asks for no limit, so that a query
// can never accidentally return an unbounded result set.
const DefaultLimit = 500

// MaxLimit caps caller-requested limits.
const MaxLimit = 10000

// normalize turns caller input into safe LIMIT/OFFSET values.
func (f AuditFilter) normalize() (limit, offset int) {
	limit, offset = f.Limit, f.Offset
	if limit <= 0 {
		limit = DefaultLimit
	}
	if limit > MaxLimit {
		limit = MaxLimit
	}
	if offset < 0 {
		offset = 0
	}
	return limit, offset
}

func (f LogEventFilter) normalize() (limit, offset int) {
	limit, offset = f.Limit, f.Offset
	if limit <= 0 {
		limit = DefaultLimit
	}
	if limit > MaxLimit {
		limit = MaxLimit
	}
	if offset < 0 {
		offset = 0
	}
	return limit, offset
}
