<script setup lang="ts">
import { UiAlert } from '@/components/ui'

/**
 * Standard empty state for endpoints another agent may still return 501 for.
 * Pages must never crash on an unimplemented route — they render this.
 */
withDefaults(
  defineProps<{
    title?: string
    endpoint?: string
    description?: string
  }>(),
  {
    title: '当前构建中该功能不可用',
    endpoint: undefined,
    description: '后端接口尚未实现（HTTP 501）或未挂载该路由。',
  },
)
</script>

<template>
  <UiAlert type="warning" :title="title">
    <div class="space-y-1">
      <p>{{ description }}</p>
      <p v-if="endpoint" class="font-mono text-[11px] text-zinc-400">{{ endpoint }}</p>
    </div>
  </UiAlert>
</template>
