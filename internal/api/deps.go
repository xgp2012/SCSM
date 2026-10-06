// Package api implements the panel's HTTP layer: the /api/v1 REST surface, the
// two WebSocket endpoints, and the middleware chain (auth, RBAC, rate limiting,
// audit, recovery, structured logging) described in §5.6 and §5.7 of the plan.
//
// # Dependency strategy
//
// The panel is assembled from several packages that are developed in parallel
// (store, supervisor, config, world, files, scheduler). To keep this package
// compilable and fully testable on its own, every one of those dependencies is
// consumed through a *narrow interface declared here* rather than by importing
// the sibling package. Two consequences:
//
//  1. The package builds and its tests run with fakes only — no SQLite, no
//     dotnet, no game server, no network.
//  2. Wiring the real packages is a pure adapter exercise, confined to
//     adapters.go, and cannot force changes in handler code.
//
// Every interface below is documented with the exact semantics the handlers
// rely on, so the sibling agents can reconcile their signatures.
//
// # Nil / nop behaviour
//
// A Deps field left nil is replaced by a "not implemented" implementation that
// returns ErrNotImplemented. Handlers translate that into HTTP 501 with a
// machine-readable code. Handlers never silently return empty data for a
// missing backend.
package api

import (
	"context"
	"errors"
	"time"

	"scnetm/internal/auth"
)

// ---------------------------------------------------------------------------
// Sentinel errors shared by all store/manager interfaces.
// ---------------------------------------------------------------------------

var (
	// ErrNotFound is returned by any store lookup that matches no row.
	// Handlers map it to HTTP 404.
	ErrNotFound = errors.New("api: resource not found")
	// ErrConflict is returned when a write violates a uniqueness or state
	// constraint (duplicate instance name, duplicate username). Handlers map
	// it to HTTP 409.
	ErrConflict = errors.New("api: resource conflict")
	// ErrInvalid is returned when a store rejects a value as malformed.
	// Handlers map it to HTTP 422.
	ErrInvalid = errors.New("api: invalid value")
	// ErrNotImplemented is returned by the nop implementations that stand in
	// for unavailable backends. Handlers map it to HTTP 501 and never treat it
	// as "empty result".
	ErrNotImplemented = errors.New("api: not implemented in this build")
	// ErrUnavailable is returned when a backend exists but is temporarily
	// unusable (e.g. the supervisor cannot reach the process). Handlers map it
	// to HTTP 503.
	ErrUnavailable = errors.New("api: dependency unavailable")
)

// ---------------------------------------------------------------------------
// Model structs
// ---------------------------------------------------------------------------
//
// These mirror the SQLite schema in §5.5. They live here (not in internal/store)
// so that this package has no compile-time dependency on the store package;
// adapters.go converts between the two representations. Field names are chosen
// to match the schema columns, and json tags define the public API shape.

// User mirrors the users table (§5.5).
//
// PasswordHash is deliberately excluded from JSON: it must never leave the
// process. Use HashPassword separately when persisting.
type User struct {
	ID           int64      `json:"id"`
	Username     string     `json:"username"`
	PasswordHash string     `json:"-"`
	Role         auth.Role  `json:"role"`
	CreatedAt    time.Time  `json:"created_at"`
	LastLoginAt  *time.Time `json:"last_login_at,omitempty"`
	Disabled     bool       `json:"disabled"`

	// HasPassword reports whether password_hash is non-empty. It is derived,
	// and drives the first-run "setup required" flow: a seeded admin with an
	// empty hash must set a password before logging in.
	HasPassword bool `json:"has_password"`
}

// InstanceGrant mirrors user_instance_grant (§5.5). Single-user mode keeps this
// table empty; the type exists so multi-user needs no migration.
type InstanceGrant struct {
	UserID     int64          `json:"user_id"`
	InstanceID int64          `json:"instance_id"`
	Perm       auth.GrantPerm `json:"perm"`
}

// Instance mirrors the instances table (§5.5).
type Instance struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Dir        string `json:"dir"`
	Port       int    `json:"port"`
	DotnetPath string `json:"dotnet_path,omitempty"`
	ServerJar  string `json:"server_jar,omitempty"`
	OwnerID    int64  `json:"owner_id"`

	AutoStart      bool `json:"auto_start"`
	AutoRestart    bool `json:"auto_restart"`
	MaxRestart     int  `json:"max_restart"`
	StopTimeoutSec int  `json:"stop_timeout_sec"`

	Term      string `json:"term"`
	ColorMode string `json:"color_mode"`

	CreatedAt time.Time `json:"created_at"`
	Memo      string    `json:"memo,omitempty"`
}

// InstanceState mirrors instance_state (§5.5): the persisted run snapshot used
// to render something sensible after a panel restart.
type InstanceState struct {
	InstanceID    int64      `json:"instance_id"`
	State         string     `json:"state"`
	PID           int        `json:"pid,omitempty"`
	StartedAt     *time.Time `json:"started_at,omitempty"`
	StoppedAt     *time.Time `json:"stopped_at,omitempty"`
	ExitCode      *int       `json:"exit_code,omitempty"`
	LastError     string     `json:"last_error,omitempty"`
	OnlinePlayers int        `json:"online_players"`
}

// InstanceListFilter narrows an instance listing. An empty filter lists all
// instances the caller may see.
type InstanceListFilter struct {
	OwnerID int64
	Limit   int
	Offset  int
}

// Backup mirrors the backups table (§5.5).
type Backup struct {
	ID         int64     `json:"id"`
	InstanceID int64     `json:"instance_id"`
	World      string    `json:"world,omitempty"`
	Path       string    `json:"path"`
	SizeBytes  int64     `json:"size_bytes"`
	SHA256     string    `json:"sha256,omitempty"`
	Kind       string    `json:"kind"`
	CreatedAt  time.Time `json:"created_at"`
	Note       string    `json:"note,omitempty"`
}

// BackupFilter narrows a backup listing.
type BackupFilter struct {
	InstanceID int64
	Limit      int
	Offset     int
}

// Job mirrors the jobs table (§5.5).
type Job struct {
	ID         int64      `json:"id"`
	InstanceID int64      `json:"instance_id"`
	Type       string     `json:"type"`
	Cron       string     `json:"cron"`
	Payload    string     `json:"payload,omitempty"`
	Enabled    bool       `json:"enabled"`
	LastRun    *time.Time `json:"last_run,omitempty"`
	LastResult string     `json:"last_result,omitempty"`
}

// AuditEntry mirrors the audit_logs table (§5.5).
type AuditEntry struct {
	ID        int64     `json:"id"`
	UserID    int64     `json:"user_id"`
	Action    string    `json:"action"`
	Target    string    `json:"target"`
	Detail    string    `json:"detail,omitempty"`
	IP        string    `json:"ip"`
	Timestamp time.Time `json:"ts"`
}

// AuditFilter narrows an audit listing.
type AuditFilter struct {
	InstanceID int64
	UserID     int64
	Action     string
	From       *time.Time
	To         *time.Time
	Limit      int
	Offset     int
}

// ---------------------------------------------------------------------------
// Store interfaces
// ---------------------------------------------------------------------------

// UserStore is the subset of the users repository this package consumes.
//
// Contract notes for the store implementer:
//   - GetByUsername must match case-insensitively (usernames are normalised
//     with auth.SanitizeUsername before every call).
//   - GetByUsername/GetByID return ErrNotFound (never a nil struct with a nil
//     error) when there is no row.
//   - Create must return ErrConflict on a UNIQUE violation for username.
//   - UpdatePassword is also the first-run bootstrap path: it sets
//     password_hash for a row whose hash was previously empty. It must be
//     atomic so that two concurrent setup calls cannot both "win" — the store
//     should only apply the update when the current hash is empty and report
//     ErrConflict otherwise (see SetInitialPassword).
//   - TouchLastLogin records last_login_at = now.
type UserStore interface {
	GetByUsername(ctx context.Context, username string) (*User, error)
	GetByID(ctx context.Context, id int64) (*User, error)
	List(ctx context.Context) ([]User, error)
	Create(ctx context.Context, u *User) error
	Update(ctx context.Context, u *User) error
	UpdatePassword(ctx context.Context, id int64, passwordHash string) error
	SetInitialPassword(ctx context.Context, id int64, passwordHash string) error
	TouchLastLogin(ctx context.Context, id int64, at time.Time) error
	Delete(ctx context.Context, id int64) error
	Count(ctx context.Context) (int, error)
}

// GrantStore persists instance-level grants (the user_instance_grant table).
//
// It is separate from UserStore because the two have genuinely different
// lifecycles: grants are the D3 extension slot, and a deployment that runs in
// single-user mode leaves this backend nil and stores nothing. Keeping it
// separate is also what lets the authorizer be the thing that *enforces* grants
// while this is the thing that *remembers* them.
//
// Contract notes:
//   - Grant is idempotent: re-granting a level a user already holds is a no-op,
//     not a duplicate-key error.
//   - Revoke clears every level for that (user, instance) pair and is
//     idempotent: revoking an absent grant is not an error.
//   - ListForUser returns an empty slice, never nil, when there are none.
type GrantStore interface {
	Grant(ctx context.Context, userID, instanceID int64, perm auth.GrantPerm) error
	Revoke(ctx context.Context, userID, instanceID int64) error
	ListForUser(ctx context.Context, userID int64) ([]InstanceGrant, error)
	ListForInstance(ctx context.Context, instanceID int64) ([]InstanceGrant, error)
}

// InstanceStore is the subset of the instances repository this package consumes.
//
// Contract notes:
//   - Create returns ErrConflict when the name is taken (UNIQUE on name).
//   - Delete removes the row and its instance_state row; it never touches the
//     filesystem (the handler owns directory removal so the safety check lives
//     in one place).
//   - ListUsedPorts returns the ports of all instances, used by the allocator.
type InstanceStore interface {
	List(ctx context.Context, f InstanceListFilter) ([]Instance, error)
	GetByID(ctx context.Context, id int64) (*Instance, error)
	GetByName(ctx context.Context, name string) (*Instance, error)
	Create(ctx context.Context, in *Instance) error
	Update(ctx context.Context, in *Instance) error
	Delete(ctx context.Context, id int64) error
	ListUsedPorts(ctx context.Context) ([]int, error)
	GetState(ctx context.Context, id int64) (*InstanceState, error)
	SaveState(ctx context.Context, st *InstanceState) error
}

// BackupStore is the subset of the backups repository this package consumes.
type BackupStore interface {
	List(ctx context.Context, f BackupFilter) ([]Backup, error)
	GetByID(ctx context.Context, id int64) (*Backup, error)
	Create(ctx context.Context, b *Backup) error
	Delete(ctx context.Context, id int64) error
}

// JobStore is the subset of the jobs repository this package consumes.
type JobStore interface {
	List(ctx context.Context) ([]Job, error)
	GetByID(ctx context.Context, id int64) (*Job, error)
	Create(ctx context.Context, j *Job) error
	Update(ctx context.Context, j *Job) error
	Delete(ctx context.Context, id int64) error
}

// AuditStore appends and queries audit_logs rows.
//
// Write must not fail the request when the audit insert fails: handlers call it
// best-effort and log the error, because losing an audit row is less bad than
// failing a successful operation. Implementations should therefore be fast and
// not require a transaction spanning the business operation.
type AuditStore interface {
	Write(ctx context.Context, e *AuditEntry) error
	List(ctx context.Context, f AuditFilter) ([]AuditEntry, error)
}

// SessionStore is the server-side half of the session model: a denylist of JWT
// IDs (jti) that have been logged out, plus an optional issued-at floor per
// user. It is an explicit design choice (see auth_handlers.go) that logout is
// enforced by the panel rather than by shortening the token TTL to minutes.
//
// Contract:
//   - Revoke must be idempotent.
//   - IsRevoked must return false for unknown jti values and must not error
//     for them.
//   - PurgeExpired may be called periodically; it drops entries past their
//     token expiry so the table cannot grow without bound.
type SessionStore interface {
	Revoke(ctx context.Context, jti string, userID int64, expiresAt time.Time) error
	IsRevoked(ctx context.Context, jti string) (bool, error)
	RevokeAllForUser(ctx context.Context, userID int64, revokedAt time.Time) error
	// RevokedBefore returns the user's revocation floor: every token issued at
	// or before this instant is invalid, regardless of its jti.
	//
	// A per-jti denylist alone cannot express "log out everywhere" or "the
	// password changed", because neither operation knows the jti of every live
	// token. The floor is how those two are enforced.
	//
	// It reports ok=false when the user has no floor. Implementations must not
	// return an error for an unknown user: absence of a floor is normal.
	RevokedBefore(userID int64) (time.Time, bool)
	PurgeExpired(ctx context.Context, now time.Time) error
}

// ---------------------------------------------------------------------------
// Feature-backend interfaces (config / world / files / logs / backups / jobs)
// ---------------------------------------------------------------------------

// ConfigKind identifies which configuration file a request targets.
type ConfigKind string

// Supported configuration files. These map onto the three writers described in
// §6.2: a strongly-typed ServerSetting.json, a key-preserving Settings.xml, and
// pass-through Configs/*.json.
const (
	ConfigKindServerSetting ConfigKind = "ServerSetting.json"
	ConfigKindSettings      ConfigKind = "Settings.xml"
	ConfigKindConfigs       ConfigKind = "Configs"
)

// ConfigDoc is a configuration file, returned as raw bytes plus a content type.
//
// Raw bytes rather than a parsed struct is the deliberate choice from §6.2:
// ServerSetting.json must round-trip unknown fields added by future server
// versions, and Configs/*.json is explicitly "pass-through editing" because
// those files are produced by closed-source plugins.
type ConfigDoc struct {
	// Kind is the logical file this document came from.
	Kind ConfigKind `json:"kind"`
	// Filename is the on-disk name, e.g. "ServerSetting.json" or
	// "Configs/BanConfig.json". Always relative to the instance directory.
	Filename string `json:"filename"`
	// Content is the verbatim file body.
	Content []byte `json:"-"`
	// RawJSON is the body as a JSON value when the file is JSON, so clients
	// can present a structured editor. It is nil for non-JSON documents.
	RawJSON map[string]any `json:"content,omitempty"`
	// Text is the body as a string for XML/text documents.
	Text string `json:"text,omitempty"`
	// ModTime is the file's last modification time.
	ModTime time.Time `json:"mod_time"`
	// RequiresRestart reports whether the last successful write to this file
	// needs an instance restart to take effect (§6.2: writes while running are
	// labelled "needs restart").
	RequiresRestart bool `json:"requires_restart"`
}

// ConfigUpdate is a write request for a configuration file.
type ConfigUpdate struct {
	Kind ConfigKind `json:"kind"`
	// Content is the exact new file body.
	Content []byte `json:"-"`
}

// ValidationIssue is a single problem found by a config or name validator.
type ValidationIssue struct {
	// Field is the offending key/path, e.g. "ServerPort" or
	// "WorldPath".
	Field string `json:"field"`
	// Code is a machine-readable identifier, e.g. "port_out_of_range".
	Code string `json:"code"`
	// Message is a human-readable explanation.
	Message string `json:"message"`
	// Severity is "error" (rejects the write) or "warning" (accepted).
	Severity string `json:"severity"`
}

// ValidationResult is the outcome of validating a config document.
type ValidationResult struct {
	Valid  bool              `json:"valid"`
	Issues []ValidationIssue `json:"issues"`
	// RequiresRestart reports whether applying the document needs a restart.
	RequiresRestart bool `json:"requires_restart"`
}

// ConfigService reads, validates and writes an instance's configuration files.
//
// Contract notes:
//   - Write MUST create a backup of the previous content before overwriting
//     (§6.2 "写前备份 .panel.bak"). The handler additionally validates before
//     calling Write, so a rejected document never reaches disk.
//   - Write MUST be atomic (temp file + rename) so a crash mid-write cannot
//     leave a truncated ServerSetting.json.
//   - Validate MUST NOT mutate anything.
//   - An unknown ConfigKind returns ErrNotFound.
type ConfigService interface {
	Get(ctx context.Context, inst *Instance, kind ConfigKind) (*ConfigDoc, error)
	Write(ctx context.Context, inst *Instance, upd ConfigUpdate) (*ConfigDoc, error)
	Validate(ctx context.Context, inst *Instance, kind ConfigKind, content []byte) (*ValidationResult, error)
}

// WorldInfo describes one directory under Worlds/.
type WorldInfo struct {
	// DirName is the directory name, which is the WorldPath suffix and the
	// authoritative identifier — the display name from Project.json is not
	// unique and must not be used to address a world (§2.7).
	DirName string `json:"dir_name"`
	// DisplayName comes from Project.json GameInfo.WorldName; it may be empty
	// or duplicated.
	DisplayName string `json:"display_name,omitempty"`
	// Guid is the SaveGame Guid from Project.json.
	Guid string `json:"guid,omitempty"`
	// GameMode as it appears inside the save (a *string*, unlike
	// ServerSetting.json's integer — §6.3.1 note 1).
	GameMode string `json:"game_mode,omitempty"`
	// MaxPlayers from Project.json.
	MaxPlayers int `json:"max_players,omitempty"`
	// SizeBytes is the on-disk footprint, including Regions/.
	SizeBytes int64 `json:"size_bytes"`
	// ModTime is the newest mtime found within the world directory.
	ModTime time.Time `json:"mod_time"`
	// Active reports whether this world is the one ServerSetting.json points
	// at via WorldPath.
	Active bool `json:"active"`
	// HasProjectJSON reports whether Project.json was found. A directory
	// without it is still listed (and flagged) rather than hidden.
	HasProjectJSON bool `json:"has_project_json"`
}

// WorldService manages the save games of an instance (§6.3).
//
// Contract notes:
//   - Activate rewrites ServerSetting.json's WorldPath (NOT WorldName) and
//     MUST refuse while the instance is running (the handler enforces this too).
//   - Delete must take a backup first and must refuse to delete the active
//     world.
//   - Import/Export handle zip archives; every archive entry must be validated
//     against zip-slip.
type WorldService interface {
	List(ctx context.Context, inst *Instance) ([]WorldInfo, error)
	Import(ctx context.Context, inst *Instance, name string, zipData []byte) (*WorldInfo, error)
	Export(ctx context.Context, inst *Instance, dirName string, includeRegions bool) ([]byte, string, error)
	Backup(ctx context.Context, inst *Instance, dirName, note string) (*Backup, error)
	Restore(ctx context.Context, inst *Instance, dirName string, backupID int64) error
	Activate(ctx context.Context, inst *Instance, dirName string) error
	Delete(ctx context.Context, inst *Instance, dirName string) error
}

// FileEntry is one directory entry in the file manager.
type FileEntry struct {
	Name string `json:"name"`
	// Path is the entry's path relative to the instance directory, using
	// forward slashes.
	Path    string    `json:"path"`
	IsDir   bool      `json:"is_dir"`
	Size    int64     `json:"size"`
	Mode    string    `json:"mode"`
	ModTime time.Time `json:"mod_time"`
	// SymlinkTarget is set when the entry is a symlink. The file manager
	// surfaces these explicitly because symlinks are the main path-traversal
	// vector; see ResolveInside.
	SymlinkTarget string `json:"symlink_target,omitempty"`
}

// FileService is the instance file manager (§6.4).
//
// Contract notes:
//   - EVERY path argument coming from a client MUST be funnelled through
//     ResolveInside before touching the filesystem. Implementations may assume
//     the handler already did this, but should not rely on it.
//   - Upload enforces a size cap and a type allowlist
//     (.zip/.dll/.scpak/.json/.xml/.txt).
//   - Unzip validates each entry against zip-slip.
type FileService interface {
	List(ctx context.Context, inst *Instance, rel string) ([]FileEntry, error)
	Read(ctx context.Context, inst *Instance, rel string) ([]byte, string, error)
	Write(ctx context.Context, inst *Instance, rel string, data []byte) error
	Mkdir(ctx context.Context, inst *Instance, rel string) error
	Rename(ctx context.Context, inst *Instance, from, to string) error
	Delete(ctx context.Context, inst *Instance, rel string) error
	Unzip(ctx context.Context, inst *Instance, rel, destRel string) error
	// SaveUpload streams an uploaded file into rel. The handler passes a
	// reader and a size hint; the service enforces the allowance itself.
	SaveUpload(ctx context.Context, inst *Instance, rel string, data []byte) (*FileEntry, error)
}

// LogService provides historical log access (§5.2, §5.2.1).
//
// Contract notes:
//   - Tail returns at most n lines, newest last. n <= 0 means "a sensible
//     default" (the implementation picks it).
//   - Grep filters lines by a regular expression. An invalid pattern is an
//     ErrInvalid, surfaced as 422 rather than an obscure 500.
//   - LogsPath is the file a download should serve.
type LogService interface {
	Tail(ctx context.Context, inst *Instance, n int, grep string) ([]string, error)
	LogsPath(ctx context.Context, inst *Instance) (string, error)
}

// BackupService creates and restores backups (§6.5).
type BackupService interface {
	Create(ctx context.Context, inst *Instance, kind, note string) (*Backup, error)
	Restore(ctx context.Context, inst *Instance, backupID int64) error
}

// JobService is the scheduler's control surface (§6.7).
type JobService interface {
	Validate(j *Job) (*ValidationResult, error)
	// Reschedule tells the scheduler to pick up a job change. A nil scheduler
	// means jobs are stored but not yet executed.
	Reschedule(ctx context.Context, j *Job) error
}

// SystemService reports host facts and the .NET runtime status (§7).
type SystemService interface {
	Info(ctx context.Context) (*SystemInfo, error)
}

// PortAllocator chooses a free UDP port for a new instance.
//
// The game speaks UDP (LiteNetLib), so TCP bind probes are useless here: a port
// can be TCP-free and still unusable, or vice versa. Every probe in this
// package therefore uses net.ListenPacket("udp", ...).
type PortAllocator interface {
	// Allocate returns a free UDP port from the configured pool, excluding
	// used ports. It returns ErrConflict when the pool is exhausted.
	Allocate(ctx context.Context, used []int) (int, error)
	// IsFree reports whether a UDP port is bindable.
	IsFree(port int) bool
}

// ---------------------------------------------------------------------------
// Reporters
// ---------------------------------------------------------------------------

// EventBroker fans state/alert events out to /ws/events subscribers (§5.6).
type EventBroker interface {
	// Publish delivers an event to all current subscribers. It must never
	// block on a slow subscriber.
	Publish(ev Event)
	// Subscribe registers a listener. The returned subscription must be closed
	// by the caller to release it.
	Subscribe() *EventSubscription
}

// LogBroadcaster fans instance log lines out to console subscribers.
type LogBroadcaster interface {
	// PublishLog delivers one log line to subscribers of instanceID.
	PublishLog(instanceID int64, line LogLine)
}

// ---------------------------------------------------------------------------
// Deps
// ---------------------------------------------------------------------------

// Deps is the dependency bundle passed to NewRouter.
//
// Every field is optional. A nil field is replaced by a nop implementation that
// returns ErrNotImplemented, so the server always starts and every route always
// responds with a meaningful status: 501 with code "not_implemented" rather
// than an empty 200.
//
// Callers that want the panel fully standalone can use NopDeps().
type Deps struct {
	// --- core (fully implemented in this build) ---
	Users    UserStore
	Tokens   *auth.TokenIssuer
	Sessions SessionStore
	// Grants persists instance-level grants. Optional: nil means the panel
	// runs in single-user mode and stores nothing (§5.5's "table stays
	// empty" rule). The Authorizer enforces grants; this remembers them.
	Grants GrantStore
	// RBAC is the role/permission + instance-grant authorizer. Never nil
	// after Normalize; single-user mode makes grant checks a no-op.
	RBAC *auth.Authorizer

	// Instances is required for every instance route.
	Instances InstanceStore
	// Process is the supervisor facade. NopProcessManager keeps lifecycle
	// endpoints working (against an in-memory state machine) with no game
	// server installed.
	Process ProcessManager
	// Ports allocates UDP ports.
	Ports PortAllocator
	// Audit records mutating operations.
	Audit AuditStore

	// --- feature backends (delegated; nop by default) ---
	Config  ConfigService
	World   WorldService
	Files   FileService
	Logs    LogService
	Backups BackupStore
	Backup  BackupService
	Jobs    JobStore
	Job     JobService
	System  SystemService

	// --- host wiring ---
	// Events receives state/alert events for /ws/events.
	Events EventBroker
	// LogsHub receives console log lines for /ws/instances/:id/console. When
	// nil, the console falls back to polling Process.Subscribe per connection.
	LogsHub LogBroadcaster

	// InstancesDir is the absolute root that instance directories live under.
	// It is used by the delete handler for the "is this dir really inside
	// instances_dir?" safety check, and by ResolveInside callers.
	InstancesDir string

	// Log is the structured logger. Defaults to a slog logger writing JSON to
	// stderr.
	Log Logger

	// Config holds HTTP-visible knobs.
	ConfigHTTP HTTPConfig

	// Version reports build information for /system/info and the health route.
	Version VersionInfo
}

// VersionInfo is the subset of internal/version.Info the API exposes. It is
// duplicated here so this package does not import internal/version (which is
// owned by another agent and may be mid-edit).
type VersionInfo struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	BuildTime string `json:"build_time"`
	GoVersion string `json:"go_version"`
	Platform  string `json:"platform"`
}

// HTTPConfig holds the tunables of the HTTP layer.
type HTTPConfig struct {
	// Login rate limit: attempts allowed per window per client IP.
	LoginRateLimit  int
	LoginRateWindow time.Duration
	// Login lockout: consecutive failures before the account is locked.
	LoginMaxFailures int
	// LoginLockoutFor is how long a locked account stays locked.
	LoginLockoutFor time.Duration
	// CommandRateLimit caps console command dispatches per second per user.
	CommandRateLimit  int
	CommandRateWindow time.Duration
	// UploadRateLimit caps file uploads per minute per user.
	UploadRateLimit  int
	UploadRateWindow time.Duration
	// MaxUploadBytes caps a single upload body.
	MaxUploadBytes int64
	// AllowedOrigins for WebSocket upgrades. Empty means "same-origin only"
	// (the Origin header, when present, must match the Host).
	AllowedOrigins []string
	// CORSEnabled turns on the CORS middleware. It is off by default: the
	// panel serves its own frontend from the same origin (§5.7 hardening).
	CORSEnabled bool
	// MaxBodyBytes caps JSON request bodies.
	MaxBodyBytes int64
	// WSReadLimit caps an inbound WebSocket message (console command).
	WSReadLimit int64
	// WSWriteTimeout bounds a single WebSocket write.
	WSWriteTimeout time.Duration
	// WSPingInterval is the server-side keepalive period.
	WSPingInterval time.Duration
	// ShutdownGrace is how long in-flight requests get on shutdown.
	ShutdownGrace time.Duration
}

// DefaultHTTPConfig returns the shipped defaults.
func DefaultHTTPConfig() HTTPConfig {
	return HTTPConfig{
		LoginRateLimit:    10,
		LoginRateWindow:   time.Minute,
		LoginMaxFailures:  5,
		LoginLockoutFor:   15 * time.Minute,
		CommandRateLimit:  20,
		CommandRateWindow: time.Second,
		UploadRateLimit:   30,
		UploadRateWindow:  time.Minute,
		MaxUploadBytes:    512 << 20, // 512 MiB: Content.scpak is ~19 MiB but mods are bigger
		MaxBodyBytes:      8 << 20,
		WSReadLimit:       4 << 10,
		WSWriteTimeout:    10 * time.Second,
		WSPingInterval:    30 * time.Second,
		ShutdownGrace:     15 * time.Second,
	}
}

func (c HTTPConfig) withDefaults() HTTPConfig {
	d := DefaultHTTPConfig()
	if c.LoginRateLimit <= 0 {
		c.LoginRateLimit = d.LoginRateLimit
	}
	if c.LoginRateWindow <= 0 {
		c.LoginRateWindow = d.LoginRateWindow
	}
	if c.LoginMaxFailures <= 0 {
		c.LoginMaxFailures = d.LoginMaxFailures
	}
	if c.LoginLockoutFor <= 0 {
		c.LoginLockoutFor = d.LoginLockoutFor
	}
	if c.CommandRateLimit <= 0 {
		c.CommandRateLimit = d.CommandRateLimit
	}
	if c.CommandRateWindow <= 0 {
		c.CommandRateWindow = d.CommandRateWindow
	}
	if c.UploadRateLimit <= 0 {
		c.UploadRateLimit = d.UploadRateLimit
	}
	if c.UploadRateWindow <= 0 {
		c.UploadRateWindow = d.UploadRateWindow
	}
	if c.MaxUploadBytes <= 0 {
		c.MaxUploadBytes = d.MaxUploadBytes
	}
	if c.MaxBodyBytes <= 0 {
		c.MaxBodyBytes = d.MaxBodyBytes
	}
	if c.WSReadLimit <= 0 {
		c.WSReadLimit = d.WSReadLimit
	}
	if c.WSWriteTimeout <= 0 {
		c.WSWriteTimeout = d.WSWriteTimeout
	}
	if c.WSPingInterval <= 0 {
		c.WSPingInterval = d.WSPingInterval
	}
	if c.ShutdownGrace <= 0 {
		c.ShutdownGrace = d.ShutdownGrace
	}
	return c
}

// Normalize fills nil Deps fields with nop implementations and applies default
// HTTP config. It returns the normalized copy; the receiver is untouched.
//
// It returns an error only for a missing hard requirement (Tokens, which
// cannot be invented because forging-free tokens need a real secret).
func (d Deps) Normalize() (Deps, error) {
	d.ConfigHTTP = d.ConfigHTTP.withDefaults()

	if d.Log == nil {
		d.Log = NewSlogLogger(nil)
	}
	if d.Users == nil {
		d.Users = nopUserStore{}
	}
	if d.Instances == nil {
		d.Instances = nopInstanceStore{}
	}
	if d.Audit == nil {
		d.Audit = nopAuditStore{}
	}
	if d.Backups == nil {
		d.Backups = nopBackupStore{}
	}
	if d.Jobs == nil {
		d.Jobs = nopJobStore{}
	}
	if d.Config == nil {
		d.Config = nopConfigService{}
	}
	if d.World == nil {
		d.World = nopWorldService{}
	}
	if d.Files == nil {
		d.Files = nopFileService{}
	}
	if d.Logs == nil {
		d.Logs = nopLogService{}
	}
	if d.Backup == nil {
		d.Backup = nopBackupService{}
	}
	if d.Job == nil {
		d.Job = nopJobService{}
	}
	if d.System == nil {
		d.System = NewHostSystemService()
	}
	if d.Ports == nil {
		d.Ports = NewUDPPortAllocator(PortPool{Start: DefaultPortPoolStart, End: DefaultPortPoolEnd})
	}
	if d.Process == nil {
		d.Process = NewNopProcessManager(d.Log)
	}
	if d.Sessions == nil {
		d.Sessions = NewMemorySessionStore()
	}
	if d.Events == nil {
		d.Events = NewEventHub(d.Log)
	}
	if d.RBAC == nil {
		d.RBAC = auth.NewAuthorizer(true)
	}
	if d.Tokens == nil {
		return d, errors.New("api: Deps.Tokens is required (build one with auth.NewTokenIssuer)")
	}
	return d, nil
}
