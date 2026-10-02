import { Injectable } from '@nestjs/common';
import { cpus, freemem, loadavg, platform, totalmem, uptime } from 'node:os';
import { statfs } from 'node:fs/promises';
import type { SystemStats } from '@sc-panel/shared';

/** 主机系统指标采样（plants.md §5.7）。零外部依赖，仅用 node:os + fs.statfs。 */
@Injectable()
export class SystemService {
  private lastCpu = cpuSnapshot();

  /** 采集一次系统快照（CPU 使用率为自上次采样以来的增量） */
  async snapshot(): Promise<SystemStats> {
    const curr = cpuSnapshot();
    const cpu = diffCpu(this.lastCpu, curr);
    this.lastCpu = curr;

    const memTotalMB = Math.round(totalmem() / 1024 / 1024);
    const memUsedMB = Math.round((totalmem() - freemem()) / 1024 / 1024);

    let diskPercent: number | undefined;
    let diskUsedMB: number | undefined;
    let diskTotalMB: number | undefined;
    try {
      const fs = await statfs(process.cwd());
      const total = fs.blocks * fs.bsize;
      const free = fs.bfree * fs.bsize;
      if (total > 0) {
        diskTotalMB = Math.round(total / 1024 / 1024);
        diskUsedMB = Math.round((total - free) / 1024 / 1024);
        diskPercent = round2(((total - free) / total) * 100);
      }
    } catch {
      // 某些平台 statfs 不可用，忽略磁盘
    }

    return {
      cpu,
      memPercent: round2((memUsedMB / memTotalMB) * 100),
      memUsedMB,
      memTotalMB,
      diskPercent,
      diskUsedMB,
      diskTotalMB,
      cpuCount: cpus().length,
      platform: platform(),
      uptimeSec: Math.round(uptime()),
      loadAvg: loadavg().map(round2),
      ts: Date.now(),
    };
  }
}

interface CpuSnapshot {
  idle: number;
  total: number;
}

function cpuSnapshot(): CpuSnapshot {
  let idle = 0;
  let total = 0;
  for (const cpu of cpus()) {
    for (const [key, value] of Object.entries(cpu.times)) {
      total += value;
      if (key === 'idle') idle += value;
    }
  }
  return { idle, total };
}

/** 两次采样之间的 CPU 使用率百分比；首次调用（无历史）返回 0 */
function diffCpu(prev: CpuSnapshot, curr: CpuSnapshot): number {
  const totalDiff = curr.total - prev.total;
  const idleDiff = curr.idle - prev.idle;
  if (totalDiff <= 0) return 0;
  return round2(((totalDiff - idleDiff) / totalDiff) * 100);
}

function round2(n: number): number {
  return Math.round(n * 100) / 100;
}
