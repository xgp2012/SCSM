import type { ReleaseAsset, ReleaseInfo, ServerPlatform, VersionSourceId } from '@sc-panel/shared';

/**
 * 版本来源抽象（plants.md §5.8）。
 *
 * 默认实现为 Gitee；接口保留 `listReleases()`，后续可新增 MirrorSource /
 * GiteeTokenSource 等第三方源并在 VersionService 中注册。下载统一走
 * `downloadBase`/`headers` 推导出的直链。
 */
export interface VersionRawAsset {
  name: string;
  url: string;
  size?: number;
}

export interface VersionRawRelease {
  tag: string;
  name: string;
  body?: string;
  publishedAt?: string;
  prerelease?: boolean;
  assets: VersionRawAsset[];
}

export interface VersionSource {
  readonly id: VersionSourceId;
  readonly label: string;
  /** 拉取 release 列表（原始结构） */
  listReleases(): Promise<VersionRawRelease[]>;
  /** 下载资源时附加的请求头（Referer/UA 等防盗链处理） */
  headers(): Record<string, string>;
  /** 面板数据目录（该源可选的本地文件索引/缓存根；第三方源可缺省） */
  dataDir?: string;
}

/** 判断 asset 是否为服务端包（命中服务端关键字，排除电脑版/安卓版/源码归档） */
export function isServerPackageName(name: string): boolean {
  const lower = name.toLowerCase();
  if (lower.endsWith('.apk')) return false;
  // 源码自动归档（与 tag 同名的 zip/tar.gz，无平台标记，如 x26.06.19.zip）
  const isPlainArchive = /\.(zip|tar\.gz)$/i.test(lower) && !/[\[\]]/.test(name);
  if (isPlainArchive && !lower.includes('服务端')) return false;
  return lower.includes('服务端');
}

/** 解析 asset 平台 */
export function detectPlatform(name: string): ServerPlatform {
  const lower = name.toLowerCase();
  if (lower.includes('linux')) return 'linux';
  if (lower.includes('windows') || lower.includes('win')) return 'windows';
  return 'unknown';
}

/** 解析 asset 架构 */
export function detectArch(name: string): string | undefined {
  const lower = name.toLowerCase();
  if (lower.includes('arm64') || lower.includes('aarch64')) return 'arm64';
  if (lower.includes('x64') || lower.includes('x86_64') || lower.includes('amd64')) return 'x64';
  return undefined;
}

/**
 * 服务端包优先级评分（越大越优先）。
 *
 * 实测 Gitee 同时提供多种服务端形态：
 * - `服务端X26.07.01.01.zip` / `X26.07.01.01【Linuxx64】.zip` → 托管式
 *   `net10.0/Survivalcraft.dll`（本面板 PTY 托管的目标形态，最高优先）；
 * - `[服务端]SCNETx26.06.19z1.zip` → 同为 Survivalcraft 托管形态；
 * - `[服务端]PocketSurvival-*.zip` → C++/PSTerminal 变体（不同服务端，低优先）。
 */
export function serverPackageScore(name: string): number {
  const lower = name.toLowerCase();
  if (!isServerPackageName(name)) return -1;
  let score = 0;
  if (lower.includes('pocketsurvival') || lower.includes('psterminal')) score -= 10;
  // `服务端X26.07.01.01.zip`（无方括号前缀）为最新托管式命名，优先
  if (/^服务端/.test(name)) score += 6;
  if (lower.includes('survivalcraft')) score += 5;
  // 带 [服务端] 方括号标记的旧命名
  if (/\[服务端\]/.test(name)) score += 2;
  if (lower.includes('scnet')) score += 1;
  return score;
}

/** 把来源的原始 release 结构映射为共享类型 */
export function toReleaseInfo(raw: VersionRawRelease): ReleaseInfo {
  const assets: ReleaseAsset[] = raw.assets.map((a) => ({
    name: a.name,
    url: a.url,
    size: a.size,
    isServerPackage: isServerPackageName(a.name),
    platform: detectPlatform(a.name),
    arch: detectArch(a.name),
    isSourceArchive: /archive\/refs\/tags\//.test(a.url),
  }));
  return {
    tag: raw.tag,
    name: raw.name,
    body: raw.body,
    publishedAt: raw.publishedAt,
    prerelease: raw.prerelease,
    assets,
  };
}
