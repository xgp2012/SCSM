import { createHash } from 'node:crypto';

/**
 * 归档类型识别与安全文件名工具（plants.md §5.8 / §7.3）。
 */

export type ArchiveKind = 'zip' | 'tar.gz' | 'unknown';

/** 由文件名/URL 推断归档类型 */
export function detectArchiveKind(input: string): ArchiveKind {
  const name = input.split('?')[0].toLowerCase();
  if (name.endsWith('.zip')) return 'zip';
  if (name.endsWith('.tar.gz') || name.endsWith('.tgz')) return 'tar.gz';
  return 'unknown';
}

/** 安全化文件名（去除路径分隔与危险字符，防目录穿越） */
export function safeFilename(name: string): string {
  const base = name.replace(/[\\/]+/g, '_').replace(/[\u0000-\u001f]/g, '');
  const cleaned = base.replace(/^\.+/, '').trim();
  return cleaned || 'version';
}

/** 去除可执行扩展名（.exe/.dll 等）用于识别 dotnet muxer */
export function stripExecutableExt(name: string): string {
  return name.toLowerCase().replace(/\.(exe|cmd|bat|sh|com)$/i, '');
}

/** 计算缓冲区 sha256（十六进制） */
export function sha256Hex(buf: Buffer): string {
  return createHash('sha256').update(buf).digest('hex');
}

/** 人类可读字节数 */
export function formatBytes(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes <= 0) return '0 B';
  const units = ['B', 'KB', 'MB', 'GB', 'TB'];
  const i = Math.min(Math.floor(Math.log(bytes) / Math.log(1024)), units.length - 1);
  return `${(bytes / 1024 ** i).toFixed(i === 0 ? 0 : 2)} ${units[i]}`;
}
