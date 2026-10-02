import type { GameMode } from './instance';

/**
 * SurvivalcraftNet UDP ServerInfo 探测协议（plants.md §0.3 / §5.7）。
 *
 * 实测（x26.06.19）：客户端/面板直接向实例 ServerPort 发送 **单播** 未连接消息：
 *   UDP 载荷 = [0x09 (LiteNetLib PacketProperty.UnconnectedMessage)]
 *              + RAWDEFLATE( 0x88 + 包ID(ServerInfo=0) + bool requestInfo=true )
 * 服务端回执单播至来源地址，格式：
 *   [0x09] + RAWDEFLATE( 0x88 + 0x00 + bool requestInfo=false
 *                        + version(7bit 长度前缀 UTF8)
 *                        + u16 clientCount + u16 maxPlayerCount
 *                        + u8 gameMode + bool needLogin + bool needPasswd
 *                        + f32 timeOfDay )
 *
 * 注：`BroadcastPort` 在编译产物中不存在（仅 ServerPort），LAN 广播发现依赖未编译常量；
 * 面板探测只需已知 ServerPort，故采用单播，不依赖 BroadcastPort（见 §11-Q3）。
 */
export const PROBE_PACKAGE_MARKER = 0x88;
export const PROBE_PACKAGE_ID_SERVER_INFO = 0x00;
/** LiteNetLib PacketProperty.UnconnectedMessage */
export const PROBE_NET_PROPERTY = 0x09;
/** 默认探测单播超时（ms） */
export const PROBE_TIMEOUT_MS = 1500;

/** GameMode 枚举（对齐服务端 GameMode：0=Creative,1=Cruel?,2=Survival... 实测 2=Survival） */
export const GAME_MODE_NAMES: Record<number, GameMode> = {
  0: 'Creative',
  1: 'Cruel',
  2: 'Survival',
  3: 'Adventure',
};

export function gameModeName(value: number): GameMode {
  return GAME_MODE_NAMES[value] ?? String(value);
}
