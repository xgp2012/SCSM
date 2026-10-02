import { deflateRawSync, inflateRawSync } from 'node:zlib';
import {
  gameModeName,
  PROBE_NET_PROPERTY,
  PROBE_PACKAGE_ID_SERVER_INFO,
  PROBE_PACKAGE_MARKER,
} from '@sc-panel/shared';
import type { ProbeResult } from '@sc-panel/shared';

/**
 * ServerInfo 探测报文编解码（plants.md §0.3 / §5.7）。
 * 协议细节见 `packages/shared/src/probe.ts` 注释。
 */

/** 构造探测请求的 UDP 载荷：[0x09][rawDeflate(0x88, id, requestInfo=true)] */
export function buildProbeRequest(): Buffer {
  const inner = Buffer.from([
    PROBE_PACKAGE_MARKER,
    PROBE_PACKAGE_ID_SERVER_INFO,
    0x01, // requestInfo = true
  ]);
  const deflated = deflateRawSync(inner);
  return Buffer.concat([Buffer.from([PROBE_NET_PROPERTY]), deflated]);
}

/** 解出回执内层字节（去掉 NetProperty 前缀并 raw-inflate），失败返回 null */
export function unwrapReply(payload: Buffer): Buffer | null {
  if (!payload || payload.length < 2) return null;
  if (payload[0] !== PROBE_NET_PROPERTY) return null;
  try {
    return inflateRawSync(payload.subarray(1));
  } catch {
    return null;
  }
}

/** 解析 ServerInfo 回执（已 inflate 的内层字节） */
export function parseServerInfo(inner: Buffer): Omit<ProbeResult, 'instanceId' | 'online' | 'pingMs'> {
  let o = 0;
  const marker = inner[o++];
  const id = inner[o++];
  const requestInfo = inner[o++] === 1;
  if (marker !== PROBE_PACKAGE_MARKER || id !== PROBE_PACKAGE_ID_SERVER_INFO || requestInfo) {
    throw new Error(`非 ServerInfo 回执: marker=${marker} id=${id} req=${requestInfo}`);
  }
  const versionLen = inner[o++];
  const version = inner.subarray(o, o + versionLen).toString('utf8');
  o += versionLen;
  const playerCount = inner.readUInt16LE(o);
  o += 2;
  const maxCount = inner.readUInt16LE(o);
  o += 2;
  const gameModeRaw = inner[o++];
  const needLogin = inner[o++] === 1;
  const needPassword = inner[o++] === 1;
  const timeOfDay = inner.readFloatLE(o);
  o += 4;
  return {
    version,
    playerCount,
    maxCount,
    gameMode: gameModeName(gameModeRaw),
    needLogin,
    needPassword,
    timeOfDay,
  };
}

/** 便捷：从原始 UDP 回执字节直接解析（含 unwrap） */
export function decodeReply(payload: Buffer): Omit<ProbeResult, 'instanceId' | 'online' | 'pingMs'> | null {
  const inner = unwrapReply(payload);
  if (!inner) return null;
  try {
    return parseServerInfo(inner);
  } catch {
    return null;
  }
}
