import { Controller, Get, NotFoundException, Param } from '@nestjs/common';
import type { ProbeResult } from '@sc-panel/shared';
import { InstanceService } from '../instance/instance.service';
import { MetricsService } from './metrics.service';
import { SystemService } from '../system/system.service';
import type { SystemStats } from '@sc-panel/shared';

@Controller()
export class ProbeController {
  constructor(
    private readonly instances: InstanceService,
    private readonly metrics: MetricsService,
    private readonly system: SystemService,
  ) {}

  /** 即时 UDP 探测指定实例（不依赖周期任务） */
  @Get('instances/:id/probe')
  async probe(@Param('id') id: string): Promise<{ ok: true; data: ProbeResult }> {
    const inst = await this.instances.get(id); // 抛 404
    if (!inst) throw new NotFoundException(`实例不存在: ${id}`);
    const result = await this.metrics.probeInstance(id, inst.serverPort);
    return { ok: true, data: result };
  }

  /** 面板主机系统指标快照 */
  @Get('system/stats')
  async stats(): Promise<{ ok: true; data: SystemStats }> {
    return { ok: true, data: await this.system.snapshot() };
  }
}
