import { execFile } from 'node:child_process';

/**
 * 单进程资源采样（plants.md §5.7）。零外部依赖：
 * - Linux/macOS：读取 `/proc/<pid>/stat`（CPU 时间）与 `/proc/<pid>/status`（RSS）。
 * - Windows：使用 `wmic process where ProcessId=<pid> get KernelModeTime,UserModeTime,WorkingSetSize`；
 *   wmic 缺失时回退 `powershell Get-Process`。
 *
 * CPU 百分比为两次采样之间的增量除以墙钟时间 × 核数。
 */

export interface ProcessSample {
  cpu: number;
  memMB: number;
}

interface RawProcess {
  /** 累计 CPU 时间（秒） */
  cpuSeconds: number;
  /** 常驻内存（字节） */
  rssBytes: number;
}

const CLK_TCK = 100; // Linux 默认 USER_HZ；作为近似足够用于百分比

export class ProcessSampler {
  private last = new Map<number, { ts: number; cpuSeconds: number }>();

  async sample(pid: number): Promise<ProcessSample | null> {
    const raw = await readProcess(pid);
    if (!raw) {
      this.last.delete(pid);
      return null;
    }
    const now = Date.now();
    const prev = this.last.get(pid);
    this.last.set(pid, { ts: now, cpuSeconds: raw.cpuSeconds });

    let cpu = 0;
    if (prev) {
      const wallSec = (now - prev.ts) / 1000;
      const cpuDelta = raw.cpuSeconds - prev.cpuSeconds;
      if (wallSec > 0 && cpuDelta >= 0) {
        cpu = round2(Math.min(100 * 64, (cpuDelta / wallSec) * 100)); // 上限保护
      }
    }
    return { cpu, memMB: round2(raw.rssBytes / 1024 / 1024) };
  }

  forget(pid: number): void {
    this.last.delete(pid);
  }
}

async function readProcess(pid: number): Promise<RawProcess | null> {
  if (process.platform === 'win32') return readProcessWindows(pid);
  return readProcessProc(pid);
}

/** Linux/macOS：/proc（macOS 无 /proc，将返回 null，由上层降级） */
async function readProcessProc(pid: number): Promise<RawProcess | null> {
  try {
    const { readFile } = await import('node:fs/promises');
    const stat = await readFile(`/proc/${pid}/stat`, 'utf8');
    // 字段：pid (comm) state ppid ... utime(14) stime(15) ...
    const close = stat.lastIndexOf(')');
    const rest = stat.slice(close + 2).split(' ');
    // rest[0] = state, 故 utime 在 rest 索引 11，stime 在 12
    const utime = Number(rest[11] ?? 0);
    const stime = Number(rest[12] ?? 0);
    const cpuSeconds = (utime + stime) / CLK_TCK;

    let rssBytes = 0;
    try {
      const status = await readFile(`/proc/${pid}/status`, 'utf8');
      const m = status.match(/VmRSS:\s+(\d+)\s+kB/);
      if (m) rssBytes = Number(m[1]) * 1024;
    } catch {
      // ignore
    }
    return { cpuSeconds, rssBytes };
  } catch {
    return null;
  }
}

function readProcessWindows(pid: number): Promise<RawProcess | null> {
  return new Promise((resolve) => {
    execFile(
      'wmic',
      ['process', 'where', `ProcessId=${pid}`, 'get', 'KernelModeTime,UserModeTime,WorkingSetSize', '/format:list'],
      { windowsHide: true, timeout: 4000 },
      (err, stdout) => {
        if (err) {
          resolve(readProcessWindowsFallback(pid));
          return;
        }
        const get = (key: string): number => {
          const m = stdout.match(new RegExp(`${key}=([0-9]+)`, 'i'));
          return m ? Number(m[1]) : 0;
        };
        const kernel = get('KernelModeTime');
        const user = get('UserModeTime');
        const rss = get('WorkingSetSize');
        if (kernel === 0 && user === 0 && rss === 0) {
          resolve(null);
          return;
        }
        // Windows 时间单位为 100ns
        resolve({ cpuSeconds: (kernel + user) / 1e7, rssBytes: rss });
      },
    );
  });
}

function readProcessWindowsFallback(pid: number): Promise<RawProcess | null> {
  return new Promise((resolve) => {
    execFile(
      'powershell',
      [
        '-NoProfile',
        '-Command',
        `$p=Get-Process -Id ${pid} -ErrorAction SilentlyContinue; if($p){ "$($p.CPU) $($p.WorkingSet64)" }`,
      ],
      { windowsHide: true, timeout: 6000 },
      (err, stdout) => {
        if (err) {
          resolve(null);
          return;
        }
        const [cpuStr, rssStr] = stdout.trim().split(/\s+/);
        const cpuSeconds = Number(cpuStr);
        const rss = Number(rssStr);
        if (!Number.isFinite(cpuSeconds) || !Number.isFinite(rss)) {
          resolve(null);
          return;
        }
        resolve({ cpuSeconds, rssBytes: rss });
      },
    );
  });
}

function round2(n: number): number {
  return Math.round(n * 100) / 100;
}
