<script setup lang="ts">
import { computed, ref } from 'vue'
import { CornerDownLeft, History, Terminal as TerminalIcon } from 'lucide-vue-next'
import { UiButton, UiInput, UiTooltip } from '@/components/ui'
import { cn } from '@/components/ui/cn'

const props = withDefaults(
  defineProps<{
    disabled?: boolean
    /** Quick commands rendered as one-click buttons. */
    quickCommands?: string[]
  }>(),
  {
    disabled: false,
    // NOTE (plan §2.5 verified on the real server): the bare `/player` command
    // returns only its help text — the working form is `/player list 0`.
    quickCommands: () => ['/player list 0', '/time', '/stop'],
  },
)

const emit = defineEmits<{ send: [command: string]; historyChange: [commands: string[]] }>()

const draft = ref('')
const history = ref<string[]>([])
const historyIndex = ref(-1)

const canSend = computed(() => draft.value.trim().length > 0 && !props.disabled)

function pushHistory(command: string): void {
  const trimmed = command.trim()
  history.value = [trimmed, ...history.value.filter((c) => c !== trimmed)].slice(0, 200)
  historyIndex.value = -1
  emit('historyChange', history.value)
}

function submit(command = draft.value): void {
  const trimmed = command.trim()
  if (!trimmed) return
  emit('send', trimmed)
  pushHistory(trimmed)
  draft.value = ''
}

/** Up/Down walk the command history like a shell prompt. */
function navigate(direction: -1 | 1): void {
  if (history.value.length === 0) return
  const next = historyIndex.value + direction
  if (next < 0) {
    historyIndex.value = -1
    draft.value = ''
    return
  }
  if (next >= history.value.length) return
  historyIndex.value = next
  draft.value = history.value[next]
}

function onKeydown(event: KeyboardEvent): void {
  if (event.key === 'ArrowUp') {
    event.preventDefault()
    navigate(-1)
    return
  }
  if (event.key === 'ArrowDown') {
    event.preventDefault()
    navigate(1)
    return
  }
  if (event.key === 'Enter' && !event.shiftKey) {
    event.preventDefault()
    submit()
  }
}
</script>

<template>
  <div class="space-y-2">
    <div class="flex items-center gap-2">
      <div class="relative min-w-0 flex-1">
        <TerminalIcon class="pointer-events-none absolute left-2.5 top-2.5 size-4 text-zinc-500" />
        <UiInput
          v-model="draft"
          class="pl-8 font-mono"
          placeholder="输入指令后回车，例如 /player list 0（↑↓ 调出历史）"
          :disabled="disabled"
          clearable
          @keydown="onKeydown"
        />
      </div>
      <UiButton :disabled="!canSend" :icon="CornerDownLeft" @click="submit()">发送</UiButton>
      <UiTooltip :content="`历史 ${history.length} 条（↑ 上一条，↓ 下一条）`">
        <span
          :class="
            cn(
              'inline-flex items-center gap-1 rounded-md border border-zinc-800 px-2 py-1.5 text-xs text-zinc-400',
            )
          "
        >
          <History class="size-3.5" />
          {{ history.length }}
        </span>
      </UiTooltip>
    </div>

    <div class="flex flex-wrap items-center gap-2">
      <span class="text-xs text-zinc-500">快捷指令</span>
      <UiButton
        v-for="command in quickCommands"
        :key="command"
        size="sm"
        variant="secondary"
        :disabled="disabled"
        @click="submit(command)"
      >
        <span class="font-mono">{{ command }}</span>
      </UiButton>
      <span class="text-[11px] text-zinc-600">
        提示：裸 <span class="font-mono">/player</span> 只返回帮助文本，正确形式是
        <span class="font-mono">/player list 0</span>
      </span>
    </div>
  </div>
</template>
