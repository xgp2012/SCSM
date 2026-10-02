import type { InstanceStatus } from '@sc-panel/shared';

export interface AdapterStartOptions {
  cwd: string;
  args: string[];
  command: string;
  cols?: number;
  rows?: number;
  env?: Record<string, string>;
}

export interface AdapterExitInfo {
  exitCode: number;
  signal?: number;
}

export type AdapterEvent =
  | { type: 'raw'; data: string }
  | { type: 'line'; line: string; ts: number }
  | { type: 'exit'; info: AdapterExitInfo }
  | { type: 'status'; status: InstanceStatus };

export type AdapterEventListener = (event: AdapterEvent) => void;

/**
 * 服务端适配器（plants.md §5.4）。
 * 首期实现 PtyAdapter（node-pty 伪终端，硬性要求）；预留 PluginAdapter。
 */
export interface IServerAdapter {
  start(opts: AdapterStartOptions): Promise<{ pid: number }>;
  stop(opts?: { gracefulMs?: number }): Promise<void>;
  kill(): void;
  sendCommand(cmd: string): void;
  write(data: string): void;
  resize(cols: number, rows: number): void;
  isAlive(): boolean;
  getPid(): number | undefined;
  on(listener: AdapterEventListener): () => void;
}
