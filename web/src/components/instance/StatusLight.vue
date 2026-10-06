<script setup lang="ts">
import { computed } from 'vue'
import { cn } from '@/components/ui/cn'
import type { InstanceStatus } from '@/api/types'

const props = defineProps<{ status: InstanceStatus; size?: 'sm' | 'md' }>()

const tone: Record<InstanceStatus, string> = {
  running: 'bg-emerald-500 shadow-emerald-500/40',
  starting: 'bg-amber-400 shadow-amber-400/40 animate-pulse',
  stopping: 'bg-amber-400 shadow-amber-400/40 animate-pulse',
  stopped: 'bg-zinc-600',
  crashed: 'bg-red-500 shadow-red-500/40',
  unknown: 'bg-zinc-700',
}

const label: Record<InstanceStatus, string> = {
  running: '运行中',
  starting: '启动中',
  stopping: '停止中',
  stopped: '已停止',
  crashed: '已崩溃',
  unknown: '未知',
}

const dotClass = computed(() => cn('rounded-full shadow-[0_0_8px]', tone[props.status] ?? tone.unknown))
const sizeClass = computed(() => (props.size === 'sm' ? 'size-2' : 'size-2.5'))
const text = computed(() => label[props.status] ?? label.unknown)
</script>

<template>
  <span class="inline-flex items-center gap-2" :data-status="status">
    <span :class="cn(dotClass, sizeClass)" />
    <span class="text-xs text-zinc-400">{{ text }}</span>
  </span>
</template>
