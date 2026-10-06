<script setup lang="ts">
import { UiCard } from '@/components/ui'

/**
 * Compact statistic tile.
 *
 * `fuxsto-design/statistic` animates numbers and is oversized for dense server
 * grids, so the shim ships a smaller local tile (the library's UiStatistic is
 * still re-exported for pages that want it).
 */
withDefaults(
  defineProps<{
    label: string
    value: string | number
    hint?: string
    tone?: 'default' | 'success' | 'warning' | 'danger'
  }>(),
  { tone: 'default', hint: undefined },
)

const toneClass: Record<string, string> = {
  default: 'text-zinc-100',
  success: 'text-emerald-400',
  warning: 'text-amber-400',
  danger: 'text-red-400',
}
</script>

<template>
  <UiCard padding="sm">
    <div class="flex items-baseline justify-between gap-2">
      <span class="text-xs text-zinc-500">{{ label }}</span>
      <span v-if="hint" class="text-[11px] text-zinc-600">{{ hint }}</span>
    </div>
    <div class="mt-1 text-xl font-semibold tabular-nums" :class="toneClass[tone]">
      <slot>{{ value }}</slot>
    </div>
  </UiCard>
</template>
