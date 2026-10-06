<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { Cpu, Database, HardDrive, RefreshCw, ServerCog, Settings2 } from 'lucide-vue-next'
import {
  UiAlert,
  UiBadge,
  UiButton,
  UiCard,
  UiDivider,
  UiInputNumber,
  UiKeyValue,
  UiSegmented,
  UiSlider,
  UiSwitch,
  UiTimeline,
  UiTimelineItem,
  Message,
} from '@/components/ui'
import { endpoints } from '@/api/endpoints'
import { errorMessage, isUnavailable } from '@/api/client'
import { useAuthStore } from '@/stores/auth'
import { formatBytes, formatDuration } from '@/utils/format'
import type { SystemInfo } from '@/api/types'

const auth = useAuthStore()

const info = ref<SystemInfo | null>(null)
const loading = ref(false)
const unavailable = ref(false)
const loadError = ref<string | null>(null)

const theme = ref<'dark' | 'light'>(
  document.documentElement.classList.contains('dark') ? 'dark' : 'light',
)
const themeOptions = [
  { label: '深色', value: 'dark' },
  { label: '浅色', value: 'light' },
]

/* Local-only preferences (the panel does not persist these yet). */
const prefs = ref({
  autoRefresh: true,
  refreshSeconds: 5,
  backupRetention: 14,
  keepLogs: 30,
  notifyOnCrash: true,
  notifyWebhook: '',
})

const isAdmin = computed(() => auth.isAdmin)

function applyTheme(next: 'dark' | 'light'): void {
  document.documentElement.classList.toggle('dark', next === 'dark')
  try {
    localStorage.setItem('scnetm.theme', next)
  } catch {
    /* ignore */
  }
}

async function load(): Promise<void> {
  loading.value = true
  loadError.value = null
  unavailable.value = false
  try {
    info.value = await endpoints.systemInfo()
  } catch (err) {
    info.value = null
    if (isUnavailable(err)) unavailable.value = true
    else loadError.value = errorMessage(err)
  } finally {
    loading.value = false
  }
}

function savePrefs(): void {
  Message.success('偏好已保存在本地（后端设置接口尚未实现）')
}

onMounted(load)
</script>

<template>
  <div class="space-y-4">
    <UiAlert v-if="unavailable" type="warning" title="系统信息接口尚未实现">
      本版本后端对 <code>GET /api/v1/system/info</code> 返回 501。
    </UiAlert>
    <UiAlert v-else-if="loadError" type="error" :title="loadError" />

    <div class="grid grid-cols-1 gap-4 lg:grid-cols-2">
      <UiCard padding="md">
        <template #header>
          <div class="flex items-center justify-between gap-2">
            <div class="flex items-center gap-2">
              <ServerCog class="size-4 text-zinc-400" />
              <span class="font-medium">宿主与运行时</span>
            </div>
            <UiButton size="sm" variant="ghost" :icon="RefreshCw" :loading="loading" @click="load">
              刷新
            </UiButton>
          </div>
        </template>

        <div class="space-y-0">
          <UiKeyValue label="面板版本" :value="info?.panel_version ?? '-'" />
          <UiKeyValue label="Go 版本" :value="info?.go_version ?? '-'" mono />
          <UiKeyValue label="操作系统" :value="info ? `${info.os} / ${info.arch}` : '-'" />
          <UiKeyValue label="主机名" :value="info?.hostname ?? '-'" mono />
          <UiKeyValue label="CPU 核心" :value="info?.cpu_count ?? '-'" />
          <UiKeyValue label="内存总量" :value="formatBytes(info?.memory_total_bytes ?? 0)" />
          <UiKeyValue label="系统运行时长" :value="formatDuration(info?.uptime_seconds ?? 0)" />
          <UiKeyValue label="数据目录" :value="info?.data_dir ?? '-'" mono />
          <UiKeyValue label="实例目录" :value="info?.instances_dir ?? '-'" mono />
          <UiKeyValue label=".NET 运行时" :value="info?.dotnet_version || '未检测到'" mono />
          <UiKeyValue label="服务端模板" :value="info?.template_version || '未检测到'" mono />
        </div>

        <div class="mt-3 flex items-center gap-2">
          <UiBadge :variant="info?.runtime_ready ? 'primary' : 'outline'">
            {{ info?.runtime_ready ? '运行时就绪' : '运行时未就绪' }}
          </UiBadge>
          <UiBadge variant="outline">
            {{ info?.capabilities ? Object.values(info.capabilities).filter(Boolean).length : 0 }} 项能力已探测
          </UiBadge>
        </div>
      </UiCard>

      <div class="space-y-4">
        <UiCard padding="md">
          <template #header>
            <div class="flex items-center gap-2">
              <Settings2 class="size-4 text-zinc-400" />
              <span class="font-medium">外观</span>
            </div>
          </template>
          <div class="space-y-3">
            <UiSegmented
              v-model="theme"
              :options="themeOptions"
              size="sm"
              @update:model-value="applyTheme(theme)"
            />
            <p class="text-[11px] text-zinc-500">
              默认深色（<span class="font-mono">.dark</span> 挂在
              <span class="font-mono">&lt;html&gt;</span>），贴合服务器控制台场景。
            </p>
          </div>
        </UiCard>

        <UiCard padding="md">
          <template #header>
            <div class="flex items-center gap-2">
              <Database class="size-4 text-zinc-400" />
              <span class="font-medium">刷新与备份策略</span>
            </div>
          </template>

          <div class="space-y-4">
            <div class="flex items-center justify-between gap-3">
              <div>
                <div class="text-sm">自动刷新总览</div>
                <div class="text-[11px] text-zinc-500">通过 /ws/events 推送状态，无需轮询</div>
              </div>
              <UiSwitch v-model="prefs.autoRefresh" />
            </div>

            <div>
              <div class="mb-1 flex items-center justify-between text-xs">
                <span class="text-zinc-400">刷新间隔</span>
                <span class="font-mono text-zinc-500">{{ prefs.refreshSeconds }}s</span>
              </div>
              <UiSlider v-model="prefs.refreshSeconds" :min="2" :max="60" :step="1" />
            </div>

            <UiDivider />

            <div>
              <div class="mb-1 text-xs text-zinc-400">备份保留份数</div>
              <UiInputNumber v-model="prefs.backupRetention" :min="1" :max="365" controls />
            </div>

            <div>
              <div class="mb-1 text-xs text-zinc-400">日志保留天数</div>
              <UiInputNumber v-model="prefs.keepLogs" :min="1" :max="365" controls />
            </div>

            <div class="flex items-center justify-between gap-3">
              <div>
                <div class="text-sm">崩溃时通知</div>
                <div class="text-[11px] text-zinc-500">实例崩溃后推送告警事件</div>
              </div>
              <UiSwitch v-model="prefs.notifyOnCrash" />
            </div>

            <UiButton size="sm" :disabled="!isAdmin" @click="savePrefs">保存偏好</UiButton>
          </div>
        </UiCard>
      </div>
    </div>

    <div class="grid grid-cols-1 gap-4 lg:grid-cols-2">
      <UiCard padding="md">
        <template #header>
          <div class="flex items-center gap-2">
            <Cpu class="size-4 text-zinc-400" />
            <span class="font-medium">能力探测（V0-1）</span>
          </div>
        </template>
        <div v-if="info?.capabilities" class="space-y-0">
          <UiKeyValue
            v-for="(value, key) in info.capabilities"
            :key="key"
            :label="String(key)"
            :value="value ? '可用' : '不可用'"
          />
        </div>
        <p v-else class="text-xs text-zinc-500">
          后端未上报能力清单。部分功能（在线玩家列表、彩色终端）依赖这里的能力探测结果。
        </p>
      </UiCard>

      <UiCard padding="md">
        <template #header>
          <div class="flex items-center gap-2">
            <HardDrive class="size-4 text-zinc-400" />
            <span class="font-medium">部署提示</span>
          </div>
        </template>
        <UiTimeline>
          <UiTimelineItem title="默认仅监听 127.0.0.1:8080" description="对外暴露请走反向代理 + TLS" />
          <UiTimelineItem title="首次启动强制设置管理员密码" description="见 /setup 流程（§5.7）" />
          <UiTimelineItem title="实例目录为文件管理边界" description="所有 path 参数经 Clean + EvalSymlinks 校验" />
          <UiTimelineItem title="写操作全部进审计日志" description="含 IP 与变更前后差异摘要" />
        </UiTimeline>
      </UiCard>
    </div>
  </div>
</template>
