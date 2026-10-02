import { describe, expect, it } from 'vitest';
import { deflateRawSync, inflateRawSync } from 'node:zlib';
import {
  buildProbeRequest,
  decodeReply,
  parseServerInfo,
  unwrapReply,
} from '../src/probe/server-info.protocol';

/** 构造一个与真实服务端回执一致的 UDP 载荷（用于测试解析逻辑） */
function makeReply(opts: {
  version: string;
  clientCount: number;
  maxPlayerCount: number;
  gameMode: number;
  needLogin?: boolean;
  needPasswd?: boolean;
  timeOfDay?: number;
}): Buffer {
  const version = Buffer.from(opts.version, 'utf8');
  const head = Buffer.from([0x88, 0x00, 0x00, version.length]);
  const counts = Buffer.alloc(4);
  counts.writeUInt16LE(opts.clientCount, 0);
  counts.writeUInt16LE(opts.maxPlayerCount, 2);
  const flags = Buffer.from([opts.gameMode, opts.needLogin ? 1 : 0, opts.needPasswd ? 1 : 0]);
  const time = Buffer.alloc(4);
  time.writeFloatLE(opts.timeOfDay ?? 0.5, 0);
  const inner = Buffer.concat([head, version, counts, flags, time]);
  return Buffer.concat([Buffer.from([0x09]), deflateRawSync(inner)]);
}

describe('server-info.protocol', () => {
  it('buildProbeRequest 产出 [0x09][rawDeflate(0x88,0x00,0x01)]', () => {
    const req = buildProbeRequest();
    expect(req[0]).toBe(0x09);
    const inner = inflateRawSync(req.subarray(1));
    expect([...inner]).toEqual([0x88, 0x00, 0x01]);
  });

  it('unwrapReply 去除 NetProperty 前缀并解压', () => {
    const reply = makeReply({ version: 'x26.06.19', clientCount: 3, maxPlayerCount: 20, gameMode: 2 });
    const inner = unwrapReply(reply);
    expect(inner).not.toBeNull();
    expect(inner![0]).toBe(0x88);
  });

  it('unwrapReply 对非探测载荷返回 null', () => {
    expect(unwrapReply(Buffer.from([0x00, 0x01]))).toBeNull();
    expect(unwrapReply(Buffer.from([]))).toBeNull();
  });

  it('parseServerInfo 正确解析字段并映射 GameMode', () => {
    const reply = makeReply({
      version: 'x26.06.19',
      clientCount: 5,
      maxPlayerCount: 20,
      gameMode: 2,
      needPasswd: true,
      timeOfDay: 0.25,
    });
    const parsed = decodeReply(reply);
    expect(parsed).toMatchObject({
      version: 'x26.06.19',
      playerCount: 5,
      maxCount: 20,
      gameMode: 'Survival',
      needPassword: true,
      timeOfDay: 0.25,
    });
  });

  it('parseServerInfo 对错误 marker 抛错', () => {
    const bad = Buffer.from([0x00, 0x00, 0x00, 0x00]);
    expect(() => parseServerInfo(bad)).toThrow();
  });
});
