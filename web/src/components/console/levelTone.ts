import type { LogLevel } from '@/api/types'

/** Tailwind text colour per log level, shared by the console and audit views. */
export const LOG_LEVEL_TONE: Record<LogLevel, string> = {
  trace: 'text-zinc-600',
  debug: 'text-zinc-500',
  info: 'text-sky-400',
  warn: 'text-amber-400',
  error: 'text-red-400',
  fatal: 'text-fuchsia-400',
}

/** Background tone for the level filter chips. */
export const LOG_LEVEL_CHIP: Record<LogLevel, string> = {
  trace: 'bg-zinc-800 text-zinc-400',
  debug: 'bg-zinc-800 text-zinc-400',
  info: 'bg-sky-950 text-sky-300',
  warn: 'bg-amber-950 text-amber-300',
  error: 'bg-red-950 text-red-300',
  fatal: 'bg-fuchsia-950 text-fuchsia-300',
}
