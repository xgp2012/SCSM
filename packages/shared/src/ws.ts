import type { GameMode, InstanceRuntime, InstanceStatus, SystemStats } from './instance';

/** WebSocket 事件通道（plants.md §5.3） */
export type WsEventType =
  | 'console.raw'
  | 'console.line'
  | 'console.history'
  | 'console.hello'
  | 'instance.status'
  | 'instance.stats'
  | 'player.list'
  | 'system.stats'
  | 'error';

export interface ConsoleRawEvent {
  type: 'console.raw';
  instanceId: string;
  /** 原始终端字节流（ANSI），供 xterm.js 渲染 */
  data: string;
}

export interface ConsoleLineEvent {
  type: 'console.line';
  instanceId: string;
  /** 清洗后的纯文本行（供日志/检索/事件匹配） */
  line: string;
  ts: number;
}

/** 连接时下发的回放（plants.md §5.3）：纯文本行 + 一屏 ANSI 快照 */
export interface ConsoleHistoryEvent {
  type: 'console.history';
  instanceId: string;
  /** 清洗后的历史纯文本行（最多 N 行） */
  lines: string[];
  /** 历史 ANSI 快照（截尾至上限字节） */
  ansi: string;
}

/** 连接握手确认：回传实例运行时快照 */
export interface ConsoleHelloEvent {
  type: 'console.hello';
  instanceId: string;
  runtime: InstanceRuntime;
}

export interface InstanceStatusEvent {
  type: 'instance.status';
  instanceId: string;
  status: InstanceStatus;
  pid?: number;
  startedAt?: string;
  uptimeMs?: number;
  serverPort?: number;
}

export interface InstanceStatsEvent {
  type: 'instance.stats';
  instanceId: string;
  /** 进程 CPU 百分比（可超过 100 表示多核） */
  cpu: number;
  memMB: number;
  ts: number;
}

export interface PlayerListEvent {
  type: 'player.list';
  instanceId: string;
  online: number;
  maxOnline: number;
  gameMode: GameMode;
  hasPassword: boolean;
  /** 探测是否在线 */
  probeOnline: boolean;
  version?: string;
  timeOfDay?: number;
  pingMs?: number;
}

/** 面板主机系统指标（广播给所有 WS 连接） */
export interface SystemStatsEvent extends SystemStats {
  type: 'system.stats';
}

export interface WsErrorEvent {
  type: 'error';
  instanceId?: string;
  message: string;
  code?: string;
}

export type WsServerEvent =
  | ConsoleRawEvent
  | ConsoleLineEvent
  | ConsoleHistoryEvent
  | ConsoleHelloEvent
  | InstanceStatusEvent
  | InstanceStatsEvent
  | PlayerListEvent
  | SystemStatsEvent
  | WsErrorEvent;

/** 客户端 → 服务端 */
export interface ConsoleWriteMessage {
  type: 'console.write';
  data: string;
}

export interface ConsoleCommandMessage {
  type: 'console.command';
  command: string;
}

export interface TerminalResizeMessage {
  type: 'terminal.resize';
  cols: number;
  rows: number;
}

export interface PingMessage {
  type: 'ping';
}

export type WsClientEvent =
  | ConsoleWriteMessage
  | ConsoleCommandMessage
  | TerminalResizeMessage
  | PingMessage;

/** 终端回滚缓冲容量（plants.md §2.2） */
export const CONSOLE_HISTORY_LINES = 5000;
export const CONSOLE_HISTORY_ANSI_BYTES = 64 * 1024;
