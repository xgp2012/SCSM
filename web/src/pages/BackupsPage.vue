<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { Archive, Download, RefreshCw, RotateCcw, Trash2 } from 'lucide-vue-next'
import {
  UiAlert,
  UiButton,
  UiCard,
  UiEmpty,
  UiLoading,
  UiPopconfirm,
  UiSelect,
  UiTable,
  Message,
  type TableColumn,
} from '@/components/ui'
import { endpoints } from '@/api/endpoints'
import { errorMessage, isUnavailable } from '@/api/client'
import { formatBytes, formatDateTime } from '@/utils/format'
import type { Backup } from '@/api/types'

const doRestore = (row: Backup): Promise<unknown> => endpoints.restoreBackup(row.id)
const doDelete = (row: Backup): Promise<unknown> => endpoints.deleteBackup(row.id)

const backups = ref<Backup[]>([])
const loading = ref(false)
const unavailable = ref(false)
const loadError = ref<string | null>(null)
const instanceFilter = ref<string>('')
const busy = ref(false)

const columns: TableColumn[] = [
  { key: 'id', title: '#', width: 70 },
  { key: 'instance_id', title: '实例', width: 160 },
  { key: 'world_name', title: '存档', width: 160 },
  { key: 'trigger', title: '触发方式', width: 120 },
  { key: 'size_bytes', title: '大小', width: 110 },
  { key: 'created_at', title: '创建时间', width: 180 },
  { key: 'actions', title: '操作', align: 'right' },
]

const triggerLabel: Record<Backup['trigger'], string> = {
  manual: '手动',
  scheduled: '计划任务',
  'pre-start': '启动前',
  'pre-restore': '还原前',
}

const triggers = computed(() => {
  const set = new Set(backups.value.map((b) => b.trigger))
  return Array.from(set).map((trigger) => ({ label: triggerLabel[trigger] ?? trigger, value: trigger }))
})

const triggerFilter = ref<string>('')

const filtered = computed(() =>
  backups.value.filter((b) => {
    if (instanceFilter.value && b.instance_id !== instanceFilter.value) return false
    if (triggerFilter.value && b.trigger !== triggerFilter.value) return false
    return true
  }),
)

async function load(): Promise<void> {
  loading.value = true
  loadError.value = null
  unavailable.value = false
  try {
    backups.value = (await endpoints.listBackups()) ?? []
  } catch (err) {
    backups.value = []
    if (isUnavailable(err)) unavailable.value = true
    else loadError.value = errorMessage(err)
  } finally {
    loading.value = false
  }
}

async function guard(action: () => Promise<unknown>, success: string): Promise<void> {
  busy.value = true
  try {
    await action()
    Message.success(success)
    await load()
  } catch (err) {
    Message.error(errorMessage(err))
  } finally {
    busy.value = false
  }
}

function exportRow(row: Backup): void {
  const name = `${row.instance_id}-${row.world_name ?? 'backup'}-${row.id}.zip`
  const anchor = document.createElement('a')
  anchor.href = `/api/v1/backups/${row.id}/download`
  anchor.download = name
  document.body.appendChild(anchor)
  anchor.click()
  document.body.removeChild(anchor)
  Message.info('若后端未提供备份下载路由，此操作不会产生文件')
}

onMounted(load)
</script>

<template>
  <div class="space-y-4">
    <UiAlert v-if="unavailable" type="warning" title="备份接口尚未实现">
      本版本后端对 <code>GET /api/v1/backups</code> 返回 501。
    </UiAlert>
    <UiAlert v-else-if="loadError" type="error" :title="loadError" />

    <UiCard padding="md">
      <template #header>
        <div class="flex flex-wrap items-center justify-between gap-2">
          <div class="flex items-center gap-2">
            <Archive class="size-4 text-zinc-400" />
            <span class="font-medium">备份中心</span>
          </div>
          <div class="flex items-center gap-2">
            <UiButton size="sm" variant="ghost" :icon="RefreshCw" :loading="loading" @click="load">
              刷新
            </UiButton>
          </div>
        </div>
      </template>

      <div class="mb-3 flex flex-wrap items-center gap-2">
        <span class="text-xs text-zinc-500">触发方式</span>
        <UiSelect
          v-model="triggerFilter"
          :options="[{ label: '全部', value: '' }, ...triggers]"
          size="sm"
          clearable
        />
        <span class="ml-2 text-xs text-zinc-500">共 {{ filtered.length }} 份备份</span>
      </div>

      <UiLoading :loading="loading">
        <UiEmpty
          v-if="filtered.length === 0 && !loading"
          size="sm"
          title="暂无备份"
          description="计划任务或手动备份触发后会在这里出现。"
        />
        <UiTable v-else :columns="columns" :data="filtered" row-key="id" hover>
          <template #cell-instance_id="{ row }">
            <span class="font-mono text-xs">{{ (row as Backup).instance_id }}</span>
          </template>
          <template #cell-trigger="{ row }">
            {{ triggerLabel[(row as Backup).trigger] ?? (row as Backup).trigger }}
          </template>
          <template #cell-size_bytes="{ row }">
            {{ formatBytes((row as Backup).size_bytes) }}
          </template>
          <template #cell-created_at="{ row }">
            <span class="text-xs text-zinc-400">{{ formatDateTime((row as Backup).created_at) }}</span>
          </template>
          <template #cell-actions="{ row }">
            <div class="flex items-center justify-end gap-1">
              <UiButton size="sm" variant="ghost" :icon="Download" @click="exportRow(row as Backup)">
                下载
              </UiButton>
              <UiPopconfirm
                title="还原该备份？"
                description="当前存档会被覆盖，还原前会自动再备份一次。"
                danger
                confirm-text="还原"
                @confirm="guard(() => doRestore(row as Backup), '已开始还原')"
              >
                <UiButton size="sm" variant="ghost" :icon="RotateCcw" :disabled="busy">还原</UiButton>
              </UiPopconfirm>
              <UiPopconfirm
                title="删除该备份？"
                danger
                confirm-text="删除"
                @confirm="guard(() => doDelete(row as Backup), '备份已删除')"
              >
                <UiButton size="sm" variant="ghost" danger :icon="Trash2" :disabled="busy" />
              </UiPopconfirm>
            </div>
          </template>
        </UiTable>
      </UiLoading>
    </UiCard>
  </div>
</template>
