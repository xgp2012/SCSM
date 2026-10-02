import { afterEach, describe, expect, it } from 'vitest';
import { createSocket } from 'node:dgram';
import { probeServer } from '../src/probe/udp-probe';
import { buildProbeRequest, decodeReply } from '../src/probe/server-info.protocol';
import { deflateRawSync } from 'node:zlib';

/**
 * 用本地 UDP 回显服务器验证 udp-probe 的收发与超时行为（不依赖真实服务端）。
 */

function startFakeServer(handler: (msg: Buffer, from: { address: string; port: number }, socket: ReturnType<typeof createSocket>) => void) {
  const socket = createSocket('udp4');
  return new Promise<{ port: number; close: () => void }>((resolve) => {
    socket.on('message', (msg, rinfo) => handler(msg, rinfo, socket));
    socket.bind(0, '127.0.0.1', () => {
      const port = (socket.address() as { port: number }).port;
      resolve({ port, close: () => socket.close() });
    });
  });
}

function makeReply(): Buffer {
  const version = Buffer.from('x26.06.19', 'utf8');
  const head = Buffer.from([0x88, 0x00, 0x00, version.length]);
  const counts = Buffer.alloc(4);
  counts.writeUInt16LE(2, 0);
  counts.writeUInt16LE(20, 2);
  const flags = Buffer.from([2, 0, 0]);
  const time = Buffer.alloc(4);
  time.writeFloatLE(0.5, 0);
  const inner = Buffer.concat([head, version, counts, flags, time]);
  return Buffer.concat([Buffer.from([0x09]), deflateRawSync(inner)]);
}

describe('udp-probe', () => {
  let server: { port: number; close: () => void } | undefined;

  afterEach(() => {
    server?.close();
    server = undefined;
  });

  it('收到有效回执时返回 online=true 与解析字段', async () => {
    server = await startFakeServer((msg, from, socket) => {
      // 仅在请求格式正确时回复
      if (decodeReply(Buffer.concat([Buffer.from([0x09]), msg.subarray(1)])) || msg[0] === 0x09) {
        socket.send(makeReply(), from.port, from.address);
      }
    });
    const result = await probeServer('inst-1', server.port, { timeoutMs: 1000 });
    expect(result.online).toBe(true);
    expect(result.version).toBe('x26.06.19');
    expect(result.playerCount).toBe(2);
    expect(result.maxCount).toBe(20);
    expect(result.gameMode).toBe('Survival');
    expect(typeof result.pingMs).toBe('number');
  });

  it('无响应时超时返回 online=false', async () => {
    server = await startFakeServer(() => {
      // 故意不回复
    });
    const result = await probeServer('inst-2', server.port, { timeoutMs: 300 });
    expect(result.online).toBe(false);
    expect(result.error).toBe('timeout');
  });

  it('buildProbeRequest 请求体被回显服务器收到', async () => {
    let received = false;
    server = await startFakeServer((msg, from, socket) => {
      received = msg.equals(buildProbeRequest());
      socket.send(makeReply(), from.port, from.address);
    });
    await probeServer('inst-3', server.port, { timeoutMs: 1000 });
    expect(received).toBe(true);
  });
});
