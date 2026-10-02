import { accessSync, constants, existsSync } from 'node:fs';
import { isAbsolute, join } from 'node:path';
import { delimiter } from 'node:path';

/**
 * 解析可执行文件为绝对路径。
 *
 * Windows 上 ConPTY（node-pty）不接受裸命令名（会抛 "File not found"），
 * 必须给出完整路径；Linux/macOS 则允许 PATH 查找。
 */

function isExecutable(path: string): boolean {
  try {
    accessSync(path, constants.F_OK);
    return true;
  } catch {
    return false;
  }
}

export function resolveExecutable(command: string, extraDirs: string[] = []): string {
  if (isAbsolute(command)) return command;

  const exts =
    process.platform === 'win32'
      ? (process.env.PATHEXT ?? '.EXE;.CMD;.BAT;.COM').split(';')
      : [''];

  const dirs = [
    ...extraDirs,
    ...(process.env.PATH ?? '').split(delimiter).filter(Boolean),
  ];

  for (const dir of dirs) {
    for (const ext of exts) {
      const candidate = join(dir, `${command}${ext}`);
      if (isExecutable(candidate)) return candidate;
    }
  }

  return command;
}

/** 常见 dotnet 安装位置（Windows），作为 PATH 缺失时的兜底 */
export function dotnetSearchDirs(): string[] {
  if (process.platform !== 'win32') return [];
  const pf = process.env.ProgramFiles ?? 'C:\\Program Files';
  const pf86 = process.env['ProgramFiles(x86)'] ?? 'C:\\Program Files (x86)';
  return [join(pf, 'dotnet'), join(pf86, 'dotnet')].filter((d) => existsSync(d));
}
