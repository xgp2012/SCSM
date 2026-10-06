<script setup lang="ts">
import { Info } from 'lucide-vue-next'

/** Small inline explanatory note. Local shim — no library equivalent. */
defineProps<{ title?: string }>()
</script>

<template>
  <div class="rounded-md border border-zinc-800 bg-zinc-900/60 p-3 text-xs text-zinc-400">
    <div v-if="title" class="mb-1 flex items-center gap-1.5 font-medium text-zinc-300">
      <Info class="size-3.5" />
      {{ title }}
    </div>
    <slot />
  </div>
</template>
