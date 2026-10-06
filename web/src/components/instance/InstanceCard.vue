<script setup lang="ts">
import { computed, ref } from 'vue'
import { RouterLink } from 'vue-router'
import { CircleStop, Cpu, Play, RefreshCw, Users } from 'lucide-vue-next'
import { UiBadge, UiButton, UiCard, UiPopconfirm, UiProgress, Message } from '@/components/ui'
import StatusLight from '@/components/instance/StatusLight.vue'
import { useInstancesStore } from '@/stores/instances'
import { useAuthStore } from '@/stores/auth'
import { errorMessage } from '@/api/client'
import { formatBytes, formatPercent, formatDuration } from '@/utils/format'
import type { InstanceSummary } from '@/api/types'

const props = defineProps<{ instance: InstanceSummary }>()

const instances = useInstancesStore()
const auth = useAuthStore()
const busy = ref(false)

const canOperate = computed(() => auth.canOperate)
const isRunning = computed(() => props.instance.status === 'running')
const isBusy = computed(
  () => props.instance.status === 'starting' || props.instance.status === 'stopping',
)

const memoryPercent = computed(() => {
  const used = props.instance.memory_bytes ?? 0
  const limit = props.instance.memory_limit_bytes ?? 0
  if (!limit) return 0
  return Math.min(100, (used / limit) * 100)
})

async function run(action: () => Promise<void>, success: string): Promise<void> {
  busy.value = true
  try {
    await action()
    Message.success(success)
  } catch (err) {
    Message.error(errorMessage(err))
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <UiCard padding="md" interactive="lift">
    <template #header>
      <div class="flex items-start justify-between gap-2">
        <div class="min-w-0">
          <RouterLink
            :to="{ name: 'instance-detail', params: { id: instance.id } }"
            class="truncate text-sm font-semibold hover:underline"
          >
            {{ instance.name }}
          </RouterLink>
          <div class="mt-0.5 flex items-center gap-2">
            <UiBadge variant="outline" class="font-mono text-[11px]">:{{ instance.port }}</UiBadge>
            <span class="truncate text-[11px] text-zinc-500">{{ instance.world_name ?? '-' }}</span>
          </div>
        </div>
        <StatusLight :status="instance.status" />
      </div>
    </template>

    <div class="space-y-3">
      <div class="grid grid-cols-2 gap-3 text-xs">
        <div class="flex items-center gap-1.5 text-zinc-400">
          <Users class="size-3.5" />
          <span>
            {{ instance.players_online ?? 0 }}
            <span class="text-zinc-600">/ {{ instance.players_max ?? '-' }}</span>
          </span>
        </div>
        <div class="flex items-center gap-1.5 text-zinc-400">
          <Cpu class="size-3.5" />
          <span>{{ formatPercent(instance.cpu_percent ?? 0) }}</span>
        </div>
        <div class="text-zinc-400">内存 {{ formatBytes(instance.memory_bytes) }}</div>
        <div class="text-zinc-400">运行 {{ formatDuration(instance.uptime_seconds) }}</div>
      </div>

      <UiProgress
        :percentage="instance.cpu_percent ?? 0"
        size="sm"
        :status="(instance.cpu_percent ?? 0) > 85 ? 'warning' : 'normal'"
        :show-text="false"
      />
      <UiProgress
        v-if="instance.memory_limit_bytes"
        :percentage="memoryPercent"
        size="sm"
        :status="memoryPercent > 85 ? 'warning' : 'normal'"
        :show-text="false"
      />

      <div class="flex flex-wrap items-center gap-2 pt-1">
        <UiButton
          size="sm"
          :icon="Play"
          :disabled="busy || !canOperate || isRunning || isBusy"
          @click="run(() => instances.start(instance.id), '已下发起动指令')"
        >
          启动
        </UiButton>
        <UiPopconfirm
          title="停止该实例？"
          description="面板会先走优雅停止（save + stop），超时后才允许强制结束。"
          @confirm="run(() => instances.stop(instance.id), '已下发停止指令')"
        >
          <UiButton
            size="sm"
            variant="secondary"
            :icon="CircleStop"
            :disabled="busy || !canOperate || !isRunning"
          >
            停止
          </UiButton>
        </UiPopconfirm>
        <UiButton
          size="sm"
          variant="ghost"
          :icon="RefreshCw"
          :disabled="busy || !canOperate"
          @click="run(() => instances.restart(instance.id), '已下发重启指令')"
        >
          重启
        </UiButton>
        <RouterLink
          :to="{ name: 'instance-detail', params: { id: instance.id } }"
          class="ml-auto text-xs text-zinc-400 hover:text-zinc-200"
        >
          详情 →
        </RouterLink>
      </div>
    </div>
  </UiCard>
</template>
