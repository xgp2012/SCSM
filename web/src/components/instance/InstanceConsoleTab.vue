<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import {
  ArrowDownToLine,
  Eraser,
  Pause,
  Play,
  RotateCw,
  Search,
  Terminal as TerminalIcon,
} from 'lucide-vue-next'
import {
  UiAlert,
  UiBadge,
  UiButton,
  UiCard,
  UiInput,
  UiSegmented,
  UiTooltip,
  Message,
} from '@/components/ui'
import { cn } from '@/components/ui/cn'
import XtermTerminal from '@/components/console/XtermTerminal.vue'
import ColorModeBadge from '@/components/console/ColorModeBadge.vue'
import CommandInput from '@/components/console/CommandInput.vue'
import PlainLogView from '@/components/console/PlainLogView.vue'
import { LOG_LEVEL_CHIP } from '@/components/console/levelTone'
import { filterLines, LOG_LEVELS, useConsoleSession } from '@/composables/useConsole'
import { downloadText, stripAnsi } from '@/utils/format'
import { endpoints } from '@/api/endpoints'
import type { LogLevel } from '@/api/types'

const props = defineProps<{ instanceId: string }>()

const instanceIdRef = computed(() => props.instanceId)
const session = useConsoleSession(instanceIdRef)

const terminalRef = ref<InstanceType<typeof XtermTerminal> | null>(null)
/** Chunks fed to xterm (raw, ANSI preserved). */
const chunks = ref<string[]>([])
/** Pause only stops the xterm view from growing; the WS keeps streaming. */
const paused = ref(false)
const pendingWhilePaused = ref(0)
const view = ref<'terminal' | 'plain'>('terminal')
const level = ref<LogLevel | 'all'>('all')
const filter = ref('')
const cols = ref(0)
const rows = ref(0)

const viewOptions = [
  { label: '终端 (ANSI)', value: 'terminal' },
  { label: '纯文本', value: 'plain' },
]

session.onOutput((chunk) => {
  if (paused.value) {
    pendingWhilePaused.value += 1
    return
  }
  appendChunk(chunk)
})

function appendChunk(chunk: string): void {
  chunks.value = [...chunks.value, chunk]
}

const plainLines = computed(() => filterLines(session.plainText.value, level.value))

const visibleLines = computed(() => {
  const needle = filter.value.trim().toLowerCase()
  const lines = plainLines.value
  if (!needle) return lines
  return lines.filter((line) => line.text.toLowerCase().includes(needle))
})

const levelCounts = computed(() =>
  (['info', 'warn', 'error', 'fatal'] as LogLevel[]).map((key) => ({
    key,
    count: session.levels.value[key],
    chip: LOG_LEVEL_CHIP[key],
  })),
)

const connectionLabel = computed(() => {
  switch (session.state.value) {
    case 'open':
      return '已连接'
    case 'connecting':
      return '连接中'
    case 'reconnecting':
      return `重连中（第 ${session.reconnectAttempt.value} 次）`
    default:
      return '未连接'
  }
})

function handleResize(nextCols: number, nextRows: number): void {
  cols.value = nextCols
  rows.value = nextRows
  // Report the fitted size so the backend can call pty.Setsize (§8).
  session.sendResize(nextCols, nextRows)
}

function handleSend(command: string): void {
  session.send(command)
}

function clearTerminal(): void {
  terminalRef.value?.clear()
  Message.success('终端已清屏（仅前端）')
}

function togglePause(): void {
  paused.value = !paused.value
  if (!paused.value) {
    pendingWhilePaused.value = 0
  }
}

function downloadLog(): void {
  const text = stripAnsi(session.plainText.value)
  if (!text.trim()) {
    Message.warning('暂无可下载的日志内容')
    return
  }
  downloadText(`instance-${props.instanceId}-console.log`, text)
}

async function downloadServerLog(): Promise<void> {
  try {
    const url = endpoints.logsDownloadUrl(props.instanceId)
    const anchor = document.createElement('a')
    anchor.href = url
    anchor.download = ''
    document.body.appendChild(anchor)
    anchor.click()
    document.body.removeChild(anchor)
  } catch {
    Message.error('日志下载接口不可用')
  }
}

function reload(): void {
  terminalRef.value?.reset()
  chunks.value = []
  session.reconnect()
}

watch(
  () => props.instanceId,
  () => {
    chunks.value = []
    pendingWhilePaused.value = 0
  },
)
</script>

<template>
  <div class="space-y-3">
    <UiAlert v-if="session.unavailable.value" type="warning" title="控制台通道尚未实现">
      后端未挂载 <code>/ws/instances/:id/console</code>，或返回 501。窗口会持续自动重连。
    </UiAlert>

    <UiCard padding="sm">
      <div class="flex flex-wrap items-center gap-2">
        <ColorModeBadge
          :mode="session.colorMode.value"
          :reason="session.degradeReason.value"
          :reconnecting="session.state.value === 'reconnecting'"
        />

        <span
          :class="
            cn(
              'inline-flex items-center gap-1.5 rounded-full border px-2.5 py-1 text-xs',
              session.state.value === 'open'
                ? 'border-zinc-700 text-zinc-300'
                : 'border-amber-800 text-amber-300',
            )
          "
        >
          <span
            :class="
              cn(
                'size-1.5 rounded-full',
                session.state.value === 'open' ? 'bg-emerald-500' : 'bg-amber-500',
              )
            "
          />
          WS {{ connectionLabel }}
        </span>

        <UiBadge variant="outline" class="font-mono">
          {{ cols }}×{{ rows }}
        </UiBadge>
        <UiBadge variant="outline">行数 {{ chunks.length }}</UiBadge>

        <div class="flex-1" />

        <UiSegmented v-model="view" :options="viewOptions" size="sm" />

        <UiButton size="sm" variant="ghost" :icon="Eraser" @click="clearTerminal">清屏</UiButton>
        <UiTooltip :content="paused ? '恢复自动滚动' : '暂停自动滚动（后端仍在接收）'">
          <UiButton
            size="sm"
            variant="ghost"
            :icon="paused ? Play : Pause"
            :class="paused ? 'text-amber-300' : undefined"
            @click="togglePause"
          >
            {{ paused ? `已暂停 (${pendingWhilePaused})` : '自动滚动' }}
          </UiButton>
        </UiTooltip>
        <UiButton size="sm" variant="ghost" :icon="ArrowDownToLine" @click="downloadLog">
          下载日志
        </UiButton>
        <UiTooltip content="直接调用后端 /logs/download 接口（需要该路由已实现）">
          <UiButton size="sm" variant="ghost" @click="downloadServerLog">下载服务端日志</UiButton>
        </UiTooltip>
        <UiButton size="sm" variant="ghost" :icon="RotateCw" @click="reload">重连</UiButton>
      </div>

      <div class="mt-3 flex flex-wrap items-center gap-2 border-t border-zinc-800 pt-3">
        <span class="text-xs text-zinc-500">级别过滤</span>
        <button
          v-for="option in LOG_LEVELS"
          :key="option.value"
          type="button"
          :class="
            cn(
              'rounded-md px-2 py-1 text-xs transition-colors',
              level === option.value ? 'bg-zinc-700 text-zinc-100' : 'text-zinc-400 hover:bg-zinc-800',
            )
          "
          @click="level = option.value"
        >
          {{ option.label }}
        </button>

        <span
          v-for="item in levelCounts"
          :key="item.key"
          :class="cn('rounded-md px-2 py-0.5 font-mono text-[11px]', item.chip)"
        >
          {{ item.key }} {{ item.count }}
        </span>

        <div class="ml-auto flex items-center gap-2">
          <Search class="size-3.5 text-zinc-500" />
          <UiInput
            v-model="filter"
            size="sm"
            class="w-56"
            placeholder="过滤关键字（纯文本视图）"
            clearable
          />
        </div>
      </div>
    </UiCard>

    <div v-show="view === 'terminal'" class="h-[60vh] min-h-80">
      <XtermTerminal ref="terminalRef" :chunks="chunks" @resize="handleResize" />
    </div>

    <PlainLogView
      v-if="view === 'plain'"
      :lines="visibleLines"
      :height="480"
      empty-text="纯文本视图暂无内容（终端视图仍保留完整 ANSI 输出）"
    />

    <UiCard v-if="paused" padding="sm">
      <div class="flex items-center gap-2 text-xs text-amber-300">
        <TerminalIcon class="size-3.5" />
        自动滚动已暂停，期间收到 {{ pendingWhilePaused }} 个数据块；恢复后不会补写历史（xterm 的
        scrollback 仍然保留）。
      </div>
    </UiCard>

    <CommandInput
      :disabled="session.state.value !== 'open'"
      @send="handleSend"
    />
  </div>
</template>
