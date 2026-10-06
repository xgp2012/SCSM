<script setup lang="ts">
import { computed, onMounted, ref, type Ref } from 'vue'
import { RefreshCw, ScrollText, Search } from 'lucide-vue-next'
import {
  UiAlert,
  UiBadge,
  UiButton,
  UiCard,
  UiEmpty,
  UiInput,
  UiLoading,
  UiSelect,
  UiTable,
  Message,
  type SelectOption,
  type TableColumn,
} from '@/components/ui'
import { endpoints } from '@/api/endpoints'
import { errorMessage, isUnavailable } from '@/api/client'
import { useInstancesStore } from '@/stores/instances'
import { formatDateTime } from '@/utils/format'
import type { AuditEntry } from '@/api/types'

const instances = useInstancesStore()

const entries = ref<AuditEntry[]>([]) as Ref<AuditEntry[]>
const loading = ref(false)
const unavailable = ref(false)
const loadError = ref<string | null>(null)

const filters = ref({ instance_id: '', from: '', to: '', keyword: '' })

const columns: TableColumn[] = [
  { key: 'at', title: '时间', width: 170 },
  { key: 'actor', title: '操作者', width: 120 },
  { key: 'action', title: '动作', width: 150 },
  { key: 'resource', title: '对象', width: 180 },
  { key: 'result', title: '结果', width: 90, align: 'center' },
  { key: 'ip', title: 'IP', width: 130 },
  { key: 'diff', title: '变更摘要' },
]

const instanceOptions = computed<SelectOption[]>(() => [
  { label: '全部实例', value: '' },
  ...instances.instances.map((item) => ({ label: item.name, value: item.id })),
])

const filtered = computed(() => {
  const needle = filters.value.keyword.trim().toLowerCase()
  if (!needle) return entries.value
  return entries.value.filter((entry) =>
    [entry.action, entry.actor, entry.resource, entry.diff ?? '']
      .join(' ')
      .toLowerCase()
      .includes(needle),
  )
})

const resultTone: Record<string, string> = {
  ok: 'text-emerald-400',
  denied: 'text-amber-400',
  failed: 'text-red-400',
}

async function load(): Promise<void> {
  loading.value = true
  loadError.value = null
  unavailable.value = false
  try {
    const query: { instance_id?: string; from?: string; to?: string } = {}
    if (filters.value.instance_id) query.instance_id = filters.value.instance_id
    if (filters.value.from) query.from = filters.value.from
    if (filters.value.to) query.to = filters.value.to
    entries.value = (await endpoints.listAudit(query)) ?? []
  } catch (err) {
    entries.value = []
    if (isUnavailable(err)) unavailable.value = true
    else loadError.value = errorMessage(err)
  } finally {
    loading.value = false
  }
}

onMounted(() => {
  void instances.fetchAll()
  void load()
})

function exportJson(): void {
  if (filtered.value.length === 0) {
    Message.warning('暂无可导出的记录')
    return
  }
  const blob = new Blob([JSON.stringify(filtered.value, null, 2)], { type: 'application/json' })
  const url = URL.createObjectURL(blob)
  const anchor = document.createElement('a')
  anchor.href = url
  anchor.download = 'scnetm-audit.json'
  document.body.appendChild(anchor)
  anchor.click()
  document.body.removeChild(anchor)
  URL.revokeObjectURL(url)
}
</script>

<template>
  <div class="space-y-4">
    <UiAlert v-if="unavailable" type="warning" title="审计接口尚未实现">
      本版本后端对 <code>GET /api/v1/audit</code> 返回 501。
    </UiAlert>
    <UiAlert v-else-if="loadError" type="error" :title="loadError" />

    <UiCard padding="md">
      <template #header>
        <div class="flex flex-wrap items-center justify-between gap-2">
          <div class="flex items-center gap-2">
            <ScrollText class="size-4 text-zinc-400" />
            <span class="font-medium">审计日志</span>
            <UiBadge variant="outline">{{ filtered.length }} 条</UiBadge>
          </div>
          <div class="flex items-center gap-2">
            <UiButton size="sm" variant="ghost" @click="exportJson">导出 JSON</UiButton>
            <UiButton size="sm" variant="ghost" :icon="RefreshCw" :loading="loading" @click="load">
              刷新
            </UiButton>
          </div>
        </div>
      </template>

      <div class="mb-3 grid grid-cols-1 gap-2 md:grid-cols-4">
        <UiSelect v-model="filters.instance_id" :options="instanceOptions" size="sm" />
        <UiInput v-model="filters.from" size="sm" type="date" placeholder="起始日期" />
        <UiInput v-model="filters.to" size="sm" type="date" placeholder="结束日期" />
        <div class="relative">
          <Search class="pointer-events-none absolute left-2.5 top-2.5 size-3.5 text-zinc-500" />
          <UiInput v-model="filters.keyword" size="sm" class="pl-7" placeholder="关键字" clearable />
        </div>
      </div>

      <UiLoading :loading="loading">
        <UiEmpty
          v-if="filtered.length === 0 && !loading"
          size="sm"
          title="暂无审计记录"
          description="所有写操作都会记录 IP 与变更前后差异摘要。"
        />
        <UiTable v-else :columns="columns" :data="filtered" row-key="id" hover>
          <template #cell-at="{ row }">
            <span class="text-xs text-zinc-400">{{ formatDateTime((row as AuditEntry).at) }}</span>
          </template>
          <template #cell-action="{ row }">
            <span class="font-mono text-xs">{{ (row as AuditEntry).action }}</span>
          </template>
          <template #cell-resource="{ row }">
            <span class="font-mono text-xs">{{ (row as AuditEntry).resource }}</span>
          </template>
          <template #cell-result="{ row }">
            <span :class="resultTone[(row as AuditEntry).result] ?? 'text-zinc-400'">
              {{ (row as AuditEntry).result }}
            </span>
          </template>
          <template #cell-ip="{ row }">
            <span class="font-mono text-xs">{{ (row as AuditEntry).ip }}</span>
          </template>
          <template #cell-diff="{ row }">
            <span class="text-xs text-zinc-400">{{ (row as AuditEntry).diff ?? '-' }}</span>
          </template>
        </UiTable>
      </UiLoading>
    </UiCard>
  </div>
</template>
