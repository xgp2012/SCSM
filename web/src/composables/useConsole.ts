import { onBeforeUnmount, ref, watch, type Ref } from 'vue'
import { openConsoleSocket, parseConsoleFrame, type ReconnectingSocket, type SocketState } from '@/api/ws'
import { endpoints } from '@/api/endpoints'
import { isUnavailable } from '@/api/client'
import type { ColorMode, InstanceStatus, LogLevel, LogLine, MetricSample } from '@/api/types'

/** Level ordering used by the filter dropdown. */
export const LOG_LEVELS: Array<{ value: LogLevel | 'all'; label: string }> = [
  { value: 'all', label: '全部级别' },
  { value: 'info', label: 'info 及以上' },
  { value: 'warn', label: 'warn 及以上' },
  { value: 'error', label: 'error 及以上' },
]

const LEVEL_WEIGHT: Record<LogLevel, number> = {
  trace: 0,
  debug: 1,
  info: 2,
  warn: 3,
  error: 4,
  fatal: 5,
}

/**
 * Heuristic level detection for a raw log line.
 *
 * The SurvivalcraftNet log format is `HH:mm:ss: LEVEL: message`; when the line
 * carries no level we keep the previous level so multi-line stack traces stay
 * attached to their originating entry.
 */
export function detectLevel(text: string, fallback: LogLevel = 'info'): LogLevel {
  const match = /\b(TRACE|DEBUG|INFO|WARN|WARNING|ERROR|ERR|FATAL|CRITICAL)\b/i.exec(text)
  if (!match) return fallback
  const token = match[1].toUpperCase()
  switch (token) {
    case 'TRACE':
      return 'trace'
    case 'DEBUG':
      return 'debug'
    case 'WARNING':
    case 'WARN':
      return 'warn'
    case 'ERROR':
    case 'ERR':
      return 'error'
    case 'FATAL':
    case 'CRITICAL':
      return 'fatal'
    default:
      return 'info'
  }
}

/** The character budget for the plain-text mirror of the terminal. */
const PLAIN_BUFFER_CHARS = 400_000
/** Rolling metric window kept client-side for the monitor sparkline. */
const METRIC_WINDOW = 120

export interface ConsoleSession {
  state: Ref<SocketState>
  status: Ref<InstanceStatus>
  colorMode: Ref<ColorMode>
  degradeReason: Ref<string | null>
  levels: Ref<Record<LogLevel, number>>
  metrics: Ref<MetricSample[]>
  /** ANSI-stripped mirror, bounded; drives the "raw/plain" copy + filter view. */
  plainText: Ref<string>
  unavailable: Ref<boolean>
  reconnectAttempt: Ref<number>
  send: (data: string) => void
  sendResize: (cols: number, rows: number) => void
  reconnect: () => void
  disconnect: () => void
  onOutput: (handler: (chunk: string) => void) => () => void
}

/**
 * Owns one instance console connection: WS lifecycle, colour-mode badge state,
 * level counters and the rolling metric window. Both the xterm console and the
 * plain-text mirror read from this single session.
 */
export function useConsoleSession(instanceId: Ref<string>): ConsoleSession {
  const state = ref<SocketState>('closed')
  const status = ref<InstanceStatus>('unknown')
  const colorMode = ref<ColorMode>('enhanced')
  const degradeReason = ref<string | null>(null)
  const levels = ref<Record<LogLevel, number>>({
    trace: 0,
    debug: 0,
    info: 0,
    warn: 0,
    error: 0,
    fatal: 0,
  })
  const metrics = ref<MetricSample[]>([])
  const plainText = ref('')
  const unavailable = ref(false)
  const reconnectAttempt = ref(0)

  let socket: ReconnectingSocket | null = null
  let lastLevel: LogLevel = 'info'
  const outputHandlers = new Set<(chunk: string) => void>()

  function emit(chunk: string): void {
    for (const handler of outputHandlers) handler(chunk)
  }

  function appendPlain(chunk: string): void {
    const next = plainText.value + chunk
    plainText.value = next.length > PLAIN_BUFFER_CHARS ? next.slice(next.length - PLAIN_BUFFER_CHARS) : next
  }

  function ingestOutput(chunk: string): void {
    appendPlain(chunk)
    for (const rawLine of chunk.split(/\r?\n/)) {
      if (!rawLine.trim()) continue
      lastLevel = detectLevel(rawLine, lastLevel)
      levels.value = { ...levels.value, [lastLevel]: levels.value[lastLevel] + 1 }
    }
    emit(chunk)
  }

  function handleFrame(raw: string): void {
    const frame = parseConsoleFrame(raw)
    switch (frame.type) {
      case 'output':
        ingestOutput(frame.data)
        break
      case 'status':
        status.value = frame.status
        break
      case 'color_mode':
        colorMode.value = frame.mode
        degradeReason.value = frame.reason ?? null
        break
      case 'stats': {
        const sample: MetricSample = {
          t: Date.now() / 1000,
          cpu_percent: frame.stats.cpu_percent,
          memory_bytes: frame.stats.memory_bytes,
          players: frame.stats.players_online,
        }
        metrics.value = [...metrics.value, sample].slice(-METRIC_WINDOW)
        if (frame.stats.status) status.value = frame.stats.status
        if (frame.stats.color_mode) colorMode.value = frame.stats.color_mode
        break
      }
      case 'error':
        ingestOutput(`\r\n[panel] ${frame.message}\r\n`)
        break
      default:
        break
    }
  }

  async function probeColorMode(): Promise<void> {
    // Fallback path: if the WS never announces a colour mode, ask the REST
    // layer for the last log page, which carries the same D1 signal.
    try {
      const page = await endpoints.getLogs(instanceId.value, { tail: 1 })
      if (page?.color_mode) colorMode.value = page.color_mode
      if (page?.degrade_reason) degradeReason.value = page.degrade_reason
    } catch (err) {
      if (isUnavailable(err)) unavailable.value = true
    }
  }

  function connect(): void {
    disconnect()
    unavailable.value = false
    if (!instanceId.value) return
    socket = openConsoleSocket(instanceId.value, {
      onState: (next) => {
        state.value = next
      },
      onMessage: handleFrame,
      onReconnectScheduled: (attempt) => {
        reconnectAttempt.value = attempt
      },
      onOpen: () => {
        reconnectAttempt.value = 0
      },
    })
  }

  function disconnect(): void {
    socket?.close()
    socket = null
    state.value = 'closed'
  }

  function reconnect(): void {
    reconnectAttempt.value = 0
    connect()
  }

  function send(data: string): void {
    const trimmed = data.replace(/[\r\n]+/g, ' ').trim()
    if (!trimmed) return
    socket?.send({ type: 'input', data: `${trimmed}\n` })
  }

  function sendResize(cols: number, rows: number): void {
    socket?.send({ type: 'resize', cols, rows })
  }

  function onOutput(handler: (chunk: string) => void): () => void {
    outputHandlers.add(handler)
    return () => outputHandlers.delete(handler)
  }

  watch(instanceId, () => {
    resetCounters()
    connect()
    void probeColorMode()
  })

  function resetCounters(): void {
    levels.value = { trace: 0, debug: 0, info: 0, warn: 0, error: 0, fatal: 0 }
    metrics.value = []
    plainText.value = ''
  }

  connect()
  void probeColorMode()

  onBeforeUnmount(disconnect)

  return {
    state,
    status,
    colorMode,
    degradeReason,
    levels,
    metrics,
    plainText,
    unavailable,
    reconnectAttempt,
    send,
    sendResize,
    reconnect,
    disconnect,
    onOutput,
  }
}

/** Filter a batch of plain log lines by the active level filter. */
export function filterLines(text: string, minLevel: LogLevel | 'all'): LogLine[] {
  const threshold = minLevel === 'all' ? -1 : LEVEL_WEIGHT[minLevel]
  const out: LogLine[] = []
  let running: LogLevel = 'info'
  let seq = 0
  for (const raw of text.split(/\r?\n/)) {
    if (!raw.trim()) continue
    running = detectLevel(raw, running)
    seq += 1
    if (threshold >= 0 && LEVEL_WEIGHT[running] < threshold) continue
    out.push({ seq, t: '', level: running, text: raw })
  }
  return out
}
