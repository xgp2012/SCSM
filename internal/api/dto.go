package api

import (
	"time"

	"scnetm/internal/auth"
)

// This file defines the request and response payloads of the /api/v1 surface.
//
// Conventions:
//   - Request types are named <Verb><Thing>Request and are bound from JSON.
//   - Response types are named <Thing>Response; they never embed a store model
//     directly, so adding a column to the schema cannot silently change the
//     public API.
//   - Optional fields use pointers when "absent" differs from "zero" (notably
//     the stop request's force flag, where an absent body must be legal).
//   - Timestamps are RFC3339 UTC.

// ---------------------------------------------------------------------------
// Envelope
// ---------------------------------------------------------------------------

// DataResponse wraps a single object.
type DataResponse struct {
	Data any `json:"data"`
}

// ListResponse wraps a collection, always including a total so the frontend can
// paginate without a second request.
type ListResponse struct {
	Data  any `json:"data"`
	Total int `json:"total"`
}

// OKResponse is the response for an operation with no payload.
type OKResponse struct {
	OK bool `json:"ok"`
}

// ---------------------------------------------------------------------------
// Auth
// ---------------------------------------------------------------------------

// LoginRequest is the body of POST /auth/login.
type LoginRequest struct {
	Username string `json:"username" binding:"required"`
	Password string `json:"password" binding:"required"`
	// RememberMe requests a longer-lived token. It only extends the access
	// token TTL; it never bypasses the denylist.
	RememberMe bool `json:"remember_me,omitempty"`
}

// LoginResponse is returned on success.
type LoginResponse struct {
	// Token is the HS256 JWT to send as `Authorization: Bearer <token>`.
	Token string `json:"token"`
	// TokenType is always "Bearer".
	TokenType string `json:"token_type"`
	// ExpiresIn is the token lifetime in seconds.
	ExpiresIn int64 `json:"expires_in"`
	// ExpiresAt is the absolute expiry, so the UI can schedule a refresh.
	ExpiresAt time.Time `json:"expires_at"`
	// User is the authenticated principal.
	User UserResponse `json:"user"`
}

// SetupRequest is the body of POST /auth/setup (first-run bootstrap).
type SetupRequest struct {
	// Password is the administrator password to set. It must satisfy the
	// panel policy (>= auth.MinPasswordLength chars, not in the common list).
	Password string `json:"password" binding:"required"`
	// ConfirmPassword, when supplied, must equal Password. It exists so a
	// typo cannot lock the operator out of a fresh panel.
	ConfirmPassword string `json:"confirm_password,omitempty"`
	// Username optionally renames the administrator account. Empty keeps the
	// seeded name.
	Username string `json:"username,omitempty"`
}

// SetupStatusResponse is returned by GET /auth/setup-required.
type SetupStatusResponse struct {
	// Required is true when no user has a password set yet.
	Required bool `json:"setup_required"`
	// Reason explains why, so the login screen can say something useful.
	Reason string `json:"reason,omitempty"`
	// Username is the account that will receive the password.
	Username string `json:"username,omitempty"`
	// MinPasswordLength mirrors the server policy for client-side hints.
	MinPasswordLength int `json:"min_password_length"`
}

// UserResponse is the public projection of a user. The password hash is never
// included; User has `json:"-"` on that field and this type re-declares only
// the safe fields.
type UserResponse struct {
	ID          int64      `json:"id"`
	Username    string     `json:"username"`
	Role        auth.Role  `json:"role"`
	CreatedAt   time.Time  `json:"created_at"`
	LastLoginAt *time.Time `json:"last_login_at,omitempty"`
	Disabled    bool       `json:"disabled"`
	HasPassword bool       `json:"has_password"`
	// Permissions lists the resolved permission strings, so the UI can hide
	// controls without duplicating the RBAC table.
	Permissions []string `json:"permissions,omitempty"`
}

// MeResponse is returned by GET /auth/me.
type MeResponse struct {
	User UserResponse `json:"user"`
	// InstanceGrants is the caller's instance-level grants. Always empty in
	// single-user mode, but present so the frontend's shape does not change
	// when multi-user is enabled.
	InstanceGrants []InstanceGrant `json:"instance_grants"`
	// SingleUser reports the panel's current mode.
	SingleUser bool `json:"single_user"`
}

// LogoutRequest is the optional body of POST /auth/logout.
type LogoutRequest struct {
	// AllSessions revokes every token for the user, not just the current one.
	AllSessions bool `json:"all_sessions,omitempty"`
}

// ---------------------------------------------------------------------------
// Instances
// ---------------------------------------------------------------------------

// CreateInstanceRequest is the body of POST /instances.
type CreateInstanceRequest struct {
	// Name is the instance name; it must be unique and filesystem-safe,
	// because it becomes the directory name under instances_dir.
	//
	// There is deliberately no `binding:"required"` tag: an empty name is
	// validated by ValidateInstanceName alongside every other name rule, so the
	// API answers 422 validation_failed consistently instead of letting Gin's
	// binder produce a bare 400 for this one field.
	Name string `json:"name"`
	// WorldName is the display name of the initial world.
	WorldName string `json:"world_name,omitempty"`
	// WorldSeed is the raw seed string. Only WorldSeedString is written; the
	// derived integer WorldSeed stays read-only (§6.3.1 note 2).
	WorldSeed string `json:"world_seed,omitempty"`
	// MaxPlayers is the initial MaxOnlinePlayerCount.
	MaxPlayers int `json:"max_players,omitempty"`
	// GameMode is the numeric ServerSetting.json game mode (0-6). It is
	// converted to the in-save string enum only when writing Project.json.
	GameMode *int `json:"game_mode,omitempty"`
	// Port is the UDP port. 0 means "allocate from the pool".
	Port int `json:"port,omitempty"`
	// Dir overrides the instance directory name. It must stay inside
	// instances_dir; the handler resolves and verifies it.
	Dir string `json:"dir,omitempty"`
	// Memo is a free-form note.
	Memo string `json:"memo,omitempty"`
	// AutoStart starts the instance immediately after creation.
	AutoStart bool `json:"auto_start,omitempty"`
	// AutoRestart enables crash auto-restart.
	AutoRestart bool `json:"auto_restart,omitempty"`
	// MaxRestart caps automatic restarts.
	MaxRestart int `json:"max_restart,omitempty"`
	// StopTimeoutSec overrides the graceful stop timeout.
	StopTimeoutSec int `json:"stop_timeout_sec,omitempty"`
	// ServerJar is the server assembly to run, e.g. "Survivalcraft.dll". The
	// current official package ships no native launcher, so the panel must
	// invoke `dotnet <ServerJar>` (§2.1). An instance created without it cannot
	// be started, so it is recorded here at creation time rather than only in
	// the process layer.
	ServerJar string `json:"server_jar,omitempty"`
	// DotnetPath overrides the dotnet executable for this instance. Empty means
	// "use the panel's configured dotnet_path" (default "dotnet").
	DotnetPath string `json:"dotnet_path,omitempty"`
	// WorldPassword is the optional in-game password.
	WorldPassword string `json:"world_password,omitempty"`
}

// InstanceResponse merges the stored row with live supervisor state.
type InstanceResponse struct {
	Instance
	// State is the live state, merged from ProcessManager. It falls back to
	// the persisted snapshot when the supervisor has no opinion.
	State string `json:"state"`
	// StateDetail carries the extra live fields (pid, uptime, readiness).
	StateDetail InstanceStateResponse `json:"state_detail"`
	// Online is a convenience flag: true when the instance is Running.
	Online bool `json:"online"`
	// PortInUse reports whether the instance's UDP port is currently bound by
	// anything. It lets the UI warn before a start attempt.
	PortInUse bool `json:"port_in_use"`
}

// InstanceStateResponse is the live-state projection.
type InstanceStateResponse struct {
	PID           int        `json:"pid,omitempty"`
	Ready         bool       `json:"ready"`
	StartedAt     *time.Time `json:"started_at,omitempty"`
	StoppedAt     *time.Time `json:"stopped_at,omitempty"`
	ExitCode      *int       `json:"exit_code,omitempty"`
	Restarts      int        `json:"restarts"`
	UptimeSeconds int64      `json:"uptime_seconds"`
	OnlinePlayers int        `json:"online_players"`
	LastError     string     `json:"last_error,omitempty"`
	// StateSource is "supervisor" when the value came from live state, or
	// "snapshot" when the supervisor had no entry and the persisted row was
	// used instead.
	StateSource string `json:"state_source"`
}

// ListInstancesResponse is the body of GET /instances.
type ListInstancesResponse struct {
	Data  []InstanceResponse `json:"data"`
	Total int                `json:"total"`
	// Summary aggregates counts for a dashboard header.
	Summary InstanceSummary `json:"summary"`
}

// InstanceSummary counts instances by state.
type InstanceSummary struct {
	Total   int `json:"total"`
	Running int `json:"running"`
	Stopped int `json:"stopped"`
	Crashed int `json:"crashed"`
	Other   int `json:"other"`
}

// DeleteInstanceRequest is the body of DELETE /instances/:id.
//
// A body on DELETE is unusual but justified: deleting the directory is
// destructive, so it must be an explicit opt-in that a query string cannot
// trigger by accident (and it is audited separately).
type DeleteInstanceRequest struct {
	// DeleteDir removes the instance's working directory. Without it the row
	// is deleted and the files are left on disk.
	DeleteDir bool `json:"delete_dir,omitempty"`
	// ConfirmName must equal the instance name when DeleteDir is true. This is
	// the "二次确认" of §6.3 applied to the most destructive operation in the
	// panel.
	ConfirmName string `json:"confirm_name,omitempty"`
}

// StopInstanceRequest is the body of POST /instances/:id/stop.
//
// The body is optional: an empty body means a graceful stop. Force is a
// pointer so "absent" is distinguishable from "false" when validating.
type StopInstanceRequest struct {
	// Force skips the graceful path and kills the process tree.
	Force bool `json:"force,omitempty"`
	// TimeoutSec overrides the instance's configured stop timeout.
	TimeoutSec int `json:"timeout_sec,omitempty"`
}

// StopInstanceResponse reports the outcome.
type StopInstanceResponse struct {
	InstanceID int64  `json:"instance_id"`
	Forced     bool   `json:"forced"`
	Exited     bool   `json:"exited"`
	ExitCode   *int   `json:"exit_code,omitempty"`
	DurationMS int64  `json:"duration_ms"`
	State      string `json:"state"`
}

// StatsResponse is the body of GET /instances/:id/stats (§6.6).
type StatsResponse struct {
	InstanceID int64       `json:"instance_id"`
	State      string      `json:"state"`
	Metrics    MetricsInfo `json:"metrics"`
	// PortListening is the UDP bind probe result: true when the port is taken,
	// which for a running instance is the expected outcome.
	PortListening bool `json:"port_listening"`
	Port          int  `json:"port"`
	// OnlinePlayers is the last parsed count, or -1 when the command channel
	// that would provide it is unavailable.
	OnlinePlayers int `json:"online_players"`
	// OnlinePlayersSource explains how the count was obtained.
	OnlinePlayersSource string `json:"online_players_source"`
}

// PlayersResponse is the body of GET /instances/:id/players.
type PlayersResponse struct {
	InstanceID int64 `json:"instance_id"`
	// Players is the parsed player list. Empty with Available=false means
	// "unknown", not "nobody online".
	Players []PlayerInfo `json:"players"`
	// Count is len(Players); kept explicit for the empty case.
	Count int `json:"count"`
	// Available reports whether the player list could be obtained. It depends
	// on the command channel (V0-1), so it is false on a build without a
	// supervisor.
	Available bool `json:"available"`
	// Note explains an unavailable result in operator terms.
	Note string `json:"note,omitempty"`
}

// PlayerInfo is one online player.
type PlayerInfo struct {
	Name string `json:"name"`
	// IP is masked in the response by default for privacy.
	IP string `json:"ip,omitempty"`
	// Ping is the round-trip time in ms when known.
	Ping int `json:"ping,omitempty"`
}

// ---------------------------------------------------------------------------
// Config
// ---------------------------------------------------------------------------

// ConfigQuery names the configuration file to operate on.
type ConfigQuery struct {
	// Kind is one of ServerSetting.json / Settings.xml / Configs.
	Kind string `form:"kind"`
	// File selects a specific file inside Configs/ when Kind is "Configs".
	File string `form:"file"`
}

// ConfigResponse is the body of GET/PUT /instances/:id/config.
type ConfigResponse struct {
	Kind     string `json:"kind"`
	Filename string `json:"filename"`
	// Content is the parsed JSON object when the document is JSON; null
	// otherwise.
	Content map[string]any `json:"content,omitempty"`
	// Text is the verbatim body for XML/text documents.
	Text string `json:"text,omitempty"`
	// ModTime is the file's last modification time.
	ModTime time.Time `json:"mod_time"`
	// RequiresRestart reports whether the last write needs a restart (§6.2).
	RequiresRestart bool `json:"requires_restart"`
	// BackupPath, on a write response, is the backup the panel took first.
	BackupPath string `json:"backup_path,omitempty"`
	// Validated is the validation result of the document just written.
	Validated *ValidationResult `json:"validated,omitempty"`
	// RestartNeeded is set on a write that changed a value which only takes
	// effect after a restart while the instance is running.
	RestartNeeded bool `json:"restart_needed"`
}

// UpdateConfigRequest is the body of PUT /instances/:id/config.
type UpdateConfigRequest struct {
	// Kind defaults to ServerSetting.json.
	Kind string `json:"kind,omitempty"`
	// File selects a specific Configs/ file.
	File string `json:"file,omitempty"`
	// Content is the new document. JSON documents may be sent either as a
	// structured object (JSONContent) or as text (Text); exactly one must be
	// present.
	JSONContent map[string]any `json:"content,omitempty"`
	// Text is the raw body for XML/text documents.
	Text *string `json:"text,omitempty"`
	// Force writes even when validation produces warnings (never when it
	// produces errors).
	Force bool `json:"force,omitempty"`
	// Restart restarts the instance after a successful write.
	Restart bool `json:"restart,omitempty"`
}

// ValidateConfigRequest is the body of POST /instances/:id/config/validate.
type ValidateConfigRequest struct {
	Kind        string         `json:"kind,omitempty"`
	File        string         `json:"file,omitempty"`
	JSONContent map[string]any `json:"content,omitempty"`
	Text        *string        `json:"text,omitempty"`
}

// ---------------------------------------------------------------------------
// Worlds
// ---------------------------------------------------------------------------

// WorldResponse is one entry of GET /instances/:id/worlds.
type WorldResponse struct {
	WorldInfo
	// SizeHuman is a preformatted size for the UI.
	SizeHuman string `json:"size_human"`
}

// WorldListResponse is the body of GET /instances/:id/worlds.
type WorldListResponse struct {
	Data  []WorldResponse `json:"data"`
	Total int             `json:"total"`
	// ActiveDir is the world ServerSetting.json currently points at.
	ActiveDir string `json:"active_dir,omitempty"`
}

// ActivateWorldRequest is the body of POST /instances/:id/worlds/:w/activate.
type ActivateWorldRequest struct {
	// Confirm acknowledges the restart requirement. Activation is refused on a
	// running instance (§6.3), so the flag documents operator intent.
	Confirm bool `json:"confirm,omitempty"`
}

// WorldBackupRequest is the body of POST /instances/:id/worlds/:w/backup.
type WorldBackupRequest struct {
	Note string `json:"note,omitempty"`
}

// WorldRestoreRequest is the body of POST /instances/:id/worlds/:w/restore.
type WorldRestoreRequest struct {
	// BackupID selects the backup to restore.
	BackupID int64 `json:"backup_id" binding:"required"`
	// Confirm acknowledges that the current world content will be replaced.
	Confirm bool `json:"confirm,omitempty"`
}

// ---------------------------------------------------------------------------
// Files
// ---------------------------------------------------------------------------

// FileListResponse is the body of GET /instances/:id/files.
type FileListResponse struct {
	// Path is the resolved relative directory being listed (normalised, so the
	// UI breadcrumb is always safe to render).
	Path string `json:"path"`
	// Parent is the parent path, empty at the instance root.
	Parent string `json:"parent,omitempty"`
	// Entries are the directory's children, directories first.
	Entries []FileEntry `json:"entries"`
	// Truncated reports that a page limit was applied.
	Truncated bool `json:"truncated"`
}

// FileMkdirRequest is the body of POST /instances/:id/files/mkdir.
type FileMkdirRequest struct {
	Path string `json:"path" binding:"required"`
	// Mode is an optional octal permission string, e.g. "0755".
	Mode string `json:"mode,omitempty"`
}

// FileRenameRequest is the body of POST /instances/:id/files/rename.
type FileRenameRequest struct {
	From string `json:"from" binding:"required"`
	To   string `json:"to" binding:"required"`
}

// FileDeleteRequest is the body of POST /instances/:id/files/delete.
type FileDeleteRequest struct {
	Path string `json:"path" binding:"required"`
	// Recursive allows removing a non-empty directory.
	Recursive bool `json:"recursive,omitempty"`
}

// FileUnzipRequest is the body of POST /instances/:id/files/unzip.
type FileUnzipRequest struct {
	// Path is the zip archive, relative to the instance directory.
	Path string `json:"path" binding:"required"`
	// Dest is the extraction directory. Empty means "next to the archive".
	Dest string `json:"dest,omitempty"`
	// Overwrite allows replacing existing files.
	Overwrite bool `json:"overwrite,omitempty"`
}

// FileUploadResponse is returned by POST /instances/:id/files/upload.
type FileUploadResponse struct {
	Entry FileEntry `json:"entry"`
	// Bytes is the stored size.
	Bytes int64 `json:"bytes"`
	// AllowlistWarnings lists non-fatal notes about the upload.
	AllowlistWarnings []string `json:"warnings,omitempty"`
}

// ---------------------------------------------------------------------------
// Logs
// ---------------------------------------------------------------------------

// LogsQuery holds the query parameters of GET /instances/:id/logs.
type LogsQuery struct {
	// Tail is the number of trailing lines to return. 0 means the default.
	Tail int `form:"tail"`
	// Grep is a regular expression filter.
	Grep string `form:"grep"`
	// Stream selects stdout/stderr/both.
	Stream string `form:"stream"`
	// Format is "text" (default) or "json".
	Format string `form:"format"`
}

// LogsResponse is the body of GET /instances/:id/logs.
type LogsResponse struct {
	InstanceID int64 `json:"instance_id"`
	// Lines are the matched lines, oldest first.
	Lines []string `json:"lines"`
	// Count is len(Lines).
	Count int `json:"count"`
	// Tail echoes the effective tail size, so the UI can say "last N lines".
	Tail int `json:"tail"`
	// Grep echoes the applied filter.
	Grep string `json:"grep,omitempty"`
	// Truncated reports that more lines matched than were returned.
	Truncated bool `json:"truncated"`
}

// ---------------------------------------------------------------------------
// Backups
// ---------------------------------------------------------------------------

// CreateBackupRequest is the body of POST /instances/:id/backups.
type CreateBackupRequest struct {
	// Kind is manual/scheduled/pre-start. Empty means "manual".
	Kind string `json:"kind,omitempty"`
	Note string `json:"note,omitempty"`
}

// BackupListResponse is the body of GET /backups.
type BackupListResponse struct {
	Data  []Backup `json:"data"`
	Total int      `json:"total"`
}

// ---------------------------------------------------------------------------
// Jobs
// ---------------------------------------------------------------------------

// JobRequest is the body of POST /jobs and PUT /jobs/:id.
type JobRequest struct {
	// InstanceID scopes the job; 0 means panel-wide.
	InstanceID int64 `json:"instance_id"`
	// Type is backup/restart/command/announce.
	Type string `json:"type" binding:"required"`
	// Cron is a five-field cron expression.
	Cron string `json:"cron" binding:"required"`
	// Payload is the type-specific JSON payload.
	Payload string `json:"payload,omitempty"`
	// Enabled toggles execution.
	Enabled bool `json:"enabled"`
}

// JobListResponse is the body of GET /jobs.
type JobListResponse struct {
	Data  []Job `json:"data"`
	Total int   `json:"total"`
	// SchedulerAvailable reports whether jobs will actually run. When false
	// the UI must say "stored but not executed" rather than implying success.
	SchedulerAvailable bool `json:"scheduler_available"`
}

// ---------------------------------------------------------------------------
// Users
// ---------------------------------------------------------------------------

// CreateUserRequest is the body of POST /users.
type CreateUserRequest struct {
	Username string         `json:"username" binding:"required"`
	Password string         `json:"password" binding:"required"`
	Role     string         `json:"role" binding:"required"`
	Disabled bool           `json:"disabled,omitempty"`
	Grants   []GrantRequest `json:"grants,omitempty"`
}

// GrantRequest is one instance-level grant.
type GrantRequest struct {
	InstanceID int64  `json:"instance_id" binding:"required"`
	Perm       string `json:"perm" binding:"required"`
}

// UpdateUserRequest is the body of PUT /users/:id. Every field is optional; a
// pointer distinguishes "leave alone" from "set to zero".
type UpdateUserRequest struct {
	Username *string        `json:"username,omitempty"`
	Password *string        `json:"password,omitempty"`
	Role     *string        `json:"role,omitempty"`
	Disabled *bool          `json:"disabled,omitempty"`
	Grants   []GrantRequest `json:"grants,omitempty"`
}

// UserListResponse is the body of GET /users.
type UserListResponse struct {
	Data  []UserResponse `json:"data"`
	Total int            `json:"total"`
}

// ---------------------------------------------------------------------------
// Audit
// ---------------------------------------------------------------------------

// AuditQuery holds the query parameters of GET /audit.
type AuditQuery struct {
	InstanceID int64  `form:"instance_id"`
	UserID     int64  `form:"user_id"`
	Action     string `form:"action"`
	From       string `form:"from"`
	To         string `form:"to"`
	Limit      int    `form:"limit"`
	Offset     int    `form:"offset"`
}

// AuditListResponse is the body of GET /audit.
type AuditListResponse struct {
	Data   []AuditEntry `json:"data"`
	Total  int          `json:"total"`
	Limit  int          `json:"limit"`
	Offset int          `json:"offset"`
}

// ---------------------------------------------------------------------------
// System
// ---------------------------------------------------------------------------

// SystemInfoResponse is the body of GET /system/info.
type SystemInfoResponse struct {
	// Version is the panel build.
	Version VersionInfo `json:"version"`
	// Host describes the machine.
	Host HostInfo `json:"host"`
	// Dotnet is the .NET runtime check — the plan's number one deployment
	// precondition (§7). It is always present and never causes a 5xx: a
	// missing runtime is reported as Available=false with an actionable hint.
	Dotnet DotnetInfo `json:"dotnet"`
	// Storage describes the instances root's filesystem.
	Storage StorageInfo `json:"storage"`
	// Panel reports runtime facts about the panel itself.
	Panel PanelInfo `json:"panel"`
}

// HostInfo describes the host machine.
type HostInfo struct {
	Hostname    string  `json:"hostname"`
	OS          string  `json:"os"`
	Arch        string  `json:"arch"`
	Kernel      string  `json:"kernel,omitempty"`
	CPUCores    int     `json:"cpu_cores"`
	GoVersion   string  `json:"go_version"`
	UptimeSec   int64   `json:"uptime_seconds,omitempty"`
	LoadAvg1    float64 `json:"load_avg_1,omitempty"`
	LoadAvg5    float64 `json:"load_avg_5,omitempty"`
	LoadAvg15   float64 `json:"load_avg_15,omitempty"`
	MemoryTotal uint64  `json:"memory_total_bytes,omitempty"`
	MemoryUsed  uint64  `json:"memory_used_bytes,omitempty"`
	MemoryAvail uint64  `json:"memory_available_bytes,omitempty"`
}

// DotnetInfo reports the .NET runtime detection result.
type DotnetInfo struct {
	// Available is true when a Microsoft.NETCore.App 10.x runtime was found.
	Available bool `json:"available"`
	// DotnetPath is the resolved dotnet executable path, if any.
	DotnetPath string `json:"dotnet_path,omitempty"`
	// Version is the newest matching runtime version string.
	Version string `json:"version,omitempty"`
	// Runtimes lists every runtime reported by `dotnet --list-runtimes`.
	//
	// No omitempty: the field must always be present as an array so a client
	// can iterate it without a null check. With omitempty an empty list
	// vanishes from the payload entirely, which is the same trap as emitting
	// null.
	Runtimes []string `json:"runtimes"`
	// RequiredMajor is the major version the panel needs (10).
	RequiredMajor int `json:"required_major"`
	// Error explains why detection failed (usually "dotnet not found in PATH").
	// Its presence is NOT an HTTP error: the endpoint still returns 200.
	Error string `json:"error,omitempty"`
	// ExitCode is the probe process's exit status when it ran but failed. It
	// distinguishes "not installed" (ExitCode is nil) from "installed but
	// broken" (non-zero), which are very different operator problems.
	ExitCode *int `json:"exit_code,omitempty"`
	// Hint is an operator-facing remediation suggestion.
	Hint string `json:"hint,omitempty"`
	// TemplateDir is the server template directory, if one is configured and
	// exists. Without it instances cannot be fully provisioned (§6.1).
	TemplateDir string `json:"template_dir,omitempty"`
	// TemplateAvailable reports whether the template directory exists.
	TemplateAvailable bool `json:"template_available"`
}

// StorageInfo describes disk usage of the instances root.
type StorageInfo struct {
	Path       string `json:"path"`
	TotalBytes uint64 `json:"total_bytes"`
	FreeBytes  uint64 `json:"free_bytes"`
	UsedBytes  uint64 `json:"used_bytes"`
	// Writable reports whether the panel can create files there.
	Writable bool `json:"writable"`
	// Error explains a failed stat/statfs.
	Error string `json:"error,omitempty"`
}

// PanelInfo reports panel runtime facts.
type PanelInfo struct {
	UptimeSec     int64  `json:"uptime_seconds"`
	Goroutines    int    `json:"goroutines"`
	HeapAlloc     uint64 `json:"heap_alloc_bytes"`
	Sys           uint64 `json:"sys_bytes"`
	Instances     int    `json:"instances"`
	RunningCount  int    `json:"running_instances"`
	Users         int    `json:"users"`
	SingleUser    bool   `json:"single_user"`
	SetupRequired bool   `json:"setup_required"`
}

// HealthResponse is the body of GET /healthz.
type HealthResponse struct {
	Status  string `json:"status"`
	Uptime  int64  `json:"uptime_seconds"`
	Version string `json:"version"`
}

// ---------------------------------------------------------------------------
// WebSocket
// ---------------------------------------------------------------------------

// WSCommand is a client → server console message.
type WSCommand struct {
	// Type is "command" | "ping" | "resize" | "subscribe".
	Type string `json:"type"`
	// Line is the command text for Type == "command".
	Line string `json:"line,omitempty"`
	// Cols/Rows carry a terminal resize for Type == "resize".
	Cols int `json:"cols,omitempty"`
	Rows int `json:"rows,omitempty"`
}

// WSMessage is a server → client console message.
type WSMessage struct {
	// Type is "log" | "status" | "error" | "pong" | "hello".
	Type string `json:"type"`
	// InstanceID scopes the message.
	InstanceID int64 `json:"instance_id,omitempty"`
	// Seq is the monotonically increasing line number for Type == "log".
	Seq uint64 `json:"seq,omitempty"`
	// TS is the event time.
	TS time.Time `json:"ts"`
	// Text is the log line or status text.
	Text string `json:"text,omitempty"`
	// Stream is stdout/stderr for log messages.
	Stream string `json:"stream,omitempty"`
	// State is the instance state for Type == "status".
	State string `json:"state,omitempty"`
	// Code is a machine-readable error code for Type == "error".
	Code string `json:"code,omitempty"`
	// Message is a human-readable error message.
	Message string `json:"message,omitempty"`
}

// WSClient message types.
const (
	// WSMsgCommand sends a console command.
	WSMsgCommand = "command"
	// WSMsgPing is a client keepalive.
	WSMsgPing = "ping"
	// WSMsgResize carries a PTY resize.
	WSMsgResize = "resize"
)

// WS server message types.
const (
	// WSMsgLog is a log line push.
	WSMsgLog = "log"
	// WSMsgStatus is a state change notification.
	WSMsgStatus = "status"
	// WSMsgError reports a rejected client action.
	WSMsgError = "error"
	// WSMsgPong answers a client ping.
	WSMsgPong = "pong"
	// WSMsgHello is the first frame after a successful upgrade.
	WSMsgHello = "hello"
)
