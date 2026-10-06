<script setup lang="ts">
import { computed } from 'vue'
import { UiEmpty } from '@/components/ui'
import { cn } from '@/components/ui/cn'
import type { LogLine } from '@/api/types'
import { LOG_LEVEL_TONE } from '@/components/console/levelTone'

const props = defineProps<{
  lines: LogLine[]
  /** Follow the tail like `tail -f`. */
  autoScroll?: boolean
  height?: number
  emptyText?: string
}>()

const classes = computed(() => ({
  wrap: cn('overflow-auto rounded-md border border-zinc-800 bg-[#09090b] p-3 font-mono text-xs'),
}))
</script>

<template>
  <div
    :class="classes.wrap"
    :style="{ height: `${props.height ?? 360}px` }"
    data-testid="plain-log-view"
  >
    <UiEmpty v-if="lines.length === 0" size="sm" :title="emptyText ?? '暂无日志'" />
    <div v-else class="space-y-0.5">
      <div v-for="line in lines" :key="line.seq" class="flex gap-2 leading-relaxed">
        <span
          :class="
            cn(
              'w-12 shrink-0 select-none text-right uppercase opacity-70',
              LOG_LEVEL_TONE[line.level],
            )
          "
          >{{ line.level }}</span
        >
        <span class="log-pre min-w-0 flex-1 text-zinc-300">{{ line.text }}</span>
      </div>
    </div>
  </div>
</template>
