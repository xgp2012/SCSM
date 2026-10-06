<script setup lang="ts">
import { computed } from 'vue'
import { CircleAlert, Palette, PaletteIcon } from 'lucide-vue-next'
import { UiTooltip } from '@/components/ui'
import { cn } from '@/components/ui/cn'
import type { ColorMode } from '@/api/types'

const props = defineProps<{
  mode: ColorMode
  reason?: string | null
  /** Reconnect button state. */
  reconnecting?: boolean
}>()

/**
 * D1 acceptance point: the panel must make it obvious whether the console is
 * receiving a true PTY (ANSI colour preserved) or has degraded to a plain pipe.
 */
const isEnhanced = computed(() => props.mode === 'enhanced')

const label = computed(() => (isEnhanced.value ? '彩色 / enhanced' : '无色 / basic (降级)'))

const tooltip = computed(() =>
  isEnhanced.value
    ? 'PTY 通道正常：ANSI 颜色与控制序列完整保留（D1 验收点）'
    : `终端降级为基础模式，ANSI 颜色丢失。原因：${props.reason || '未提供（后端未挂载 PTY 或平台不支持）'}`,
)
</script>

<template>
  <UiTooltip :content="tooltip">
    <span
      :class="
        cn(
          'inline-flex select-none items-center gap-1.5 rounded-full border px-2.5 py-1 text-xs font-medium',
          isEnhanced
            ? 'border-emerald-800 bg-emerald-950/60 text-emerald-300'
            : 'border-amber-800 bg-amber-950/60 text-amber-300',
        )
      "
      data-testid="color-mode-badge"
      :data-color-mode="mode"
    >
      <Palette v-if="isEnhanced" class="size-3.5" />
      <PaletteIcon v-else class="size-3.5" />
      <CircleAlert v-if="!isEnhanced" class="size-3.5" />
      <span>{{ label }}</span>
      <span v-if="reconnecting" class="text-[10px] opacity-70">重连中…</span>
    </span>
  </UiTooltip>
</template>
