<script setup lang="ts">
import { computed, onMounted, ref, type Ref } from 'vue'
import { Archive, Download, FolderInput, PlayCircle, Trash2, Upload } from 'lucide-vue-next'
import {
  UiAlert,
  UiButton,
  UiCard,
  UiEmpty,
  UiLoading,
  UiPopconfirm,
  UiTable,
  UiUpload,
  Message,
  type TableColumn,
  type UploadFile,
} from '@/components/ui'
import { endpoints } from '@/api/endpoints'
import { errorMessage, isUnavailable } from '@/api/client'
import { formatBytes, formatDateTime } from '@/utils/format'
import type { World } from '@/api/types'

const doActivate = (world: World): Promise<unknown> => endpoints.activateWorld(props.instanceId, world.name)
const doBackup = (world: World): Promise<unknown> => endpoints.backupWorld(props.instanceId, world.name)
const doRestore = (world: World): Promise<unknown> => endpoints.restoreWorld(props.instanceId, world.name)
const doDelete = (world: World): Promise<unknown> => endpoints.deleteWorld(props.instanceId, world.name)

const props = defineProps<{ instanceId: string }>()

const worlds = ref<World[]>([]) as Ref<World[]>
const loading = ref(false)
const unavailable = ref(false)
const loadError = ref<string | null>(null)
const uploadFiles = ref<UploadFile[]>([])
const busy = ref(false)

const columns: TableColumn[] = [
  { key: 'name', title: '存档名', width: 200 },
  { key: 'size_bytes', title: '大小', width: 110 },
  { key: 'modified_at', title: '修改时间', width: 180 },
  { key: 'players', title: '人数上限', width: 100, align: 'center' },
  { key: 'active', title: '启用中', width: 90, align: 'center' },
  { key: 'actions', title: '操作', align: 'right' },
]

const rows = computed(() => worlds.value)

async function load(): Promise<void> {
  loading.value = true
  loadError.value = null
  unavailable.value = false
  try {
    worlds.value = (await endpoints.listWorlds(props.instanceId)) ?? []
  } catch (err) {
    if (isUnavailable(err)) {
      unavailable.value = true
      worlds.value = []
    } else {
      loadError.value = errorMessage(err)
    }
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

async function importZip(): Promise<void> {
  const file = uploadFiles.value[0]?.raw
  if (!file) {
    Message.warning('请先选择要导入的 zip 包')
    return
  }
  const form = new FormData()
  form.append('file', file, file.name)
  await guard(async () => endpoints.importWorld(props.instanceId, form), '存档已导入')
  uploadFiles.value = []
}

function exportWorld(world: World): void {
  const anchor = document.createElement('a')
  anchor.href = endpoints.exportWorldUrl(props.instanceId, world.name)
  anchor.download = `${world.name}.zip`
  document.body.appendChild(anchor)
  anchor.click()
  document.body.removeChild(anchor)
}

onMounted(load)
</script>

<template>
  <div class="space-y-4">
    <UiAlert v-if="unavailable" type="warning" title="存档接口尚未实现">
      本版本后端对 <code>GET /api/v1/instances/:id/worlds</code> 返回 501。
    </UiAlert>
    <UiAlert v-else-if="loadError" type="error" :title="loadError" />

    <UiCard padding="md">
      <template #header>
        <div class="flex flex-wrap items-center justify-between gap-2">
          <div class="flex items-center gap-2">
            <Archive class="size-4 text-zinc-400" />
            <span class="font-medium">存档列表</span>
          </div>
          <div class="flex items-center gap-2">
            <UiButton size="sm" variant="ghost" :icon="FolderInput" :loading="loading" @click="load">
              刷新
            </UiButton>
          </div>
        </div>
      </template>

      <UiLoading :loading="loading">
        <UiEmpty
          v-if="rows.length === 0 && !loading"
          size="sm"
          title="暂无存档"
          description="实例目录下未发现世界存档，或接口返回 501。"
        />
        <UiTable v-else :columns="columns" :data="rows" row-key="name" hover>
          <template #cell-name="{ row }">
            <span class="font-mono text-xs">{{ (row as World).name }}</span>
          </template>
          <template #cell-size_bytes="{ row }">
            {{ formatBytes((row as World).size_bytes) }}
          </template>
          <template #cell-modified_at="{ row }">
            <span class="text-xs text-zinc-400">{{ formatDateTime((row as World).modified_at) }}</span>
          </template>
          <template #cell-players="{ row }">
            {{ (row as World).max_players ?? '-' }}
          </template>
          <template #cell-active="{ row }">
            <span :class="(row as World).active ? 'text-emerald-400' : 'text-zinc-500'">
              {{ (row as World).active ? '是' : '否' }}
            </span>
          </template>
          <template #cell-actions="{ row }">
            <div class="flex items-center justify-end gap-1">
              <UiButton
                size="sm"
                variant="ghost"
                :icon="PlayCircle"
                :disabled="busy || (row as World).active"
                @click="guard(() => doActivate(row as World), '已切换启用存档')"
              >
                启用
              </UiButton>
              <UiButton
                size="sm"
                variant="ghost"
                :icon="Download"
                @click="exportWorld(row as World)"
              >
                导出
              </UiButton>
              <UiButton
                size="sm"
                variant="ghost"
                :icon="Archive"
                :disabled="busy"
                @click="guard(() => doBackup(row as World), '备份已创建')"
              >
                备份
              </UiButton>
              <UiPopconfirm
                title="还原该存档？"
                description="当前存档将被覆盖，操作前会强制备份。"
                danger
                @confirm="guard(() => doRestore(row as World), '已开始还原')"
              >
                <UiButton size="sm" variant="ghost" danger :disabled="busy">还原</UiButton>
              </UiPopconfirm>
              <UiPopconfirm
                title="删除该存档？"
                description="此操作不可撤销。"
                danger
                confirm-text="删除"
                @confirm="guard(() => doDelete(row as World), '存档已删除')"
              >
                <UiButton size="sm" variant="ghost" danger :icon="Trash2" :disabled="busy" />
              </UiPopconfirm>
            </div>
          </template>
        </UiTable>
      </UiLoading>
    </UiCard>

    <UiCard padding="md">
      <template #header>
        <div class="flex items-center gap-2">
          <Upload class="size-4 text-zinc-400" />
          <span class="font-medium">导入存档（zip）</span>
        </div>
      </template>
      <div class="space-y-3">
        <UiUpload v-model="uploadFiles" accept=".zip" :max-count="1" :auto-upload="false" drag tip="仅支持 .zip，解压时逐条目校验目标路径（防 zip-slip）" />
        <UiButton size="sm" :icon="Upload" :disabled="busy" @click="importZip">导入</UiButton>
      </div>
    </UiCard>
  </div>
</template>
