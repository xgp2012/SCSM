/**
 * TypeScript mirrors of the scnetm REST/WS surface (plan §5.6).
 *
 * Endpoints owned by other agents may legitimately answer `501 Not Implemented`
 * in this build; every page therefore treats "not available" as a first-class
 * state instead of an exception.
 */

/* ------------------------------------------------------------------ */
/* Envelopes                                                          */
/* ------------------------------------------------------------------ */

export interface APIErrorBody {
  code: string
  message: string
  details?: unknown
}

export interface APIErrorEnvelope {
  error: APIErrorBody
}

/** Error thrown by the client for any non-2xx / transport failure. */
export class ApiError extends Error {
  readonly status: number
  readonly code: string
  readonly details: unknown

  constructor(status: number, code: string, message: string, details?: unknown) {
    super(message)
    this.name = 'ApiError'
    this.status = status
    this.code = code
    this.details = details ?? null
  }

  /** True when the backend does not implement this route yet. */
  get isUnavailable(): boolean {
    return this.status === 501 || this.code === 'not_implemented'
  }

  get isConflict(): boolean {
    return this.status === 409
  }

  get isValidation(): boolean {
    return this.status === 422
  }
}

/* ------------------------------------------------------------------ */
/* Auth & users                                                       */
/* ------------------------------------------------------------------ */

export type Role = 'admin' | 'operator' | 'viewer'

export interface User {
  id: number
  username: string
  role: Role
  created_at: string
  last_login_at?: string | null
}

export interface LoginRequest {
  username: string
  password: string
}

export interface LoginResponse {
  token: string
  expires_at?: string
  user: User
}

/** Mirrors api.SetupStatusResponse (GET /api/v1/auth/setup-required). */
export interface SetupStatus {
  /** true → first run, /setup must be used to create the admin password. */
  setup_required: boolean
  /** Why setup is required, for the login screen to explain itself. */
  reason?: string
  /** The account that will receive the password (normally "admin"). */
  username?: string
  /** Server-side minimum password length, so the client can agree with it. */
  min_password_length: number
}

export interface SetupRequest {
  username?: string
  password: string
  /** Password confirmation — echoed back by the form. */
  confirm_password?: string
}

/* ------------------------------------------------------------------ */
/* Instances                                                          */
/* ------------------------------------------------------------------ */

export type InstanceStatus =
  | 'stopped'
  | 'starting'
  | 'running'
  | 'stopping'
  | 'crashed'
  | 'unknown'

export interface InstanceSummary {
  id: string
  name: string
  status: InstanceStatus
  port: number
  dir: string
  world_name?: string
  players_online?: number
  players_max?: number
  cpu_percent?: number
  memory_bytes?: number
  memory_limit_bytes?: number
  uptime_seconds?: number
  auto_start?: boolean
  last_exit_code?: number | null
  started_at?: string | null
}

export interface InstanceStats {
  instance_id: string
  status: InstanceStatus
  cpu_percent: number
  memory_bytes: number
  memory_limit_bytes?: number
  players_online: number
  players_max?: number
  uptime_seconds: number
  /** Colour capability of the terminal channel (D1 acceptance point). */
  color_mode?: 'enhanced' | 'basic'
  /** Rolling samples used by the monitor charts. */
  samples?: MetricSample[]
}

export interface MetricSample {
  t: number
  cpu_percent: number
  memory_bytes: number
  players: number
}

export interface Player {
  name: string
  /** Player index in the world ("/player list 0" indexes). */
  index?: number
  online: boolean
  ping?: number
  joined_at?: string
}

export interface CreateInstanceRequest {
  name: string
  port: number
  template?: string
  world_name?: string
  max_players?: number
  auto_start?: boolean
  /** Optional dedicated server directory; empty → panel default. */
  dir?: string
}

export interface StopInstanceRequest {
  force: boolean
}

export interface ActionResult {
  ok: boolean
  message?: string
}

/**
 * Transport-neutral file handle used by the `ui/` adapter.
 *
 * `fuxsto-design`'s own `UploadFile` type is structurally identical, but the
 * adapter must not leak a third-party type into application code (it has to
 * survive a library swap), so the shim declares its own.
 */
export interface UploadFile {
  uid: string
  name: string
  status: 'uploading' | 'done' | 'error'
  size: number
  percent?: number
  url?: string
  raw?: File
  response?: unknown
  error?: Error
}

/* ------------------------------------------------------------------ */
/* Config                                                             */
/* ------------------------------------------------------------------ */

/**
 * GameMode is a numeric enum on the server side. Mapping (plan §6.2.1 / D5):
 * 0 Creative · 1 Harmless · 2 Survival · 3 Challenging · 4 Cruel.
 * The inverse mapping in the UI is flagged "推断值，待实测校验".
 */
export interface ServerSetting {
  WorldName: string
  WorldPath?: string
  Seed?: number
  MaxPlayers?: number
  GameMode: number
  PVP?: boolean
  Seasons?: boolean
  DaySpeed?: number
  RecoverySpeed?: number
  AutoRun?: boolean
  [key: string]: unknown
}

export interface ConfigBundle {
  /** ServerSetting.json — the form-driven part. */
  server_setting: Partial<ServerSetting> | null
  /** Settings.xml — raw text. */
  settings_xml?: string | null
  /** Configs/*.json — filename → raw text. */
  configs?: Record<string, string>
  /** Files discovered on disk, for the advanced editor's file picker. */
  files?: string[]
  restart_required?: boolean
}

export interface ConfigValidationIssue {
  path: string
  message: string
  severity: 'error' | 'warning'
}

export interface ConfigValidationResult {
  valid: boolean
  issues: ConfigValidationIssue[]
}

export interface SaveConfigRequest {
  server_setting?: Partial<ServerSetting>
  /** path → raw content, for Settings.xml and Configs/*.json. */
  raw_files?: Record<string, string>
  restart?: boolean
}

/* ------------------------------------------------------------------ */
/* Worlds                                                             */
/* ------------------------------------------------------------------ */

export interface World {
  name: string
  dir: string
  size_bytes: number
  modified_at: string
  active: boolean
  max_players?: number
  game_mode?: number
  seed?: number
  backup_count?: number
}

/* ------------------------------------------------------------------ */
/* Files                                                              */
/* ------------------------------------------------------------------ */

export type FileKind = 'dir' | 'file' | 'symlink'

export interface FileEntry {
  name: string
  path: string
  kind: FileKind
  size_bytes: number
  modified_at: string
}

export interface FileListing {
  /** Directory relative to the instance root; "" is the root. */
  path: string
  entries: FileEntry[]
  /** Hard boundary enforced by the backend (§5.7 path traversal guard). */
  root: string
}

/* ------------------------------------------------------------------ */
/* Logs                                                               */
/* ------------------------------------------------------------------ */

export type LogLevel = 'trace' | 'debug' | 'info' | 'warn' | 'error' | 'fatal'

export type ColorMode = 'enhanced' | 'basic'

export interface LogLine {
  seq: number
  t: string
  level: LogLevel
  text: string
  raw?: string
}

export interface LogQuery {
  tail?: number
  grep?: string
  level?: LogLevel | 'all'
}

export interface LogPage {
  lines: LogLine[]
  /** Colour capability reported by the PTY layer (D1). */
  color_mode: ColorMode
  /** Present when the backend fell back to a piped, non-PTY pipe. */
  degrade_reason?: string
}

/* ------------------------------------------------------------------ */
/* Backups / jobs / audit / system                                    */
/* ------------------------------------------------------------------ */

export interface Backup {
  id: number
  instance_id: string
  world_name?: string
  path: string
  size_bytes: number
  created_at: string
  trigger: 'manual' | 'scheduled' | 'pre-start' | 'pre-restore'
  note?: string
}

export interface Job {
  id: number
  name: string
  /** Cron expression (5-field) or "" for one-shot jobs. */
  schedule: string
  action: 'backup' | 'restart' | 'start' | 'stop' | 'command'
  instance_id?: string
  command?: string
  enabled: boolean
  last_run_at?: string | null
  last_status?: 'success' | 'failed' | 'running' | null
  last_message?: string
  next_run_at?: string | null
}

export type JobPayload = Omit<
  Job,
  'id' | 'last_run_at' | 'last_status' | 'last_message' | 'next_run_at'
>

export interface AuditEntry {
  id: number
  at: string
  actor: string
  action: string
  resource: string
  instance_id?: string
  ip: string
  /** Human readable diff summary (before → after). */
  diff?: string
  result: 'ok' | 'denied' | 'failed'
}

export interface SystemInfo {
  panel_version: string
  go_version: string
  os: string
  arch: string
  hostname: string
  cpu_count: number
  memory_total_bytes: number
  uptime_seconds: number
  data_dir: string
  /** Instances directory root. */
  instances_dir: string
  /** Detected .NET runtime, empty when missing. */
  dotnet_version?: string
  /** Detected server template pack version. */
  template_version?: string
  runtime_ready: boolean
  /** Optional secondary capabilities the panel probes at boot (V0-1). */
  capabilities?: Record<string, boolean>
}

/* ------------------------------------------------------------------ */
/* WebSocket wire protocol                                            */
/* ------------------------------------------------------------------ */

/** Client → server frames on /ws/instances/:id/console. */
export type ConsoleClientFrame =
  | { type: 'input'; data: string }
  | { type: 'resize'; cols: number; rows: number }
  | { type: 'ping' }

/** Server → client frames on /ws/instances/:id/console. */
export type ConsoleServerFrame =
  | { type: 'output'; data: string; seq?: number }
  | { type: 'status'; status: InstanceStatus }
  | { type: 'color_mode'; mode: ColorMode; reason?: string }
  | { type: 'stats'; stats: InstanceStats }
  | { type: 'error'; message: string }
  | { type: 'pong' }

/** Server → client frames on /ws/events. */
export type EventFrame =
  | { type: 'instance_status'; instance_id: string; status: InstanceStatus }
  | { type: 'instance_stats'; instance_id: string; stats: InstanceStats }
  | { type: 'alert'; level: 'info' | 'warning' | 'error'; message: string }
  | { type: 'job'; job_id: number; status: 'success' | 'failed' | 'running' }
  | { type: 'hello'; server_time: string }

/* ------------------------------------------------------------------ */
/* GameMode helpers (§6.2.1 / D5)                                     */
/* ------------------------------------------------------------------ */

export interface GameModeOption {
  value: number
  label: string
  /** True when the mapping is inferred from the assembly strings, not measured. */
  inferred: boolean
}

export const GAME_MODE_OPTIONS: GameModeOption[] = [
  { value: 0, label: 'Creative（创造）', inferred: true },
  { value: 1, label: 'Harmless（无害）', inferred: false },
  { value: 2, label: 'Survival（生存）', inferred: true },
  { value: 3, label: 'Challenging（挑战）', inferred: true },
  { value: 4, label: 'Cruel（残酷）', inferred: true },
]
