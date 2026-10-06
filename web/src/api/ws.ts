import { getToken } from './authState'
import { wsUrls } from './endpoints'
import type {
  ConsoleClientFrame,
  ConsoleServerFrame,
  EventFrame,
  InstanceStats,
  InstanceStatus,
} from './types'

export type SocketState = 'connecting' | 'open' | 'closed' | 'reconnecting'

export interface ReconnectingSocketOptions {
  /** Absolute URL including the auth token query parameter. */
  url: string
  /** First retry delay in ms (doubles per attempt). */
  baseDelay?: number
  /** Upper bound of the exponential backoff. */
  maxDelay?: number
  /** Backoff multiplier, defaults to 2. */
  factor?: number
  /** Random jitter ratio applied to each delay (0 disables). */
  jitter?: number
  /** Max attempts before giving up; Infinity keeps trying (default). */
  maxAttempts?: number
  /** Keep-alive ping interval in ms (0 disables). */
  heartbeatInterval?: number
  onMessage: (raw: string) => void
  onState?: (state: SocketState) => void
  onOpen?: () => void
  /** Called with the retry delay before each scheduled reconnect. */
  onReconnectScheduled?: (attempt: number, delay: number) => void
  onClose?: (event: CloseEvent | null) => void
}

/**
 * WebSocket wrapper with automatic reconnection and exponential backoff.
 *
 * Used for both `/ws/instances/:id/console` and `/ws/events`. The JWT is carried
 * in the `?token=` query parameter because the browser WebSocket API cannot set
 * an Authorization header (plan §5.6).
 */
export class ReconnectingSocket {
  private socket: WebSocket | null = null
  private attempt = 0
  private reconnectTimer: ReturnType<typeof setTimeout> | null = null
  private heartbeatTimer: ReturnType<typeof setInterval> | null = null
  private disposed = false

  private readonly url: string
  private readonly baseDelay: number
  private readonly maxDelay: number
  private readonly factor: number
  private readonly jitter: number
  private readonly maxAttempts: number
  private readonly heartbeatInterval: number
  private readonly handlers: Omit<ReconnectingSocketOptions, 'url'>

  state: SocketState = 'closed'

  constructor(options: ReconnectingSocketOptions) {
    this.url = options.url
    this.baseDelay = options.baseDelay ?? 800
    this.maxDelay = options.maxDelay ?? 30_000
    this.factor = options.factor ?? 2
    this.jitter = options.jitter ?? 0.25
    this.maxAttempts = options.maxAttempts ?? Number.POSITIVE_INFINITY
    this.heartbeatInterval = options.heartbeatInterval ?? 30_000
    this.handlers = options
  }

  get isOpen(): boolean {
    return this.socket !== null && this.socket.readyState === WebSocket.OPEN
  }

  connect(): void {
    if (this.disposed) return
    if (
      this.socket &&
      (this.socket.readyState === WebSocket.OPEN || this.socket.readyState === WebSocket.CONNECTING)
    ) {
      return
    }
    this.setState(this.attempt === 0 ? 'connecting' : 'reconnecting')

    let socket: WebSocket
    try {
      socket = new WebSocket(this.url)
    } catch {
      this.scheduleReconnect()
      return
    }
    this.socket = socket

    socket.onopen = () => {
      this.attempt = 0
      this.setState('open')
      this.startHeartbeat()
      this.handlers.onOpen?.()
    }

    socket.onmessage = (event: MessageEvent<string>) => {
      if (typeof event.data === 'string') this.handlers.onMessage(event.data)
    }

    socket.onerror = () => {
      // `onclose` always follows; reconnection is handled there.
    }

    socket.onclose = (event: CloseEvent) => {
      this.stopHeartbeat()
      this.socket = null
      if (this.disposed) {
        this.setState('closed')
        this.handlers.onClose?.(event)
        return
      }
      // 4401/4403: authentication/authorization failure — retrying is pointless.
      if (event.code === 4401 || event.code === 4403) {
        this.setState('closed')
        this.handlers.onClose?.(event)
        return
      }
      this.scheduleReconnect()
    }
  }

  send(frame: ConsoleClientFrame): boolean {
    if (!this.isOpen) return false
    try {
      this.socket?.send(JSON.stringify(frame))
      return true
    } catch {
      return false
    }
  }

  /** Send an already encoded payload (used by callers with custom frames). */
  sendRaw(payload: unknown): boolean {
    if (!this.isOpen) return false
    try {
      this.socket?.send(typeof payload === 'string' ? payload : JSON.stringify(payload))
      return true
    } catch {
      return false
    }
  }

  close(): void {
    this.disposed = true
    this.clearReconnectTimer()
    this.stopHeartbeat()
    if (this.socket) {
      this.socket.onclose = null
      this.socket.onmessage = null
      this.socket.onerror = null
      this.socket.onopen = null
      try {
        this.socket.close(1000, 'client closed')
      } catch {
        /* ignore */
      }
      this.socket = null
    }
    this.setState('closed')
  }

  private scheduleReconnect(): void {
    if (this.disposed) return
    if (this.attempt >= this.maxAttempts) {
      this.setState('closed')
      this.handlers.onClose?.(null)
      return
    }
    this.attempt += 1
    const raw = this.baseDelay * this.factor ** (this.attempt - 1)
    const capped = Math.min(raw, this.maxDelay)
    const jitter = capped * this.jitter * (Math.random() * 2 - 1)
    const delay = Math.max(250, Math.round(capped + jitter))
    this.setState('reconnecting')
    this.handlers.onReconnectScheduled?.(this.attempt, delay)
    this.reconnectTimer = setTimeout(() => {
      this.reconnectTimer = null
      this.connect()
    }, delay)
  }

  private startHeartbeat(): void {
    this.stopHeartbeat()
    if (this.heartbeatInterval <= 0) return
    this.heartbeatTimer = setInterval(() => {
      this.send({ type: 'ping' })
    }, this.heartbeatInterval)
  }

  private stopHeartbeat(): void {
    if (this.heartbeatTimer !== null) {
      clearInterval(this.heartbeatTimer)
      this.heartbeatTimer = null
    }
  }

  private clearReconnectTimer(): void {
    if (this.reconnectTimer !== null) {
      clearTimeout(this.reconnectTimer)
      this.reconnectTimer = null
    }
  }

  private setState(state: SocketState): void {
    if (this.state === state) return
    this.state = state
    this.handlers.onState?.(state)
  }
}

const CONSOLE_FRAME_TYPES = ['output', 'status', 'color_mode', 'stats', 'error', 'pong'] as const
const INSTANCE_STATUSES: InstanceStatus[] = [
  'stopped',
  'starting',
  'running',
  'stopping',
  'crashed',
  'unknown',
]

function asRecord(raw: string): Record<string, unknown> | null {
  try {
    const value: unknown = JSON.parse(raw)
    if (typeof value !== 'object' || value === null || Array.isArray(value)) return null
    return value as Record<string, unknown>
  } catch {
    return null
  }
}

function asInstanceStatus(value: unknown): InstanceStatus {
  return INSTANCE_STATUSES.includes(value as InstanceStatus) ? (value as InstanceStatus) : 'unknown'
}

/** Parse a console frame defensively — a malformed frame must never crash the app. */
export function parseConsoleFrame(raw: string): ConsoleServerFrame {
  const frame = asRecord(raw)
  if (!frame) return { type: 'output', data: raw }
  switch (frame.type) {
    case 'output':
      return typeof frame.data === 'string'
        ? {
            type: 'output',
            data: frame.data,
            ...(typeof frame.seq === 'number' ? { seq: frame.seq } : {}),
          }
        : { type: 'output', data: raw }
    case 'status':
      return { type: 'status', status: asInstanceStatus(frame.status) }
    case 'color_mode':
      return {
        type: 'color_mode',
        mode: frame.mode === 'enhanced' ? 'enhanced' : 'basic',
        ...(typeof frame.reason === 'string' ? { reason: frame.reason } : {}),
      }
    case 'stats':
      return { type: 'stats', stats: frame.stats as InstanceStats }
    case 'error':
      return { type: 'error', message: String(frame.message ?? 'unknown error') }
    case 'pong':
      return { type: 'pong' }
    default:
      // Tolerate a bare frame type or a plain-text log line sent without envelope.
      if (typeof frame.type === 'string' && (CONSOLE_FRAME_TYPES as readonly string[]).includes(frame.type)) {
        return { type: 'output', data: raw }
      }
      return { type: 'output', data: raw }
  }
}

/** Parse a global event frame defensively. */
export function parseEventFrame(raw: string): EventFrame | null {
  const frame = asRecord(raw)
  if (!frame) return null
  switch (frame.type) {
    case 'hello':
      return { type: 'hello', server_time: String(frame.server_time ?? '') }
    case 'instance_status':
      return {
        type: 'instance_status',
        instance_id: String(frame.instance_id ?? ''),
        status: asInstanceStatus(frame.status),
      }
    case 'instance_stats':
      return {
        type: 'instance_stats',
        instance_id: String(frame.instance_id ?? ''),
        stats: frame.stats as InstanceStats,
      }
    case 'alert':
      return {
        type: 'alert',
        level: frame.level === 'error' || frame.level === 'warning' ? frame.level : 'info',
        message: String(frame.message ?? ''),
      }
    case 'job':
      return {
        type: 'job',
        job_id: Number(frame.job_id ?? 0),
        status:
          frame.status === 'success' || frame.status === 'failed' ? frame.status : 'running',
      }
    default:
      return null
  }
}

/** Convenience factory for the console socket. */
export function openConsoleSocket(
  instanceId: string,
  options: Omit<ReconnectingSocketOptions, 'url'>,
): ReconnectingSocket {
  const socket = new ReconnectingSocket({ ...options, url: wsUrls.console(instanceId) })
  socket.connect()
  return socket
}

/** Convenience factory for the global event socket. */
export function openEventSocket(
  options: Omit<ReconnectingSocketOptions, 'url'>,
): ReconnectingSocket {
  const socket = new ReconnectingSocket({ ...options, url: wsUrls.events() })
  socket.connect()
  return socket
}

export { getToken }
