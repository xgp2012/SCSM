import { createSocket } from 'node:dgram';
import type { ProbeResult } from '@sc-panel/shared';
import { PROBE_TIMEOUT_MS } from '@sc-panel/shared';
import { buildProbeRequest, decodeReply } from './server-info.protocol';

export interface ProbeOptions {
  host?: string;
  timeoutMs?: number;
}

/**
 * 向指定 ServerPort 单播 ServerInfo 请求并等待回执（plants.md §5.7）。
 * 独立于进程状态，可用于"进程未托管但服务在跑"的只读监控。
 */
export function probeServer(
  instanceId: string,
  port: number,
  options: ProbeOptions = {},
): Promise<ProbeResult> {
  const host = options.host ?? '127.0.0.1';
  const timeoutMs = options.timeoutMs ?? PROBE_TIMEOUT_MS;
  const request = buildProbeRequest();

  return new Promise<ProbeResult>((resolve) => {
    const socket = createSocket('udp4');
    let settled = false;
    const startedAt = Date.now();

    const finish = (result: ProbeResult): void => {
      if (settled) return;
      settled = true;
      clearTimeout(timer);
      try {
        socket.close();
      } catch {
        // ignore
      }
      resolve(result);
    };

    const timer = setTimeout(() => {
      finish({ instanceId, online: false, error: 'timeout' });
    }, timeoutMs);

    socket.on('message', (msg: Buffer) => {
      const parsed = decodeReply(msg);
      if (parsed) {
        finish({ instanceId, online: true, pingMs: Date.now() - startedAt, ...parsed });
      }
    });

    socket.on('error', (err: Error) => {
      finish({ instanceId, online: false, error: err.message });
    });

    socket.send(request, port, host, (err) => {
      if (err) finish({ instanceId, online: false, error: err.message });
    });
  });
}
