<template>
  <div v-if="instance" class="flex flex-col gap-6">
    <div class="flex flex-wrap items-start justify-between gap-4">
      <div>
        <div class="flex items-center gap-3">
          <h2 class="text-xl font-semibold tracking-tight">{{ instance.name }}</h2>
          <StatusDot :status="instance.runtime.status" />
        </div>
        <p class="mt-1 text-sm text-zinc-500">
          :{{ instance.serverPort }} · {{ instance.version }} · {{ instance.dir }}
        </p>
      </div>

      <div class="flex flex-wrap gap-2">
        <Button
          v-if="canStart"
          variant="primary"
          :loading="busy === 'start'"
          @click="onAction('start')"
        >
          启动
        </Button>
        <Button
          v-if="canStop"
          variant="outline"
          :loading="busy === 'stop'"
          @click="onAction('stop')"
        >
          停止
        </Button>
        <Button v-if="isRunning" variant="ghost" :loading="busy === 'restart'" @click="onAction('restart')">
          重启
        </Button>
        <Button v-if="canStop" variant="ghost" danger @click="onKill">强杀</Button>
      </div>
    </div>

    <div class="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
      <StatTile label="状态" :value="statusText" />
      <StatTile
        label="在线人数"
        :value="isRunning ? `${instance.runtime.online ?? 0} / ${instance.runtime.maxOnline ?? instance.maxPlayers}` : '—'"
      />
      <StatTile label="CPU / 内存" :value="isRunning ? `${instance.runtime.cpu ?? 0}% · ${instance.runtime.memMB ?? 0}MB` : '—'" />
      <StatTile label="运行端口" :value="String(instance.runtime.serverPort ?? instance.serverPort)" />
    </div>

    <div class="rounded-lg border border-zinc-800 bg-zinc-900/30">
      <div class="flex items-center gap-4 border-b border-zinc-800 px-4 text-sm">
        <button
          v-for="tab in tabs"
          :key="tab.key"
          class="border-b-2 py-3 transition"
          :class="
            active === tab.key
              ? 'border-emerald-500 text-zinc-100'
              : 'border-transparent text-zinc-500 hover:text-zinc-300'
          "
          @click="active = tab.key"
        >
          {{ tab.label }}
        </button>
      </div>

      <div class="p-4 text-sm text-zinc-400">
        <div v-if="active === 'console'">
          <TerminalView :instance-id="id" />
        </div>

        <div v-else-if="active === 'settings'">
          <p class="mb-3 text-zinc-500">启停相关设置。</p>
          <div class="grid max-w-md gap-3">
            <label class="flex items-center gap-2">
              <input v-model="edit.autoRestart" type="checkbox" class="accent-emerald-500" >
              崩溃后自动重启
            </label>
            <label class="flex items-center gap-2">
              <input v-model="edit.autoRun" type="checkbox" class="accent-emerald-500" >
              无人值守开服（Autorun）
            </label>
            <div class="flex items-center gap-3">
              <span>端口</span>
              <input
                v-model.number="edit.serverPort"
                type="number"
                class="w-32 rounded-md border border-zinc-700 bg-zinc-950 px-3 py-1.5 outline-none focus:border-emerald-500"
                :disabled="isRunning"
              >
            </div>
            <Button variant="primary" size="sm" :loading="saving" @click="onSave">保存</Button>
          </div>
        </div>

        <div v-else-if="active === 'config'">
          <ConfigPanel :instance-id="id" />
        </div>

        <div v-else-if="active === 'backups'">
          <BackupPanel :instance-id="id" :running="isRunning" />
        </div>

        <div v-else-if="active === 'players'">
          <div class="mb-3 flex items-center gap-3">
            <Button variant="outline" size="sm" :loading="probing" @click="onProbe">立即探测</Button>
            <span v-if="probeAt" class="text-xs text-zinc-500">最近探测：{{ probeAt }}</span>
          </div>
          <div v-if="probe" class="grid max-w-lg grid-cols-2 gap-3 text-sm">
            <div>
              <div class="text-xs text-zinc-500">探测状态</div>
              <div :class="probe.online ? 'text-emerald-400' : 'text-amber-400'">
                {{ probe.online ? '在线' : probe.error === 'timeout' ? '无响应' : '离线' }}
              </div>
            </div>
            <div>
              <div class="text-xs text-zinc-500">版本</div>
              <div class="text-zinc-200">{{ probe.version ?? '—' }}</div>
            </div>
            <div>
              <div class="text-xs text-zinc-500">玩家</div>
              <div class="text-zinc-200">{{ probe.playerCount ?? 0 }} / {{ probe.maxCount ?? instance.maxPlayers }}</div>
            </div>
            <div>
              <div class="text-xs text-zinc-500">游戏模式</div>
              <div class="text-zinc-200">{{ probe.gameMode ?? '—' }}</div>
            </div>
            <div>
              <div class="text-xs text-zinc-500">需要密码</div>
              <div class="text-zinc-200">{{ probe.needPassword === undefined ? '—' : probe.needPassword ? '是' : '否' }}</div>
            </div>
            <div>
              <div class="text-xs text-zinc-500">延迟</div>
              <div class="text-zinc-200">{{ probe.pingMs !== undefined ? `${probe.pingMs} ms` : '—' }}</div>
            </div>
          </div>
          <p v-else class="text-zinc-500">点击「立即探测」向实例 ServerPort 发送 UDP ServerInfo 请求。</p>
        </div>
      </div>
    </div>
  </div>

  <p v-else class="text-sm text-zinc-500">加载实例…</p>
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, reactive, ref, watch } from 'vue';
import { useRoute } from 'vue-router';
import { Button } from 'fuxsto-design';
import { Dialog } from '~/components/ui/dialog';
import type { Instance, InstanceAction, ProbeResult } from '@sc-panel/shared';
import { api, ApiRequestError } from '~/composables/useApi';
import StatusDot from '~/components/instance/StatusDot.vue';
import StatTile from '~/components/instance/StatTile.vue';
import TerminalView from '~/components/console/TerminalView.vue';
import ConfigPanel from '~/components/instance/ConfigPanel.vue';
import BackupPanel from '~/components/instance/BackupPanel.vue';

const route = useRoute();
const id = route.params.id as string;

const instance = ref<Instance | null>(null);
const busy = ref<InstanceAction | null>(null);
const saving = ref(false);
const active = ref('console');
const probe = ref<ProbeResult | null>(null);
const probing = ref(false);
const probeAt = ref<string | null>(null);

const tabs = [
  { key: 'console', label: '终端' },
  { key: 'config', label: '配置' },
  { key: 'settings', label: '设置' },
  { key: 'backups', label: '存档与备份' },
  { key: 'players', label: '玩家' },
];

const edit = reactive({ autoRestart: false, autoRun: false, serverPort: 0 });

const status = computed(() => instance.value?.runtime.status ?? 'stopped');
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

async function load(): Promise<void> {
  instance.value = await api.get<Instance>(`/instances/${id}`);
  edit.autoRestart = instance.value.autoRestart;
  edit.autoRun = instance.value.autoRun;
  edit.serverPort = instance.value.serverPort;
}

let timer: ReturnType<typeof setInterval> | undefined;

onMounted(async () => {
  await load();
  // M2 前用轮询近似实时状态
  timer = setInterval(() => void load().catch(() => {}), 3000);
});

onUnmounted(() => {
  if (timer) clearInterval(timer);
});

// 打开「玩家」标签时自动探测一次
watch(active, (tab) => {
  if (tab === 'players' && isRunning.value) void onProbe();
});

async function onAction(action: InstanceAction): Promise<void> {
  busy.value = action;
  try {
    instance.value = await api.post<Instance>(`/instances/${id}/${action}`);
  } catch (err) {
    Dialog.error({ title: '操作失败', content: err instanceof ApiRequestError ? err.message : '未知错误' });
  } finally {
    busy.value = null;
  }
}

async function onProbe(): Promise<void> {
  probing.value = true;
  try {
    probe.value = await api.get<ProbeResult>(`/instances/${id}/probe`);
    probeAt.value = new Date().toLocaleTimeString();
  } catch (err) {
    Dialog.error({ title: '探测失败', content: err instanceof ApiRequestError ? err.message : '未知错误' });
  } finally {
    probing.value = false;
  }
}

function onKill(): void {
  Dialog.confirm({
    title: '强制终止',
    content: '确定强制终止该实例？未保存的世界数据可能丢失。',
    confirmText: '强杀',
    danger: true,
    onConfirm: () => void onAction('kill'),
  });
}

async function onSave(): Promise<void> {
  if (isRunning.value && edit.serverPort !== instance.value?.serverPort) {
    Dialog.error({ title: '无法保存', content: '实例运行中不可修改端口，请先停止。' });
    return;
  }
  saving.value = true;
  try {
    instance.value = await api.patch<Instance>(`/instances/${id}`, {
      autoRestart: edit.autoRestart,
      autoRun: edit.autoRun,
      serverPort: edit.serverPort,
    });
    Dialog.success({ title: '已保存', content: '实例设置已写入配置文件。' });
  } catch (err) {
    Dialog.error({ title: '保存失败', content: err instanceof ApiRequestError ? err.message : '未知错误' });
  } finally {
    saving.value = false;
  }
}
</script>
