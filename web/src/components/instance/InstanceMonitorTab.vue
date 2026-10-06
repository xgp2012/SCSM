<script setup lang="ts">
import { computed, onMounted, onBeforeUnmount, ref } from 'vue'
import { Activity, Users, Cpu, MemoryStick } from 'lucide-vue-next'
import {
  UiAlert,
  UiBadge,
  UiButton,
  UiCard,
  UiEmpty,
  UiProgress,
  UiSegmented,
  UiStatTile,
  Message,
} from '@/components/ui'
import MetricChart, { type Series } from '@/components/monitor/MetricChart.vue'
import { endpoints } from '@/api/endpoints'
import { errorMessage, isUnavailable } from '@/api/client'
import { useInstancesStore } from '@/stores/instances'
import { formatBytes, formatDuration, formatPercent, formatTime } from '@/utils/format'
import type { InstanceStats, MetricSample } from '@/api/types'

const props = defineProps<{ instanceId: string }>()

const instances = useInstancesStore()
const unavailable = ref(false)
const loadError = ref<string | null>(null)
const range = ref<'60s' | '10m' | 'all'>('10m')
const rangeOptions = [
  { label: '近 1 分钟', value: '60s' },
  { label: '近 10 分钟', value: '10m' },
  { label: '全部', value: 'all' },
]

/** Samples arrive from the WS channel managed by the stores layer. */
const samples = ref<MetricSample[]>([])
const stats = ref<InstanceStats | null>(null)
let poller: ReturnType<typeof setInterval> | null = null

const windowed = computed(() => {
  if (range.value === 'all') return samples.value
  const span = range.value === '60s' ? 60 : 600
  const cutoff = Date.now() / 1000 - span
  return samples.value.filter((sample) => sample.t >= cutoff)
})

const timestamps = computed(() => windowed.value.map((sample) => sample.t))

const series = computed<Series[]>(() => [
  {
    label: 'CPU %',
    data: windowed.value.map((sample) => sample.cpu_percent),
    color: '#60a5fa',
    scale: 'y',
    format: (value) => formatPercent(value, 1),
  },
  {
    label: '内存',
    data: windowed.value.map((sample) => sample.memory_bytes / (1024 * 1024)),
    color: '#4ade80',
    scale: 'y',
    format: (value) => `${value.toFixed(1)} MiB`,
  },
  {
    label: '在线玩家',
    data: windowed.value.map((sample) => sample.players),
    color: '#facc15',
    scale: 'y2',
    format: (value) => `${Math.round(value)} 人`,
  },
])

const memoryPercent = computed(() => {
  const used = stats.value?.memory_bytes ?? 0
  const limit = stats.value?.memory_limit_bytes ?? 0
  if (!limit) return 0
  return Math.min(100, (used / limit) * 100)
})

const latest = computed(() => windowed.value[windowed.value.length - 1] ?? null)

async function refreshStats(): Promise<void> {
  try {
    const next = await endpoints.instanceStats(props.instanceId)
    stats.value = next
    unavailable.value = false
    loadError.value = null
    instances.setStats(props.instanceId, next)
  } catch (err) {
    if (isUnavailable(err)) unavailable.value = true
    else loadError.value = errorMessage(err)
  }
}

function ingest(sample: MetricSample): void {
  samples.value = [...samples.value, sample].slice(-1800)
}

// Fold in stats that the /ws/events channel already delivered.
function absorbStoreSamples(): void {
  const live = instances.liveStats[props.instanceId]
  if (!live) return
  stats.value = live
}

onMounted(async () => {
  await refreshStats()
  absorbStoreSamples()
  poller = setInterval(() => {
    void refreshStats()
    absorbStoreSamples()
    const live = instances.liveStats[props.instanceId]
    if (live) {
      ingest({
        t: Date.now() / 1000,
        cpu_percent: live.cpu_percent,
        memory_bytes: live.memory_bytes,
        players: live.players_online,
      })
    }
  }, 5000)
})

onBeforeUnmount(() => {
  if (poller) clearInterval(poller)
  poller = null
})

function exportCsv(): void {
  if (windowed.value.length === 0) {
    Message.warning('暂无样本可导出')
    return
  }
  const header = 'time,cpu_percent,memory_bytes,players\n'
  const rows = windowed.value
    .map((s) => `${new Date(s.t * 1000).toISOString()},${s.cpu_percent},${s.memory_bytes},${s.players}`)
    .join('\n')
  const blob = new Blob([header + rows], { type: 'text/csv;charset=utf-8' })
  const url = URL.createObjectURL(blob)
  const anchor = document.createElement('a')
  anchor.href = url
  anchor.download = `instance-${props.instanceId}-metrics.csv`
  document.body.appendChild(anchor)
  anchor.click()
  document.body.removeChild(anchor)
  URL.revokeObjectURL(url)
}
</script>

<template>
  <div class="space-y-4">
    <UiAlert v-if="unavailable" type="warning" title="监控接口尚未实现">
      本版本后端对 <code>GET /api/v1/instances/:id/stats</code> 返回 501；图表会在 WS 上报 stats 后自动填充。
    </UiAlert>
    <UiAlert v-else-if="loadError" type="error" :title="loadError" />

    <div class="grid grid-cols-2 gap-3 md:grid-cols-4">
      <UiStatTile
        label="CPU"
        :value="formatPercent(stats?.cpu_percent ?? latest?.cpu_percent ?? 0)"
      />
      <UiStatTile label="内存" :value="formatBytes(stats?.memory_bytes ?? latest?.memory_bytes ?? 0)" />
      <UiStatTile
        label="在线玩家"
        :value="stats?.players_online ?? latest?.players ?? 0"
        :hint="stats?.players_max ? `/ ${stats.players_max}` : undefined"
      />
      <UiStatTile label="运行时长" :value="formatDuration(stats?.uptime_seconds ?? 0)" />
    </div>

    <UiCard padding="md">
      <template #header>
        <div class="flex flex-wrap items-center justify-between gap-2">
          <div class="flex items-center gap-2">
            <Activity class="size-4 text-zinc-400" />
            <span class="font-medium">时序指标（uplot）</span>
            <UiBadge variant="outline">{{ windowed.length }} 个样本</UiBadge>
          </div>
          <div class="flex items-center gap-2">
            <UiSegmented v-model="range" :options="rangeOptions" size="sm" />
            <UiButton size="sm" variant="ghost" @click="exportCsv">导出 CSV</UiButton>
          </div>
        </div>
      </template>

      <UiEmpty
        v-if="windowed.length === 0"
        size="sm"
        title="暂无指标样本"
        description="实例运行并上报 stats 后，图表会自动出现。当前为空可能是后端 stats 接口未实现。"
      />
      <MetricChart v-else :timestamps="timestamps" :series="series" :height="240" />
    </UiCard>

    <div class="grid grid-cols-1 gap-4 lg:grid-cols-3">
      <UiCard padding="md">
        <template #header>
          <div class="flex items-center gap-2">
            <MemoryStick class="size-4 text-zinc-400" />
            <span class="font-medium">内存占用</span>
          </div>
        </template>
        <UiProgress :percentage="memoryPercent" :status="memoryPercent > 85 ? 'warning' : 'normal'" />
        <div class="mt-2 text-xs text-zinc-500">
          {{ formatBytes(stats?.memory_bytes ?? 0) }} /
          {{ stats?.memory_limit_bytes ? formatBytes(stats.memory_limit_bytes) : '未设置上限' }}
        </div>
      </UiCard>

      <UiCard padding="md">
        <template #header>
          <div class="flex items-center gap-2">
            <Cpu class="size-4 text-zinc-400" />
            <span class="font-medium">最近采样</span>
          </div>
        </template>
        <div v-if="latest" class="space-y-1 text-xs text-zinc-400">
          <div>时间：{{ formatTime(latest.t) }}</div>
          <div>CPU：{{ formatPercent(latest.cpu_percent) }}</div>
          <div>内存：{{ formatBytes(latest.memory_bytes) }}</div>
          <div>玩家：{{ latest.players }}</div>
        </div>
        <UiEmpty v-else size="sm" title="暂无采样" />
      </UiCard>

      <UiCard padding="md">
        <template #header>
          <div class="flex items-center gap-2">
            <Users class="size-4 text-zinc-400" />
            <span class="font-medium">在线玩家</span>
          </div>
        </template>
        <div class="text-xs text-zinc-400">
          <p>
            在线人数来自 <code>/instances/:id/players</code>（依赖 V0-1 的能力探测）与 stats 上报。
          </p>
          <p class="mt-2">
            当前图表中的玩家序列由 stats 样本驱动；若后端未上报，则序列恒为 0。
          </p>
        </div>
      </UiCard>
    </div>
  </div>
</template>
