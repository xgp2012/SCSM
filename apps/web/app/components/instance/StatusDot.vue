<template>
  <span class="inline-flex items-center gap-1.5 text-xs" :class="textClass">
    <span class="inline-block h-2 w-2 rounded-full" :class="dotClass" />
    {{ label }}
  </span>
</template>

<script setup lang="ts">
import { computed } from 'vue';
import type { InstanceStatus } from '@sc-panel/shared';

const props = defineProps<{ status: InstanceStatus }>();

const label = computed(
  () =>
    ({
      stopped: '已停止',
      starting: '启动中',
      running: '运行中',
      stopping: '停止中',
      crashed: '已崩溃',
    })[props.status],
);

const dotClass = computed(
  () =>
    ({
      stopped: 'bg-zinc-500',
      starting: 'bg-amber-400 animate-pulse',
      running: 'bg-emerald-400',
      stopping: 'bg-amber-400 animate-pulse',
      crashed: 'bg-red-500',
    })[props.status],
);

const textClass = computed(
  () =>
    ({
      stopped: 'text-zinc-400',
      starting: 'text-amber-300',
      running: 'text-emerald-300',
      stopping: 'text-amber-300',
      crashed: 'text-red-400',
    })[props.status],
);
</script>
