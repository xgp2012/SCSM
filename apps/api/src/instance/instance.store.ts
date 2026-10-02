import { Injectable, Logger } from '@nestjs/common';
import { Inject } from '@nestjs/common';
import { mkdir, readFile, readdir, rename, rm, writeFile } from 'node:fs/promises';
import { join } from 'node:path';
import type { InstanceMeta } from '@sc-panel/shared';
import type { PanelEnv } from '../config/env';
import { PANEL_ENV } from '../config/env.module';
import { toAbsolute } from '../common/utils/paths';

/**
 * 实例元数据存储：JSON 文件（plants.md §4.1）。
 * 布局：`<PANEL_DATA_DIR>/instances/<id>/meta.json`。
 * 写入为原子写（.tmp → rename）。
 */
@Injectable()
export class InstanceStore {
  private readonly logger = new Logger('InstanceStore');
  private readonly root: string;

  constructor(@Inject(PANEL_ENV) env: PanelEnv) {
    this.root = join(toAbsolute(env.PANEL_DATA_DIR), 'instances');
  }

  private metaPath(id: string): string {
    return join(this.root, id, 'meta.json');
  }

  async init(): Promise<void> {
    await mkdir(this.root, { recursive: true });
  }

  async list(): Promise<InstanceMeta[]> {
    await this.init();
    const entries = await readdir(this.root, { withFileTypes: true });
    const metas: InstanceMeta[] = [];
    for (const entry of entries) {
      if (!entry.isDirectory()) continue;
      const meta = await this.read(entry.name);
      if (meta) metas.push(meta);
    }
    return metas.sort((a, b) => a.createdAt.localeCompare(b.createdAt));
  }

  async read(id: string): Promise<InstanceMeta | null> {
    try {
      const raw = await readFile(this.metaPath(id), 'utf8');
      return JSON.parse(raw) as InstanceMeta;
    } catch (err) {
      if ((err as NodeJS.ErrnoException).code === 'ENOENT') return null;
      this.logger.error(`读取实例 ${id} 元数据失败: ${String(err)}`);
      return null;
    }
  }

  async write(meta: InstanceMeta): Promise<void> {
    const dir = join(this.root, meta.id);
    await mkdir(dir, { recursive: true });
    const path = this.metaPath(meta.id);
    const tmp = `${path}.tmp`;
    await writeFile(tmp, JSON.stringify(meta, null, 2), 'utf8');
    await rename(tmp, path);
  }

  async remove(id: string): Promise<void> {
    await rm(join(this.root, id), { recursive: true, force: true });
  }
}
