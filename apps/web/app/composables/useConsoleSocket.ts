import { ref, shallowRef, onBeforeUnmount } from 'vue';
import type {
  InstanceRuntime,
  PlayerListEvent,
  SystemStatsEvent,
  WsServerEvent,
} from '@sc-panel/shared';

export type ConsoleStatus = 'connecting' | 'open' | 'closed' | 'error';

export interface UseConsoleSocketOptions {
  onRaw?: (data: string) => void;
  onLine?: (line: string, ts: number) => void;
  onHistory?: (lines: string[], ansi: string) => void;
  onStatus?: (runtime: Partial<InstanceRuntime> & { status: InstanceRuntime['status'] }) => void;
  /** 进程指标（CPU/内存） */
  onStats?: (stats: { cpu: number; memMB: number; ts: number }) => void;
  /** UDP 探测结果（在线人数/模式等） */
  onPlayerList?: (players: PlayerListEvent) => void;
  /** 面板主机系统指标 */
  onSystemStats?: (stats: SystemStatsEvent) => void;
}

/**
 * 终端 WebSocket 客户端（plants.md §5.3）。
 * 复用 HttpOnly Cookie 握手（同源），接收 console.raw/line/history/hello 与 instance.status。
 */
export function useConsoleSocket(instanceId: string, options: UseConsoleSocketOptions = {}) {
  const ws = shallowRef<WebSocket | null>(null);
  const status = ref<ConsoleStatus>('closed');
  const lastError = ref<string | null>(null);

  function wsUrl(): string {
    // 优先直连后端（由 api-base 插件注入 window.__SC_API_BASE__）：
    // 生产 SPA 常与后端分端口部署，且 Nitro 代理不处理 WebSocket 升级；
    // 未注入时回退同源（开发网关/反向代理）。
    const base = (window as unknown as { __SC_API_BASE__?: string }).__SC_API_BASE__;
    let proto = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
    let host = window.location.host;
    if (base) {
      try {
        const u = new URL(base, window.location.origin);
        proto = u.protocol === 'https:' ? 'wss:' : 'ws:';
        host = u.host;
      } catch {
        // 保留同源回退
      }
    }
    return `${proto}//${host}/ws/console?instanceId=${encodeURIComponent(instanceId)}`;
  }

  function connect(): void {
    disconnect();
    status.value = 'connecting';
    lastError.value = null;
    const socket = new WebSocket(wsUrl());
    socket.binaryType = 'arraybuffer';
    ws.value = socket;

    socket.onopen = () => {
      status.value = 'open';
    };
    socket.onclose = (ev) => {
      status.value = 'closed';
      if (ev.code === 4401 || ev.code === 4404) lastError.value = ev.reason || '连接被拒绝';
    };
    socket.onerror = () => {
      status.value = 'error';
    };
    socket.onmessage = (ev) => {
      if (typeof ev.data !== 'string') return;
      let msg: WsServerEvent;
      try {
        msg = JSON.parse(ev.data) as WsServerEvent;
      } catch {
        return;
      }
      switch (msg.type) {
        case 'console.raw':
          options.onRaw?.(msg.data);
          break;
        case 'console.line':
          options.onLine?.(msg.line, msg.ts);
          break;
        case 'console.history':
          options.onHistory?.(msg.lines, msg.ansi);
          break;
        case 'console.hello':
          options.onStatus?.(msg.runtime);
          break;
        case 'instance.status':
          options.onStatus?.(msg);
          break;
        case 'instance.stats':
          options.onStats?.({ cpu: msg.cpu, memMB: msg.memMB, ts: msg.ts });
          break;
        case 'player.list':
          options.onPlayerList?.(msg);
          break;
        case 'system.stats':
          options.onSystemStats?.(msg);
          break;
      }
    };
  }

  function send(event: unknown): void {
    const socket = ws.value;
    if (socket && socket.readyState === WebSocket.OPEN) socket.send(JSON.stringify(event));
  }

  const write = (data: string) => send({ type: 'console.write', data });
  const command = (cmd: string) => send({ type: 'console.command', command: cmd });
  const resize = (cols: number, rows: number) => send({ type: 'terminal.resize', cols, rows });

  function disconnect(): void {
    const socket = ws.value;
    if (socket) {
      socket.onclose = null;
      socket.onerror = null;
      socket.onmessage = null;
      socket.onopen = null;
      try {
        socket.close();
      } catch {
        // ignore
      }
      ws.value = null;
    }
    if (status.value !== 'closed') status.value = 'closed';
  }

  onBeforeUnmount(disconnect);

  return { status, lastError, connect, disconnect, write, command, resize };
}
