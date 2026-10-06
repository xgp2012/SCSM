<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { RouterLink, useRoute, useRouter } from 'vue-router'
import {
  AlertTriangle,
  ArrowLeft,
  CircleStop,
  Play,
  RefreshCw,
  Trash2,
} from 'lucide-vue-next'
import {
  UiAlert,
  UiBadge,
  UiButton,
  UiCard,
  UiLoading,
  UiPopconfirm,
  UiResult,
  UiTabs,
  Message,
} from '@/components/ui'
import { errorMessage, isUnavailable } from '@/api/client'
import { useInstancesStore } from '@/stores/instances'
import { useAuthStore } from '@/stores/auth'
import { formatBytes, formatDuration } from '@/utils/format'
import InstanceConsoleTab from '@/components/instance/InstanceConsoleTab.vue'
import InstanceConfigTab from '@/components/instance/InstanceConfigTab.vue'
import InstanceWorldsTab from '@/components/instance/InstanceWorldsTab.vue'
import InstanceFilesTab from '@/components/instance/InstanceFilesTab.vue'
import InstanceMonitorTab from '@/components/instance/InstanceMonitorTab.vue'
import StatusLight from '@/components/instance/StatusLight.vue'

const route = useRoute()
const router = useRouter()
const instances = useInstancesStore()
const auth = useAuthStore()

const instanceId = computed(() => String(route.params.id ?? ''))
const detailUnavailable = ref(false)
const loadError = ref<string | null>(null)

const tab = ref<string>(typeof route.query.tab === 'string' ? route.query.tab : 'console')

const tabs = [
  { label: '控制台', value: 'console' },
  { label: '配置', value: 'config' },
  { label: '存档', value: 'worlds' },
  { label: '文件', value: 'files' },
  { label: '监控', value: 'monitor' },
]

const instance = computed(() => instances.byId(instanceId.value))

const canOperate = computed(() => auth.canOperate)
const busy = ref(false)

watch(tab, (value) => {
  void router.replace({ query: { ...route.query, tab: value } })
})

async function load(): Promise<void> {
  loadError.value = null
  detailUnavailable.value = false
  try {
    await instances.fetchOne(instanceId.value)
  } catch (err) {
    if (isUnavailable(err)) detailUnavailable.value = true
    else loadError.value = errorMessage(err)
  }
}

async function runAction(action: () => Promise<void>, success: string): Promise<void> {
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

async function handleDelete(): Promise<void> {
  busy.value = true
  try {
    await instances.remove(instanceId.value)
    Message.success('实例已删除')
    await router.push({ name: 'overview' })
  } catch (err) {
    Message.error(errorMessage(err))
  } finally {
    busy.value = false
  }
}

onMounted(load)
watch(instanceId, load)
</script>

<template>
  <div class="space-y-4">
    <div class="flex flex-wrap items-center justify-between gap-3">
      <div class="flex min-w-0 items-center gap-3">
        <RouterLink :to="{ name: 'overview' }">
          <UiButton variant="ghost" size="sm" :icon="ArrowLeft">返回</UiButton>
        </RouterLink>
        <StatusLight :status="instance?.status ?? 'unknown'" />
        <div class="min-w-0">
          <div class="flex items-center gap-2">
            <h2 class="truncate text-lg font-semibold">{{ instance?.name ?? instanceId }}</h2>
            <UiBadge variant="outline">:{{ instance?.port ?? '-' }}</UiBadge>
          </div>
          <div class="truncate text-xs text-zinc-500">
            {{ instance?.dir ?? '实例目录未知' }}
          </div>
        </div>
      </div>

      <div class="flex flex-wrap items-center gap-2">
        <UiButton
          size="sm"
          variant="secondary"
          :icon="Play"
          :disabled="busy || !canOperate || instance?.status === 'running'"
          @click="runAction(() => instances.start(instanceId), '已下发起动指令')"
        >
          启动
        </UiButton>
        <UiButton
          size="sm"
          variant="secondary"
          :icon="CircleStop"
          :disabled="busy || !canOperate || instance?.status !== 'running'"
          @click="runAction(() => instances.stop(instanceId), '已下发停止指令')"
        >
          停止
        </UiButton>
        <UiButton
          size="sm"
          variant="secondary"
          :icon="RefreshCw"
          :disabled="busy || !canOperate"
          @click="runAction(() => instances.restart(instanceId), '已下发重启指令')"
        >
          重启
        </UiButton>
        <UiPopconfirm
          title="删除实例？"
          description="将移除实例记录，实例目录可选保留。"
          danger
          confirm-text="删除"
          @confirm="handleDelete"
        >
          <UiButton size="sm" variant="ghost" danger :icon="Trash2" :disabled="busy || !canOperate">
            删除
          </UiButton>
        </UiPopconfirm>
      </div>
    </div>

    <UiAlert v-if="!canOperate" type="info" title="只读账号">
      当前角色为 {{ auth.user?.role }}，启停与配置保存按钮已禁用。
    </UiAlert>

    <UiAlert v-if="detailUnavailable" type="warning" title="实例详情接口尚未实现">
      本版本后端对 <code>GET /api/v1/instances/:id</code> 返回 501。控制台与配置页仍可用（若后端已挂载对应路由）。
    </UiAlert>
    <UiAlert v-else-if="loadError" type="error" :title="loadError" />

    <div v-if="instance" class="grid grid-cols-2 gap-3 md:grid-cols-4">
      <UiCard padding="sm">
        <div class="text-xs text-zinc-500">在线玩家</div>
        <div class="text-xl font-semibold">
          {{ instance.players_online ?? 0 }}
          <span class="text-sm text-zinc-500">/ {{ instance.players_max ?? '-' }}</span>
        </div>
      </UiCard>
      <UiCard padding="sm">
        <div class="text-xs text-zinc-500">CPU</div>
        <div class="text-xl font-semibold">{{ (instance.cpu_percent ?? 0).toFixed(1) }}%</div>
      </UiCard>
      <UiCard padding="sm">
        <div class="text-xs text-zinc-500">内存</div>
        <div class="text-xl font-semibold">{{ formatBytes(instance.memory_bytes) }}</div>
      </UiCard>
      <UiCard padding="sm">
        <div class="text-xs text-zinc-500">运行时长</div>
        <div class="text-xl font-semibold">{{ formatDuration(instance.uptime_seconds) }}</div>
      </UiCard>
    </div>

    <UiTabs v-model="tab" :options="tabs" variant="line" />

    <UiLoading :loading="busy">
      <InstanceConsoleTab v-if="tab === 'console'" :instance-id="instanceId" />
      <InstanceConfigTab v-else-if="tab === 'config'" :instance-id="instanceId" />
      <InstanceWorldsTab v-else-if="tab === 'worlds'" :instance-id="instanceId" />
      <InstanceFilesTab v-else-if="tab === 'files'" :instance-id="instanceId" />
      <InstanceMonitorTab v-else-if="tab === 'monitor'" :instance-id="instanceId" />
      <UiResult
        v-else
        status="info"
        title="未知标签页"
        description="请从上方标签栏选择一个页面。"
      >
        <template #icon><AlertTriangle class="size-6" /></template>
      </UiResult>
    </UiLoading>
  </div>
</template>
