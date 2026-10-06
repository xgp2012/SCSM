<script setup lang="ts">
import { RouterLink } from 'vue-router'
import { UiButton, UiResult } from '@/components/ui'

defineProps<{ title?: string }>()
</script>

<template>
  <div class="flex min-h-screen items-center justify-center bg-background p-4">
    <UiResult status="info" :title="title ?? '页面不存在'" description="请检查地址，或返回总览。">
      <template #extra>
        <RouterLink :to="{ name: 'overview' }">
          <UiButton>返回总览</UiButton>
        </RouterLink>
      </template>
    </UiResult>
  </div>
</template>
