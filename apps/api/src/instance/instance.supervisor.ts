import { Inject, Injectable, Logger } from '@nestjs/common';
import { EventEmitter } from 'node:events';
import type { InstanceMeta, InstanceRuntime, InstanceStatus } from '@sc-panel/shared';
import type { PanelEnv } from '../config/env';
import { PANEL_ENV } from '../config/env.module';
import { PtyAdapter } from '../adapter/pty.adapter';
import type { AdapterEvent, IServerAdapter } from '../adapter/server-adapter.interface';
import { dotnetSearchDirs, resolveExecutable } from '../common/utils/which';
import { FileTailer } from './file-tailer';

/** 启动成功特征（实测：23:15:44.192 INFO: [StartServer]开启服务器成功，端口 28887） */
const STARTUP_RE = /开启服务器成功，端口\s*(\d+)/;
/** 完全进入游戏主循环（实测） */
const RUNNING_RE = /Entered screen "Game"/;
/** 启动失败特征 */
const STARTUP_FAIL_RE = /创建服务器失败|端口已被占用|启动失败/;

const DEFAULT_START_TIMEOUT_MS = 90_000;

interface Managed {
  meta: InstanceMeta;
  runtime: InstanceRuntime;
  adapter?: IServerAdapter;
  unsubscribe?: () => void;
  tailer?: FileTailer;
  startTimer?: NodeJS.Timeout;
  autoRestartTimer?: NodeJS.Timeout;
  /** 是否由面板主动停止（用于区分崩溃 vs 正常退出） */
  intentionalStop: boolean;
  restartAttempts: number;
}

/**
 * 实例进程监督器（plants.md §5.2 / §7）。
 *
 * 状态机：stopped → starting → running → stopping → stopped；崩溃 → crashed。
 * 通过 PTY 适配器运行游戏进程，并按日志特征推进状态。
 */
@Injectable()
export class InstanceSupervisor extends EventEmitter {
  private readonly logger = new Logger('Supervisor');
  private readonly managed = new Map<string, Managed>();
  private readonly stopGraceMs: number;

  constructor(@Inject(PANEL_ENV) env: PanelEnv) {
    super();
    this.stopGraceMs = env.STOP_GRACE_MS;
  }

  /** 适配器工厂（测试可覆盖为假实现） */
  protected createAdapter(): IServerAdapter {
    return new PtyAdapter();
  }

  /** 注册（面板重启后恢复已存在实例，一律 stopped） */
  register(meta: InstanceMeta): void {
    if (this.managed.has(meta.id)) return;
    this.managed.set(meta.id, {
      meta,
      runtime: { status: 'stopped' },
      intentionalStop: false,
      restartAttempts: 0,
    });
  }

  unregister(id: string): void {
    const m = this.managed.get(id);
    if (m) {
      m.unsubscribe?.();
      m.tailer?.stop();
      if (m.startTimer) clearTimeout(m.startTimer);
      if (m.autoRestartTimer) clearTimeout(m.autoRestartTimer);
      m.adapter?.kill();
    }
    this.managed.delete(id);
  }

  updateMeta(meta: InstanceMeta): void {
    const m = this.managed.get(meta.id);
    if (m) m.meta = meta;
    else this.register(meta);
  }

  getRuntime(id: string): InstanceRuntime | undefined {
    return this.managed.get(id)?.runtime;
  }

  listRuntimes(): Record<string, InstanceRuntime> {
    const out: Record<string, InstanceRuntime> = {};
    for (const [id, m] of this.managed) out[id] = m.runtime;
    return out;
  }

  isAlive(id: string): boolean {
    return this.managed.get(id)?.adapter?.isAlive() ?? false;
  }

  private setStatus(id: string, status: InstanceStatus, patch: Partial<InstanceRuntime> = {}): void {
    const m = this.managed.get(id);
    if (!m) return;
    m.runtime = { ...m.runtime, ...patch, status };
    if (status === 'running' && !m.runtime.startedAt) {
      m.runtime.startedAt = new Date().toISOString();
    }
    if (status === 'stopped' || status === 'crashed') {
      m.runtime.pid = undefined;
      m.runtime.startedAt = undefined;
      m.runtime.uptimeMs = undefined;
      m.runtime.cpu = undefined;
      m.runtime.memMB = undefined;
      m.runtime.probeOnline = false;
    }
    this.logger.log(`[${id}] status → ${status}`);
    this.emit('status', { instanceId: id, runtime: m.runtime });
  }

  /** 运行时字段变化（如 pid/端口），不改变状态 */
  private emitRuntime(id: string): void {
    const m = this.managed.get(id);
    if (!m) return;
    this.emit('runtime', { instanceId: id, runtime: m.runtime });
  }

  async start(id: string): Promise<InstanceRuntime> {
    const m = this.managed.get(id);
    if (!m) throw new Error(`实例不存在: ${id}`);
    if (m.runtime.status === 'running' || m.runtime.status === 'starting') {
      return m.runtime;
    }

    m.intentionalStop = false;
    m.restartAttempts = 0;
    this.setStatus(id, 'starting');

    const adapter = this.createAdapter();
    m.adapter = adapter;
    m.unsubscribe = adapter.on((event) => this.onAdapterEvent(m, event));

    // 同时 tail 服务端文件日志（不同 LogMode 下与 stdout 可能不一致）
    m.tailer = new FileTailer(m.meta.dir, (line) => {
      this.emit('line', { instanceId: m.meta.id, line, ts: Date.now() });
      this.matchLine(m, line);
    });
    m.tailer.start(400);

    try {
      const command = resolveExecutable('dotnet', dotnetSearchDirs());
      const { pid } = await adapter.start({
        command,
        args: ['Survivalcraft.dll', ...m.meta.launchArgs],
        cwd: m.meta.dir,
      });
      m.runtime.pid = pid;
      this.emitRuntime(id);
    } catch (err) {
      this.logger.error(`[${id}] 启动 PTY 失败: ${String(err)}`);
      m.unsubscribe?.();
      m.unsubscribe = undefined;
      m.adapter = undefined;
      m.tailer?.stop();
      m.tailer = undefined;
      this.setStatus(id, 'crashed');
      throw err;
    }

    // 启动超时保护
    if (m.startTimer) clearTimeout(m.startTimer);
    m.startTimer = setTimeout(() => {
      if (m.runtime.status === 'starting') {
        this.logger.warn(`[${id}] 启动超时（未出现成功特征），判为 failed/crashed`);
        this.setStatus(id, 'crashed');
      }
    }, DEFAULT_START_TIMEOUT_MS);
    m.startTimer.unref?.();

    return m.runtime;
  }

  private onAdapterEvent(m: Managed, event: AdapterEvent): void {
    switch (event.type) {
      case 'raw':
        this.emit('raw', { instanceId: m.meta.id, data: event.data });
        break;
      case 'line':
        this.emit('line', { instanceId: m.meta.id, line: event.line, ts: event.ts });
        this.matchLine(m, event.line);
        break;
      case 'exit':
        this.onExit(m, event.info);
        break;
    }
  }

  private matchLine(m: Managed, line: string): void {
    if (m.runtime.status === 'starting') {
      const ok = line.match(STARTUP_RE);
      if (ok) {
        m.runtime.serverPort = Number(ok[1]);
        this.logger.log(`[${m.meta.id}] 服务端开启成功，端口 ${ok[1]}`);
      }
      if (STARTUP_FAIL_RE.test(line)) {
        this.logger.error(`[${m.meta.id}] 启动失败特征: ${line}`);
        this.setStatus(m.meta.id, 'crashed');
        return;
      }
      if (RUNNING_RE.test(line)) {
        if (m.startTimer) clearTimeout(m.startTimer);
        this.setStatus(m.meta.id, 'running');
      }
    }
  }

  private onExit(m: Managed, info: { exitCode: number; signal?: number }): void {
    const id = m.meta.id;
    if (m.startTimer) clearTimeout(m.startTimer);
    m.unsubscribe?.();
    m.unsubscribe = undefined;
    m.adapter = undefined;
    m.tailer?.stop();
    m.tailer = undefined;

    const wasIntentional = m.intentionalStop;
    this.setStatus(id, wasIntentional ? 'stopped' : 'crashed');

    if (!wasIntentional && m.meta.autoRestart) {
      m.restartAttempts += 1;
      // 指数退避，上限 60s
      const delay = Math.min(60_000, 5_000 * 2 ** (m.restartAttempts - 1));
      this.logger.warn(
        `[${id}] 崩溃（exit=${info.exitCode}），${delay}ms 后自动重启（第 ${m.restartAttempts} 次）`,
      );
      m.autoRestartTimer = setTimeout(() => {
        void this.start(id).catch((err) => this.logger.error(`[${id}] 自动重启失败: ${String(err)}`));
      }, delay);
      m.autoRestartTimer.unref?.();
    }
  }

  async stop(id: string): Promise<void> {
    const m = this.managed.get(id);
    if (!m?.adapter || !m.adapter.isAlive()) {
      if (m) m.adapter = undefined;
      this.setStatus(id, 'stopped');
      return;
    }
    m.intentionalStop = true;
    this.setStatus(id, 'stopping');
    await m.adapter.stop({ gracefulMs: this.stopGraceMs });
  }

  kill(id: string): void {
    const m = this.managed.get(id);
    if (!m?.adapter || !m.adapter.isAlive()) {
      if (m) m.adapter = undefined;
      this.setStatus(id, 'stopped');
      return;
    }
    m.intentionalStop = true;
    m.adapter.kill();
  }

  async restart(id: string): Promise<void> {
    await this.stop(id);
    // 等待进程完全退出后再启动
    await new Promise((r) => setTimeout(r, 500));
    await this.start(id);
  }

  sendCommand(id: string, cmd: string): void {
    this.managed.get(id)?.adapter?.sendCommand(cmd);
  }

  write(id: string, data: string): void {
    this.managed.get(id)?.adapter?.write(data);
  }

  resize(id: string, cols: number, rows: number): void {
    this.managed.get(id)?.adapter?.resize(cols, rows);
  }

  async onModuleDestroy(): Promise<void> {
    for (const id of this.managed.keys()) {
      try {
        this.kill(id);
      } catch {
        // ignore
      }
    }
  }
}
