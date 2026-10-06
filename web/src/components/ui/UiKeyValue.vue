<script setup lang="ts">
/** Label/value row used by detail panels. Local shim — no library equivalent. */
defineProps<{
  label: string
  value?: string | number | null
  mono?: boolean
}>()
</script>

<template>
  <div class="flex items-start justify-between gap-4 border-b border-zinc-800/60 py-2 last:border-b-0">
    <span class="shrink-0 text-xs text-zinc-500">{{ label }}</span>
    <span
      class="min-w-0 truncate text-right text-sm text-zinc-200"
      :class="mono ? 'font-mono text-xs' : ''"
    >
      <slot>{{ value ?? '-' }}</slot>
    </span>
  </div>
</template>
