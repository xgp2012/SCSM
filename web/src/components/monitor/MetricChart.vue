<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref, watch } from 'vue'
import uPlot from 'uplot'
import 'uplot/dist/uPlot.min.css'

export interface Series {
  label: string
  data: number[]
  color: string
  /** Right-hand axis (used for the player-count series). */
  scale?: 'y' | 'y2'
  /** Human formatter for the tooltip/axis, e.g. bytes. */
  format?: (value: number) => string
}

const props = withDefaults(
  defineProps<{
    /** Unix timestamps in seconds. */
    timestamps: number[]
    series: Series[]
    height?: number
    /** Render a stacked area chart instead of lines. */
    area?: boolean
  }>(),
  { height: 220, area: true },
)

const host = ref<HTMLDivElement | null>(null)
const empty = ref(true)
let chart: uPlot | null = null

function buildData(): uPlot.AlignedData {
  const columns: (number | null)[][] = [props.timestamps]
  for (const item of props.series) columns.push(item.data)
  return columns as unknown as uPlot.AlignedData
}

function buildOptions(): uPlot.Options {
  const hasRight = props.series.some((s) => s.scale === 'y2')
  return {
    width: host.value?.clientWidth ?? 640,
    height: props.height,
    padding: [8, hasRight ? 8 : 8, 0, 0],
    cursor: {
      y: false,
      drag: { x: true, y: false, setScale: false },
    },
    legend: { show: true, live: true },
    scales: {
      x: { time: true },
      y: { auto: true },
      ...(hasRight ? { y2: { auto: true } } : {}),
    },
    axes: [
      {
        stroke: '#71717a',
        grid: { stroke: '#27272a', width: 1 },
        ticks: { stroke: '#3f3f46' },
        font: '11px ui-sans-serif, system-ui',
      },
      {
        stroke: '#71717a',
        grid: { stroke: '#27272a', width: 1 },
        ticks: { stroke: '#3f3f46' },
        font: '11px ui-sans-serif, system-ui',
        size: 46,
      },
      ...(hasRight
        ? [
            {
              scale: 'y2' as const,
              side: 1 as const,
              stroke: '#71717a',
              grid: { show: false },
              ticks: { stroke: '#3f3f46' },
              font: '11px ui-sans-serif, system-ui',
              size: 40,
            },
          ]
        : []),
    ],
    series: [
      { label: '时间' },
      ...props.series.map((item) => ({
        label: item.label,
        stroke: item.color,
        width: 1.6,
        scale: item.scale ?? 'y',
        fill:
          props.area && (item.scale ?? 'y') === 'y'
            ? `${item.color}22`
            : undefined,
        points: { show: false },
        value: (_u: uPlot, value: number | null) =>
          value === null || value === undefined
            ? '-'
            : (item.format ? item.format(value) : String(Math.round(value * 10) / 10)),
      })),
    ],
  }
}

function render(): void {
  const hasData = props.timestamps.length > 1 && props.series.some((s) => s.data.length > 1)
  empty.value = !hasData
  if (!host.value) return
  if (!hasData) {
    chart?.destroy()
    chart = null
    return
  }
  chart?.destroy()
  chart = new uPlot(buildOptions(), buildData(), host.value)
}

function resize(): void {
  if (!chart || !host.value) return
  const width = host.value.clientWidth
  if (width > 0) chart.setSize({ width, height: props.height })
}

let observer: ResizeObserver | null = null

onMounted(() => {
  render()
  observer = new ResizeObserver(() => resize())
  if (host.value) observer.observe(host.value)
})

onBeforeUnmount(() => {
  observer?.disconnect()
  observer = null
  chart?.destroy()
  chart = null
})

watch(() => [props.timestamps.length, props.series.map((s) => s.data.length).join(',')], render)
</script>

<template>
  <div class="relative w-full">
    <div ref="host" class="w-full" data-testid="uplot-host" />
    <div
      v-if="empty"
      class="pointer-events-none absolute inset-0 flex items-center justify-center text-xs text-zinc-500"
    >
      暂无指标样本（需要实例运行并上报 stats）
    </div>
  </div>
</template>
