import { createGzip, createGunzip } from 'node:zlib';
import { createReadStream, createWriteStream } from 'node:fs';
import { mkdir, stat } from 'node:fs/promises';
import { pipeline } from 'node:stream/promises';
import { dirname } from 'node:path';
import { create, extract } from 'tar';

/**
 * tar.gz 流式归档（plants.md §5.6）。
 *
 * 使用 `tar` 的流式 create/extract 以降低大存档的内存占用。
 * 归档内路径统一使用 POSIX 分隔符（`portable: true`），保证跨平台可移植。
 */

/** 把 baseDir 下指定相对路径条目打包为 tar.gz，返回写入后的字节数 */
export async function createTarGz(
  outPath: string,
  baseDir: string,
  entries: string[],
): Promise<number> {
  await mkdir(dirname(outPath), { recursive: true });
  const archiveStream = create({ cwd: baseDir, portable: true }, entries);
  const out = createWriteStream(outPath);
  await pipeline(archiveStream, createGzip({ level: 6 }), out);
  const { size } = await stat(outPath);
  return size;
}

/** 解压 tar.gz 到目标目录（覆盖同名文件） */
export async function extractTarGz(archivePath: string, destDir: string): Promise<void> {
  await mkdir(destDir, { recursive: true });
  await pipeline(
    createReadStream(archivePath),
    createGunzip(),
    extract({ cwd: destDir, portable: true }),
  );
}
