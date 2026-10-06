import { api, request } from './client'
import { getToken } from './authState'
import type {
  ActionResult,
  AuditEntry,
  Backup,
  ConfigBundle,
  ConfigValidationResult,
  CreateInstanceRequest,
  FileListing,
  InstanceStats,
  InstanceSummary,
  Job,
  JobPayload,
  LogPage,
  LogQuery,
  LoginRequest,
  LoginResponse,
  Player,
  SaveConfigRequest,
  ServerSetting,
  SetupRequest,
  SetupStatus,
  StopInstanceRequest,
  SystemInfo,
  User,
  World,
} from './types'

/** The complete `/api/v1` surface from plan §5.6, in one place. */
export const endpoints = {
  /* ---------------- auth ---------------- */
  setupStatus: () => api.get<SetupStatus>('/setup/status', { redirectOn401: false }),
  setup: (body: SetupRequest) =>
    request<LoginResponse>('/setup', { method: 'POST', body, redirectOn401: false }),
  login: (body: LoginRequest) =>
    request<LoginResponse>('/auth/login', { method: 'POST', body, redirectOn401: false }),
  logout: () => api.post<ActionResult>('/auth/logout'),
  me: () => api.get<User>('/auth/me', { redirectOn401: false }),

  /* ---------------- instances ---------------- */
  listInstances: () => api.get<InstanceSummary[]>('/instances'),
  createInstance: (body: CreateInstanceRequest) => api.post<InstanceSummary>('/instances', body),
  getInstance: (id: string) => api.get<InstanceSummary>(`/instances/${encodeURIComponent(id)}`),
  deleteInstance: (id: string, removeDir = false) =>
    api.del<ActionResult>(`/instances/${encodeURIComponent(id)}`, {
      query: { remove_dir: removeDir },
    }),
  startInstance: (id: string) =>
    api.post<ActionResult>(`/instances/${encodeURIComponent(id)}/start`),
  stopInstance: (id: string, body: StopInstanceRequest) =>
    api.post<ActionResult>(`/instances/${encodeURIComponent(id)}/stop`, body),
  restartInstance: (id: string) =>
    api.post<ActionResult>(`/instances/${encodeURIComponent(id)}/restart`),
  instanceStats: (id: string) =>
    api.get<InstanceStats>(`/instances/${encodeURIComponent(id)}/stats`),
  instancePlayers: (id: string) =>
    api.get<Player[]>(`/instances/${encodeURIComponent(id)}/players`),

  /* ---------------- config ---------------- */
  getConfig: (id: string) => api.get<ConfigBundle>(`/instances/${encodeURIComponent(id)}/config`),
  saveConfig: (id: string, body: SaveConfigRequest) =>
    api.put<ActionResult>(`/instances/${encodeURIComponent(id)}/config`, body),
  validateConfig: (id: string, body: SaveConfigRequest) =>
    api.post<ConfigValidationResult>(`/instances/${encodeURIComponent(id)}/config/validate`, body),
  /** Convenience wrapper for the ServerSetting.json form. */
  saveServerSetting: (id: string, setting: Partial<ServerSetting>, restart = false) =>
    api.put<ActionResult>(`/instances/${encodeURIComponent(id)}/config`, {
      server_setting: setting,
      restart,
    } satisfies SaveConfigRequest),

  /* ---------------- worlds ---------------- */
  listWorlds: (id: string) => api.get<World[]>(`/instances/${encodeURIComponent(id)}/worlds`),
  importWorld: (id: string, form: FormData) =>
    api.upload<ActionResult>(`/instances/${encodeURIComponent(id)}/worlds/import`, form),
  exportWorldUrl: (id: string, world: string) =>
    `${apiBase()}/instances/${encodeURIComponent(id)}/worlds/${encodeURIComponent(world)}/export`,
  backupWorld: (id: string, world: string) =>
    api.post<ActionResult>(
      `/instances/${encodeURIComponent(id)}/worlds/${encodeURIComponent(world)}/backup`,
    ),
  restoreWorld: (id: string, world: string) =>
    api.post<ActionResult>(
      `/instances/${encodeURIComponent(id)}/worlds/${encodeURIComponent(world)}/restore`,
    ),
  activateWorld: (id: string, world: string) =>
    api.post<ActionResult>(
      `/instances/${encodeURIComponent(id)}/worlds/${encodeURIComponent(world)}/activate`,
    ),
  deleteWorld: (id: string, world: string) =>
    api.del<ActionResult>(
      `/instances/${encodeURIComponent(id)}/worlds/${encodeURIComponent(world)}`,
    ),

  /* ---------------- files ---------------- */
  listFiles: (id: string, path = '') =>
    api.get<FileListing>(`/instances/${encodeURIComponent(id)}/files`, { query: { path } }),
  uploadFile: (id: string, form: FormData) =>
    api.upload<ActionResult>(`/instances/${encodeURIComponent(id)}/files/upload`, form),
  fileDownloadUrl: (id: string, path: string) =>
    `${apiBase()}/instances/${encodeURIComponent(id)}/files/download?path=${encodeURIComponent(path)}`,
  mkdir: (id: string, path: string) =>
    api.post<ActionResult>(`/instances/${encodeURIComponent(id)}/files/mkdir`, { path }),
  renameFile: (id: string, from: string, to: string) =>
    api.post<ActionResult>(`/instances/${encodeURIComponent(id)}/files/rename`, {
      path: from,
      to,
    }),
  deleteFile: (id: string, path: string) =>
    api.post<ActionResult>(`/instances/${encodeURIComponent(id)}/files/delete`, { path }),
  unzipFile: (id: string, path: string) =>
    api.post<ActionResult>(`/instances/${encodeURIComponent(id)}/files/unzip`, { path }),

  /* ---------------- logs ---------------- */
  getLogs: (id: string, query: LogQuery = {}) =>
    api.get<LogPage>(`/instances/${encodeURIComponent(id)}/logs`, {
      query: {
        tail: query.tail,
        grep: query.grep,
        level: query.level && query.level !== 'all' ? query.level : undefined,
      },
    }),
  logsDownloadUrl: (id: string) => `${apiBase()}/instances/${encodeURIComponent(id)}/logs/download`,

  /* ---------------- backups ---------------- */
  listBackups: (instanceId?: string) =>
    api.get<Backup[]>('/backups', { query: { instance_id: instanceId } }),
  restoreBackup: (id: number) => api.post<ActionResult>(`/backups/${id}/restore`),
  deleteBackup: (id: number) => api.del<ActionResult>(`/backups/${id}`),

  /* ---------------- jobs ---------------- */
  listJobs: () => api.get<Job[]>('/jobs'),
  createJob: (body: JobPayload) => api.post<Job>('/jobs', body),
  updateJob: (id: number, body: Partial<JobPayload>) => api.put<Job>(`/jobs/${id}`, body),
  deleteJob: (id: number) => api.del<ActionResult>(`/jobs/${id}`),

  /* ---------------- users ---------------- */
  listUsers: () => api.get<User[]>('/users'),
  createUser: (body: { username: string; password: string; role: User['role'] }) =>
    api.post<User>('/users', body),
  updateUser: (id: number, body: Partial<{ password: string; role: User['role'] }>) =>
    api.put<User>(`/users/${id}`, body),
  deleteUser: (id: number) => api.del<ActionResult>(`/users/${id}`),

  /* ---------------- audit & system ---------------- */
  listAudit: (query: { instance_id?: string; from?: string; to?: string } = {}) =>
    api.get<AuditEntry[]>('/audit', { query }),
  systemInfo: () => api.get<SystemInfo>('/system/info'),
}

function apiBase(): string {
  return '/api/v1'
}

/* ------------------------------------------------------------------ */
/* WebSocket URLs                                                     */
/* ------------------------------------------------------------------ */

function wsBase(): string {
  const proto = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
  return `${proto}//${window.location.host}`
}

/** Build a WS URL with the JWT passed as `?token=` (browsers cannot set headers). */
export function authedWsUrl(path: string): string {
  const token = getToken()
  const url = new URL(`${wsBase()}${path}`)
  if (token) url.searchParams.set('token', token)
  return url.toString()
}

export const wsUrls = {
  console: (instanceId: string) =>
    authedWsUrl(`/ws/instances/${encodeURIComponent(instanceId)}/console`),
  events: () => authedWsUrl('/ws/events'),
}

export const authHeader = (): Record<string, string> => {
  const token = getToken()
  return token ? { Authorization: `Bearer ${token}` } : {}
}
