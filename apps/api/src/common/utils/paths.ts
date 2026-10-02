import { isAbsolute, resolve } from 'node:path';

/**
 * 把相对路径解析为绝对路径（相对 process.cwd()）。
 * 面板数据目录、模板目录等来自 .env，避免各处重复判断。
 */
export function toAbsolute(p: string, base = process.cwd()): string {
  return isAbsolute(p) ? p : resolve(base, p);
}
