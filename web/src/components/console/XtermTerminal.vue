<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { Terminal } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import { WebLinksAddon } from '@xterm/addon-web-links'
import '@xterm/xterm/css/xterm.css'
import { cn } from '@/components/ui/cn'

const props = withDefaults(
  defineProps<{
    /** Data chunks to write into the terminal (non-ANSI-filtered). */
    chunks: string[]
    /** Row/col reporting after FitAddon.fit() — forwarded to pty.Setsize. */
    onResize?: (cols: number, rows: number) => void
    /** User keystrokes when the terminal owns the input focus. */
    onData?: (data: string) => void
    fontSize?: number
  }>(),
  { fontSize: 13 },
)

const emit = defineEmits<{
  ready: [terminal: Terminal]
  resize: [cols: number, rows: number]
}>()

const host = ref<HTMLDivElement | null>(null)
const term = ref<Terminal | null>(null)
let fitAddon: FitAddon | null = null
let resizeObserver: ResizeObserver | null = null
let lastCols = 0
let lastRows = 0

/** Applied to every terminal instance — ANSI colours come from the PTY stream. */
const TERMINAL_THEME = {
  background: '#09090b',
  foreground: '#e4e4e7',
  cursor: '#a1a1aa',
  selectionBackground: '#3f3f46',
  black: '#18181b',
  red: '#f87171',
  green: '#4ade80',
  yellow: '#facc15',
  blue: '#60a5fa',
  magenta: '#c084fc',
  cyan: '#22d3ee',
  white: '#e4e4e7',
  brightBlack: '#52525b',
  brightRed: '#fca5a5',
  brightGreen: '#86efac',
  brightYellow: '#fde68a',
  brightBlue: '#93c5fd',
  brightMagenta: '#d8b4fe',
  brightCyan: '#67e8f9',
  brightWhite: '#fafafa',
} as const

function pushChunk(value: string): void {
  term.value?.write(value)
}

function fit(): void {
  if (!fitAddon || !term.value || !host.value) return
  const rect = host.value.getBoundingClientRect()
  if (rect.width < 8 || rect.height < 8) return
  try {
    fitAddon.fit()
  } catch {
    return
  }
  const { cols, rows } = term.value
  if (cols === lastCols && rows === lastRows) return
  lastCols = cols
  lastRows = rows
  props.onResize?.(cols, rows)
  emit('resize', cols, rows)
}

onMounted(() => {
  const instance = new Terminal({
    convertEol: false,
    cursorBlink: true,
    cursorStyle: 'bar',
    fontFamily:
      '"JetBrains Mono", "Cascadia Mono", "SFMono-Regular", Menlo, Consolas, "Liberation Mono", monospace',
    fontSize: props.fontSize,
    lineHeight: 1.25,
    letterSpacing: 0,
    scrollback: 20000,
    allowProposedApi: true,
    theme: TERMINAL_THEME,
  })
  fitAddon = new FitAddon()
  instance.loadAddon(fitAddon)
  try {
    instance.loadAddon(new WebLinksAddon())
  } catch {
    /* web-links is a convenience; never block the console on it */
  }

  if (host.value) {
    instance.open(host.value)
  }
  term.value = instance
  // Replay whatever arrived before the terminal existed.
  for (const chunk of props.chunks) pushChunk(chunk)

  instance.onData((data) => props.onData?.(data))
  emit('ready', instance)

  resizeObserver = new ResizeObserver(() => fit())
  if (host.value) resizeObserver.observe(host.value)
  requestAnimationFrame(() => {
    fit()
    instance.focus()
  })
})

onBeforeUnmount(() => {
  resizeObserver?.disconnect()
  resizeObserver = null
  term.value?.dispose()
  term.value = null
  fitAddon = null
})

// Every new chunk is appended immediately (xterm decodes ANSI + OSC sequences).
watch(
  () => props.chunks.length,
  (length, previous) => {
    if (length === 0) return
    if (previous === undefined || length < previous) {
      term.value?.reset()
      for (const chunk of props.chunks) pushChunk(chunk)
      requestAnimationFrame(() => fit())
      return
    }
    pushChunk(props.chunks[length - 1])
  },
)

defineExpose({
  focus: () => term.value?.focus(),
  clear: () => term.value?.clear(),
  reset: () => {
    term.value?.reset()
    lastCols = 0
    lastRows = 0
    requestAnimationFrame(() => fit())
  },
  fit,
  getTerminal: () => term.value,
})
</script>

<template>
  <div
    ref="host"
    :class="cn('h-full w-full overflow-hidden rounded-md border border-zinc-800 bg-[#09090b] p-1')"
    data-testid="xterm-host"
  />
</template>
