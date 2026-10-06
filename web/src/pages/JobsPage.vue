<script setup lang="ts">
import { computed, onMounted, ref, type Ref } from 'vue'
import { CalendarClock, Play, Plus, RefreshCw, Trash2 } from 'lucide-vue-next'
import {
  UiAlert,
  UiBadge,
  UiButton,
  UiCard,
  UiEmpty,
  UiInput,
  UiLoading,
  UiPopconfirm,
  UiSelect,
  UiSwitch,
  UiTable,
  Message,
  type SelectOption,
  type TableColumn,
} from '@/components/ui'
import { endpoints } from '@/api/endpoints'
import { errorMessage, isUnavailable } from '@/api/client'
import { useInstancesStore } from '@/stores/instances'
import { useAuthStore } from '@/stores/auth'
import { formatDateTime } from '@/utils/format'
import type { Job, JobPayload } from '@/api/types'

const doDelete = (row: Job): Promise<unknown> => endpoints.deleteJob(row.id)

const instances = useInstancesStore()
const auth = useAuthStore()

const jobs = ref<Job[]>([]) as Ref<Job[]>
const loading = ref(false)
const unavailable = ref(false)
const loadError = ref<string | null>(null)
const busy = ref(false)
const editorOpen = ref(false)
const error = ref<string | null>(null)

const blank = (): JobPayload => ({
  name: '',
  schedule: '0 4 * * *',
  action: 'backup',
  instance_id: '',
  command: '',
  enabled: true,
})

const draft = ref<JobPayload>(blank())
const editingId = ref<number | null>(null)

const columns: TableColumn[] = [
  { key: 'name', title: '任务名', width: 180 },
  { key: 'schedule', title: 'Cron', width: 130 },
  { key: 'action', title: '动作', width: 110 },
  { key: 'instance_id', title: '实例', width: 150 },
  { key: 'last_status', title: '上次结果', width: 110 },
  { key: 'next_run_at', title: '下次执行', width: 170 },
  { key: 'enabled', title: '启用', width: 80, align: 'center' },
  { key: 'actions', title: '操作', align: 'right' },
]

const actionOptions: SelectOption[] = [
  { label: '备份 (backup)', value: 'backup' },
  { label: '重启 (restart)', value: 'restart' },
  { label: '启动 (start)', value: 'start' },
  { label: '停止 (stop)', value: 'stop' },
  { label: '发送指令 (command)', value: 'command' },
]

const instanceOptions = computed<SelectOption[]>(() => [
  { label: '（全部实例）', value: '' },
  ...instances.instances.map((item) => ({ label: item.name, value: item.id })),
])

const statusTone: Record<string, string> = {
  success: 'text-emerald-400',
  failed: 'text-red-400',
  running: 'text-amber-400',
}

const canOperate = computed(() => auth.canOperate)

async function load(): Promise<void> {
  loading.value = true
  loadError.value = null
  unavailable.value = false
  try {
    jobs.value = (await endpoints.listJobs()) ?? []
  } catch (err) {
    jobs.value = []
    if (isUnavailable(err)) unavailable.value = true
    else loadError.value = errorMessage(err)
  } finally {
    loading.value = false
  }
}

function openEditor(job?: Job): void {
  error.value = null
  if (job) {
    editingId.value = job.id
    draft.value = {
      name: job.name,
      schedule: job.schedule,
      action: job.action,
      instance_id: job.instance_id ?? '',
      command: job.command ?? '',
      enabled: job.enabled,
    }
  } else {
    editingId.value = null
    draft.value = blank()
  }
  editorOpen.value = true
}

function validate(): string | null {
  if (!draft.value.name.trim()) return '任务名不能为空'
  const fields = draft.value.schedule.trim().split(/\s+/)
  if (fields.length !== 5) return 'Cron 表达式需为 5 段（分 时 日 月 周）'
  if (draft.value.action === 'command' && !(draft.value.command ?? '').trim()) {
    return '动作是「发送指令」时必须填写指令内容'
  }
  return null
}

async function submit(): Promise<void> {
  error.value = validate()
  if (error.value) return
  busy.value = true
  try {
    const payload: JobPayload = {
      ...draft.value,
      name: draft.value.name.trim(),
      command: (draft.value.command ?? '').trim(),
    }
    if (editingId.value === null) await endpoints.createJob(payload)
    else await endpoints.updateJob(editingId.value, payload)
    Message.success('已保存')
    editorOpen.value = false
    await load()
  } catch (err) {
    error.value = errorMessage(err)
  } finally {
    busy.value = false
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

async function toggle(row: Job): Promise<void> {
  await guard(() => endpoints.updateJob(row.id, { enabled: !row.enabled }), '已更新启用状态')
}

onMounted(() => {
  void instances.fetchAll()
  void load()
})
</script>

<template>
  <div class="space-y-4">
    <UiAlert v-if="unavailable" type="warning" title="任务接口尚未实现">
      本版本后端对 <code>GET /api/v1/jobs</code> 返回 501。
    </UiAlert>
    <UiAlert v-else-if="loadError" type="error" :title="loadError" />

    <UiCard padding="md">
      <template #header>
        <div class="flex flex-wrap items-center justify-between gap-2">
          <div class="flex items-center gap-2">
            <CalendarClock class="size-4 text-zinc-400" />
            <span class="font-medium">任务计划</span>
            <UiBadge variant="outline">{{ jobs.length }} 个任务</UiBadge>
          </div>
          <div class="flex items-center gap-2">
            <UiButton size="sm" variant="ghost" :icon="RefreshCw" :loading="loading" @click="load">
              刷新
            </UiButton>
            <UiButton size="sm" :icon="Plus" :disabled="!canOperate" @click="openEditor()">
              新建任务
            </UiButton>
          </div>
        </div>
      </template>

      <UiLoading :loading="loading">
        <UiEmpty
          v-if="jobs.length === 0 && !loading"
          size="sm"
          title="暂无计划任务"
          description="可以配置定时备份、定时重启等（Cron 5 段表达式）。"
        />
        <UiTable v-else :columns="columns" :data="jobs" row-key="id" hover>
          <template #cell-schedule="{ row }">
            <span class="font-mono text-xs">{{ (row as Job).schedule || '一次性' }}</span>
          </template>
          <template #cell-last_status="{ row }">
            <span :class="statusTone[(row as Job).last_status ?? ''] ?? 'text-zinc-500'">
              {{ (row as Job).last_status ?? '-' }}
            </span>
          </template>
          <template #cell-next_run_at="{ row }">
            <span class="text-xs text-zinc-400">{{ formatDateTime((row as Job).next_run_at) }}</span>
          </template>
          <template #cell-enabled="{ row }">
            <UiSwitch
              :model-value="(row as Job).enabled"
              size="sm"
              :disabled="busy || !canOperate"
              @update:model-value="toggle(row as Job)"
            />
          </template>
          <template #cell-actions="{ row }">
            <div class="flex items-center justify-end gap-1">
              <UiButton size="sm" variant="ghost" :icon="Play" :disabled="busy || !canOperate" @click="toggle(row as Job)">
                触发
              </UiButton>
              <UiButton size="sm" variant="ghost" :disabled="!canOperate" @click="openEditor(row as Job)">
                编辑
              </UiButton>
              <UiPopconfirm
                title="删除该任务？"
                danger
                confirm-text="删除"
                @confirm="guard(() => doDelete(row as Job), '任务已删除')"
              >
                <UiButton size="sm" variant="ghost" danger :icon="Trash2" :disabled="busy || !canOperate" />
              </UiPopconfirm>
            </div>
          </template>
        </UiTable>
      </UiLoading>
    </UiCard>

    <div
      v-if="editorOpen"
      class="fixed inset-0 z-50 flex items-center justify-center bg-black/70 p-4"
      @click.self="editorOpen = false"
    >
      <UiCard padding="md" class="w-full max-w-lg">
        <template #header>
          <span class="font-medium">{{ editingId === null ? '新建任务' : `编辑任务 #${editingId}` }}</span>
        </template>
        <div class="space-y-3">
          <div>
            <label class="mb-1 block text-xs text-zinc-500">任务名</label>
            <UiInput v-model="draft.name" placeholder="每日 4 点备份" clearable />
          </div>
          <div>
            <label class="mb-1 block text-xs text-zinc-500">Cron（分 时 日 月 周）</label>
            <UiInput v-model="draft.schedule" class="font-mono" placeholder="0 4 * * *" clearable />
          </div>
          <div>
            <label class="mb-1 block text-xs text-zinc-500">动作</label>
            <UiSelect v-model="draft.action" :options="actionOptions" />
          </div>
          <div>
            <label class="mb-1 block text-xs text-zinc-500">目标实例</label>
            <UiSelect v-model="draft.instance_id" :options="instanceOptions" searchable />
          </div>
          <div v-if="draft.action === 'command'">
            <label class="mb-1 block text-xs text-zinc-500">指令内容</label>
            <UiInput
              v-model="draft.command"
              class="font-mono"
              placeholder="/save"
              clearable
            />
          </div>
          <div class="flex items-center gap-2">
            <UiSwitch v-model="draft.enabled" />
            <span class="text-xs text-zinc-500">启用</span>
          </div>

          <UiAlert v-if="error" type="error" :title="error" />

          <div class="flex items-center justify-end gap-2 pt-1">
            <UiButton variant="ghost" @click="editorOpen = false">取消</UiButton>
            <UiButton :loading="busy" @click="submit">保存</UiButton>
          </div>
        </div>
      </UiCard>
    </div>
  </div>
</template>
