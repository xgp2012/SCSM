import { Injectable } from '@nestjs/common';
import type { InstanceRuntime } from '@sc-panel/shared';
import { InstanceSupervisor } from '../instance/instance.supervisor';
import { RollbackBuffer } from './rollback-buffer';

/**
 * 控制台会话存储（plants.md §5.3）。
 *
 * 每个实例维护一份回滚缓冲并订阅其 Supervisor 事件（raw/line/status），
 * 将事件分发给该实例的全部观察者。允许多观察者同时观察同一 PTY；
 * 命令写入由网关侧队列化，避免交叉输入。
 */
@Injectable()
export class ConsoleService {
  private readonly buffers = new Map<string, RollbackBuffer>();
  private readonly subscribers = new Map<string, Set<(event: unknown) => void>>();
  private readonly runtimes = new Map<string, InstanceRuntime>();

  constructor(private readonly supervisor: InstanceSupervisor) {
    for (const [id, runtime] of Object.entries(this.supervisor.listRuntimes())) {
      this.runtimes.set(id, runtime);
    }
    this.supervisor.on('raw', (e: { instanceId: string; data: string }) => {
      this.bufferFor(e.instanceId).appendRaw(e.data);
      this.dispatch(e.instanceId, { type: 'console.raw', instanceId: e.instanceId, data: e.data });
    });
    this.supervisor.on('line', (e: { instanceId: string; line: string; ts: number }) => {
      this.bufferFor(e.instanceId).appendLine(e.line);
      this.dispatch(e.instanceId, {
        type: 'console.line',
        instanceId: e.instanceId,
        line: e.line,
        ts: e.ts,
      });
    });
    this.supervisor.on('status', (e: { instanceId: string; runtime: InstanceRuntime }) => {
      this.runtimes.set(e.instanceId, e.runtime);
      this.dispatch(e.instanceId, {
        type: 'instance.status',
        instanceId: e.instanceId,
        status: e.runtime.status,
        pid: e.runtime.pid,
        startedAt: e.runtime.startedAt,
        uptimeMs: e.runtime.uptimeMs,
        serverPort: e.runtime.serverPort,
      });
    });
    this.supervisor.on('runtime', (e: { instanceId: string; runtime: InstanceRuntime }) => {
      this.runtimes.set(e.instanceId, e.runtime);
    });
  }

  getRuntime(id: string): InstanceRuntime | undefined {
    return this.supervisor.getRuntime(id) ?? this.runtimes.get(id);
  }

  getBuffer(id: string): RollbackBuffer {
    return this.bufferFor(id);
  }

  subscribe(id: string, listener: (event: unknown) => void): () => void {
    let set = this.subscribers.get(id);
    if (!set) {
      set = new Set();
      this.subscribers.set(id, set);
    }
    set.add(listener);
    return () => {
      set?.delete(listener);
      if (set && set.size === 0) this.subscribers.delete(id);
    };
  }

  private dispatch(id: string, event: unknown): void {
    const set = this.subscribers.get(id);
    if (!set) return;
    for (const listener of set) {
      try {
        listener(event);
      } catch {
        // 单个订阅者异常不影响其它观察者
      }
    }
  }

  /** 向指定实例的全部观察者扇出事件（供指标/探测服务复用） */
  publish(id: string, event: unknown): void {
    this.dispatch(id, event);
  }

  private bufferFor(id: string): RollbackBuffer {
    let buf = this.buffers.get(id);
    if (!buf) {
      buf = new RollbackBuffer();
      this.buffers.set(id, buf);
    }
    return buf;
  }
}
