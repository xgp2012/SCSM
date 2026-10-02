<template>
  <div class="flex flex-col gap-3 rounded-lg border border-zinc-800 bg-zinc-900/40 p-4">
    <div class="flex items-start justify-between gap-2">
      <div class="min-w-0">
        <NuxtLink
          :to="`/instances/${instance.id}`"
          class="block truncate font-medium hover:text-emerald-400"
        >
          {{ instance.name }}
        </NuxtLink>
        <p class="mt-0.5 truncate text-xs text-zinc-500">
          :{{ instance.serverPort }} · {{ instance.version }}
        </p>
      </div>
      <StatusDot :status="instance.runtime.status" />
    </div>

    <dl class="grid grid-cols-2 gap-2 text-xs text-zinc-400">
      <div>
        <dt class="text-zinc-500">状态</dt>
        <dd class="flex items-center gap-1 text-zinc-200">
          {{ statusText }}
          <span
            v-if="isRunning"
            class="inline-block h-1.5 w-1.5 rounded-full"
            :class="instance.runtime.probeOnline ? 'bg-emerald-400' : 'bg-amber-400'"
            :title="instance.runtime.probeOnline ? 'UDP 探测在线' : 'UDP 探测无响应'"
          />
        </dd>
      </div>
      <div>
        <dt class="text-zinc-500">在线人数</dt>
        <dd class="text-zinc-200">
          {{ isRunning ? `${instance.runtime.online ?? 0}/${instance.runtime.maxOnline ?? instance.maxPlayers}` : '—' }}
        </dd>
      </div>
      <div>
        <dt class="text-zinc-500">CPU</dt>
        <dd class="text-zinc-200">{{ isRunning ? `${instance.runtime.cpu ?? 0}%` : '—' }}</dd>
      </div>
      <div>
        <dt class="text-zinc-500">内存</dt>
        <dd class="text-zinc-200">{{ isRunning ? `${instance.runtime.memMB ?? 0} MB` : '—' }}</dd>
      </div>
      <div>
        <dt class="text-zinc-500">游戏模式</dt>
        <dd class="text-zinc-200">{{ isRunning ? instance.runtime.gameMode ?? '—' : '—' }}</dd>
      </div>
      <div>
        <dt class="text-zinc-500">PID</dt>
        <dd class="text-zinc-200">{{ instance.runtime.pid ?? '—' }}</dd>
      </div>
    </dl>

    <div class="mt-1 flex flex-wrap gap-2">
      <Button
        v-if="canStart"
        variant="primary"
        size="sm"
        :loading="busy"
        @click="emit('action', 'start')"
      >
        启动
      </Button>
      <Button v-if="canStop" variant="outline" size="sm" :loading="busy" @click="emit('action', 'stop')">
        停止
      </Button>
      <Button v-if="isRunning" variant="ghost" size="sm" @click="emit('action', 'restart')">
        重启
      </Button>
      <Button v-if="canStop" variant="ghost" danger size="sm" @click="onKill">强杀</Button>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue';
import { Button } from 'fuxsto-design';
import { Dialog } from '~/components/ui/dialog';
import type { Instance, InstanceAction } from '@sc-panel/shared';
import StatusDot from './StatusDot.vue';

const props = defineProps<{ instance: Instance }>();
const emit = defineEmits<{ action: [InstanceAction] }>();

const busy = ref(false);

const status = computed(() => props.instance.runtime.status);
const isRunning = computed(() => status.value === 'running');
const canStart = computed(() => status.value === 'stopped' || status.value === 'crashed');
const canStop = computed(() => isRunning.value || status.value === 'starting');

const statusText = computed(
  () =>
    ({
      stopped: '已停止',
      starting: '启动中',
      running: '运行中',
      stopping: '停止中',
      crashed: '已崩溃',
    })[status.value],
);

function onKill(): void {
  Dialog.confirm({
    title: '强制终止',
    content: `确定强制终止实例「${props.instance.name}」？未保存的世界数据可能丢失。`,
    confirmText: '强杀',
    danger: true,
    onConfirm: () => {
      emit('action', 'kill');
    },
  });
}
</script>
