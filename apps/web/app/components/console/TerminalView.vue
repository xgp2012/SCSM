<template>
  <div class="flex flex-col gap-2">
    <div class="flex flex-wrap items-center gap-3 text-xs">
      <span class="inline-flex items-center gap-1.5">
        <span class="inline-block h-2 w-2 rounded-full" :class="dotClass" />
        <span :class="textClass">{{ statusLabel }}</span>
      </span>
      <span v-if="lastError" class="text-red-400">{{ lastError }}</span>

      <div class="ml-auto flex items-center gap-2">
        <button
          class="rounded border border-zinc-700 px-2 py-0.5 transition hover:border-zinc-500 hover:text-zinc-200"
          @click="clear"
        >
          清屏
        </button>
        <button
          class="rounded border border-zinc-700 px-2 py-0.5 transition hover:border-zinc-500 hover:text-zinc-200"
          :disabled="status === 'open'"
          @click="connect"
        >
          重连
        </button>
      </div>
    </div>

    <div ref="container" class="h-[26rem] w-full overflow-hidden rounded-md border border-zinc-800 bg-black p-1" />

    <!-- 纯文本镜像：供无障碍读屏与端到端测试断言真实控制台文本 -->
    <pre
      data-testid="console-mirror"
      class="sr-only"
      aria-live="polite"
    >{{ mirrorText }}</pre>

    <form class="flex items-center gap-2" @submit.prevent="onSubmit">
      <span class="font-mono text-sm text-emerald-400">/</span>
      <input
        v-model="input"
        class="flex-1 rounded-md border border-zinc-700 bg-zinc-950 px-3 py-1.5 font-mono text-sm outline-none focus:border-emerald-500"
        placeholder="输入命令后回车（自动补 /），例如 help / ls / save / stop"
        :disabled="status !== 'open'"
      >
      <Button variant="primary" size="sm" type="submit" :disabled="status !== 'open'">发送</Button>
    </form>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, onBeforeUnmount, ref, shallowRef } from 'vue';
import { Button } from 'fuxsto-design';
import { CONSOLE_HISTORY_LINES } from '@sc-panel/shared';
import { useConsoleSocket, type ConsoleStatus } from '~/composables/useConsoleSocket';

const props = defineProps<{ instanceId: string }>();

const container = ref<HTMLElement | null>(null);
const input = ref('');
const term = shallowRef<import('@xterm/xterm').Terminal | null>(null);
const fitAddon = shallowRef<import('@xterm/addon-fit').FitAddon | null>(null);
let resizeObserver: ResizeObserver | null = null;

/** 纯文本镜像（最近控制台行，供读屏/测试断言） */
const mirrorLines = ref<string[]>([]);
const mirrorText = computed(() => mirrorLines.value.join('\n'));
/** 未成行的尾部缓冲（原始终端流按 \r\n 拆分） */
let mirrorTail = '';

function pushMirror(line: string): void {
  const next = mirrorLines.value.concat(line);
  mirrorLines.value = next.length > 500 ? next.slice(next.length - 500) : next;
}

/** 去掉 ANSI 转义序列，保留可读文本（用于纯文本镜像） */
function stripAnsi(input: string): string {
  /* eslint-disable no-control-regex */
  return input
    .replace(/\x1b\][^\x07]*\x07/g, '')
    .replace(/\x1b\[[0-9;?]*[A-Za-z]/g, '')
    .replace(/\x1b[()][A-Z0-9]/g, '')
    .replace(/\x1b[=>]/g, '');
  /* eslint-enable no-control-regex */
}

function appendRawToMirror(data: string): void {
  const text = stripAnsi(data);
  const parts = text.split(/\r?\n/);
  if (parts.length === 1) {
    mirrorTail += parts[0];
    if (mirrorTail.length > 2000) mirrorTail = mirrorTail.slice(-2000);
    // 用不完整行刷新尾行，便于断言到即时回执
    if (mirrorTail.trim()) pushMirror(mirrorTail);
    return;
  }
  const first = mirrorTail + parts[0];
  if (first.trim()) pushMirror(first);
  for (const p of parts.slice(1, -1)) {
    if (p.trim()) pushMirror(p);
  }
  mirrorTail = parts[parts.length - 1] ?? '';
  if (mirrorTail.trim()) pushMirror(mirrorTail);
}

const socket = useConsoleSocket(props.instanceId, {
  onRaw: (data) => {
    term.value?.write(data);
    appendRawToMirror(data);
  },
  onLine: (line) => {
    term.value?.writeln(line);
    pushMirror(line);
  },
  onHistory: (lines, ansi) => {
    if (!term.value) return;
    if (ansi) term.value.write(ansi);
    else if (lines.length) term.value.write(`${lines.join('\r\n')}\r\n`);
    mirrorLines.value = lines.slice(-Math.min(500, CONSOLE_HISTORY_LINES));
    if (ansi) appendRawToMirror(ansi);
  },
});

const { status, lastError } = socket;

const statusLabel = computed(
  () => ({ connecting: '连接中', open: '已连接', closed: '已断开', error: '连接错误' })[status.value],
);
const dotClass = computed(
  () =>
    ({
      connecting: 'bg-amber-400 animate-pulse',
      open: 'bg-emerald-400',
      closed: 'bg-zinc-500',
      error: 'bg-red-500',
    })[status.value as ConsoleStatus],
);
const textClass = computed(
  () =>
    ({
      connecting: 'text-amber-300',
      open: 'text-emerald-300',
      closed: 'text-zinc-400',
      error: 'text-red-400',
    })[status.value as ConsoleStatus],
);

function fit(): void {
  try {
    fitAddon.value?.fit();
  } catch {
    // 容器不可见时忽略
  }
}

function connect(): void {
  socket.connect();
}

function clear(): void {
  term.value?.clear();
}

function onSubmit(): void {
  const cmd = input.value.trim();
  if (!cmd) return;
  socket.command(cmd);
  input.value = '';
}

onMounted(async () => {
  const [{ Terminal }, { FitAddon }] = await Promise.all([
    import('@xterm/xterm'),
    import('@xterm/addon-fit'),
  ]);
  await import('@xterm/xterm/css/xterm.css');

  const terminal = new Terminal({
    convertEol: true,
    cursorBlink: true,
    fontSize: 13,
    fontFamily: 'Consolas, "Cascadia Mono", monospace',
    theme: { background: '#000000', foreground: '#d4d4d8' },
    scrollback: 5000,
  });
  const fitAddonInstance = new FitAddon();
  terminal.loadAddon(fitAddonInstance);
  if (container.value) {
    terminal.open(container.value);
    fitAddonInstance.fit();
  }
  term.value = terminal;
  fitAddon.value = fitAddonInstance;

  terminal.onData((data) => socket.write(data));
  terminal.onResize(({ cols, rows }) => socket.resize(cols, rows));
  socket.resize(terminal.cols, terminal.rows);

  if (container.value) {
    resizeObserver = new ResizeObserver(() => fit());
    resizeObserver.observe(container.value);
  }

  socket.connect();
});

onBeforeUnmount(() => {
  resizeObserver?.disconnect();
  resizeObserver = null;
  term.value?.dispose();
  term.value = null;
});
</script>
