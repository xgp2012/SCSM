import { twMerge } from 'tailwind-merge'
import clsx, { type ClassValue } from 'clsx'

/** Shared class merge helper (same semantics as `fuxsto-design/cn`). */
export function cn(...inputs: ClassValue[]): string {
  return twMerge(clsx(inputs))
}
