import { Inject, Injectable, Logger, OnModuleDestroy, OnModuleInit } from '@nestjs/common';
import { EventEmitter } from 'node:events';
import type { PanelEnv } from '../config/env';
import { PANEL_ENV } from '../config/env.module';
import { InstanceStore } from '../instance/instance.store';
import { InstanceSupervisor } from '../instance/instance.supervisor';
import { probeServer } from './udp-probe';
import { ProcessSampler } from '../system/process-sampler';
import { SystemService } from '../system/system.service';

/** 事件：单实例进程资源 */
export interface InstanceStatsPayload {
  instanceId: string;
  cpu: number;
  memMB: number;
  ts: number;
}

/** 事件：单实例探测结果（玩家/世界信息） */
export interface PlayerListPayload {
  instanceId: string;
  online: number;
  maxOnline: number;
  gameMode: string;
  hasPassword: boolean;
  probeOnline: boolean;
  version?: string;
  timeOfDay?: number;
  pingMs?: number;
}

/**
 * M3 指标与探测调度（plants.md §5.7）。
 *
 * 周期任务：
 * - 对 `running` 实例采样进程 CPU/内存 → `instance-stats`；
 * - 对 `running` 实例 UDP 单播探测 ServerInfo → `player-list`，并写回 runtime；
 * - 采集面板主机系统指标 → `system-stats`。
 *
 * 事件由 ConsoleGateway 转发为 WS 帧（instance.stats / player.list / system.stats）。
 */
@Injectable()
export class MetricsService extends EventEmitter implements OnModuleInit, OnModuleDestroy {
  private readonly logger = new Logger('Metrics');
  private readonly sampler = new ProcessSampler();
  private metricsTimer?: NodeJS.Timeout;
  private probeTimer?: NodeJS.Timeout;
  private ticking = false;

  constructor(
    @Inject(PANEL_ENV) private readonly env: PanelEnv,
    private readonly store: InstanceStore,
    private readonly supervisor: InstanceSupervisor,
    private readonly system: SystemService,
  ) {
    super();
  }

  onModuleInit(): void {
    if (this.env.METRICS_INTERVAL_MS > 0) {
      this.metricsTimer = setInterval(() => {
        void this.collectMetrics();
      }, this.env.METRICS_INTERVAL_MS);
      this.metricsTimer.unref?.();
    }
    if (this.env.PROBE_INTERVAL_MS > 0) {
      this.probeTimer = setInterval(() => {
        void this.collectProbes();
      }, this.env.PROBE_INTERVAL_MS);
      this.probeTimer.unref?.();
    }
    this.logger.log(
      `指标采样 ${this.env.METRICS_INTERVAL_MS}ms / 探测 ${this.env.PROBE_INTERVAL_MS}ms 已启动`,
    );
  }

  onModuleDestroy(): void {
    if (this.metricsTimer) clearInterval(this.metricsTimer);
    if (this.probeTimer) clearInterval(this.probeTimer);
  }

  /** 采样全部运行中实例的进程资源 + 主机系统指标 */
  async collectMetrics(): Promise<void> {
    const runtimes = this.supervisor.listRuntimes();
    await Promise.all(
      Object.entries(runtimes)
        .filter(([, rt]) => rt.status === 'running' && rt.pid)
        .map(async ([id, rt]) => {
          const sample = await this.sampler.sample(rt.pid as number);
          if (!sample) return;
          rt.cpu = sample.cpu;
          rt.memMB = sample.memMB;
          this.emit('instance-stats', {
            instanceId: id,
            cpu: sample.cpu,
            memMB: sample.memMB,
            ts: Date.now(),
          } satisfies InstanceStatsPayload);
        }),
    );

    try {
      const stats = await this.system.snapshot();
      this.emit('system-stats', stats);
    } catch (err) {
      this.logger.warn(`系统指标采样失败: ${String(err)}`);
    }
  }

  /** 探测全部运行中实例（也可探测 stopped 但端口在跑的实例） */
  async collectProbes(): Promise<void> {
    const metas = await this.store.list();
    const runtimes = this.supervisor.listRuntimes();
    await Promise.all(
      metas.map(async (meta) => {
        const rt = runtimes[meta.id];
        // 仅探测进程在运行的实例；stopped 实例跳过（避免无谓超时等待）
        if (!rt || (rt.status !== 'running' && rt.status !== 'starting')) return;
        const result = await this.probeInstance(meta.id, meta.serverPort);
        this.applyProbe(meta.id, result);
        this.emit('player-list', {
          instanceId: meta.id,
          online: result.playerCount ?? 0,
          maxOnline: result.maxCount ?? meta.maxPlayers,
          gameMode: result.gameMode ?? 'Unknown',
          hasPassword: result.needPassword ?? false,
          probeOnline: result.online,
          version: result.version,
          timeOfDay: result.timeOfDay,
          pingMs: result.pingMs,
        } satisfies PlayerListPayload);
      }),
    );
  }

  /** 对单个实例执行一次探测（供 REST 即时查询） */
  async probeInstance(instanceId: string, port: number) {
    return probeServer(instanceId, port, { timeoutMs: 1500 });
  }

  private applyProbe(instanceId: string, result: Awaited<ReturnType<typeof probeServer>>): void {
    const rt = this.supervisor.getRuntime(instanceId);
    if (!rt) return;
    rt.probeOnline = result.online;
    if (result.online) {
      rt.lastProbeAt = new Date().toISOString();
      if (result.playerCount !== undefined) rt.online = result.playerCount;
      if (result.maxCount !== undefined) rt.maxOnline = result.maxCount;
      if (result.gameMode !== undefined) rt.gameMode = result.gameMode;
      if (result.needPassword !== undefined) rt.hasPassword = result.needPassword;
      if (result.timeOfDay !== undefined) rt.timeOfDay = result.timeOfDay;
    }
  }

  /** 外部（如停止实例时）清理采样状态 */
  forgetInstance(pid?: number): void {
    if (pid) this.sampler.forget(pid);
  }

  /** 手动触发一次全量采集（门禁/测试用） */
  async tick(): Promise<void> {
    if (this.ticking) return;
    this.ticking = true;
    try {
      await this.collectMetrics();
      await this.collectProbes();
    } finally {
      this.ticking = false;
    }
  }
}
