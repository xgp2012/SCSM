import {
  BadRequestException,
  ConflictException,
  Injectable,
  Logger,
  NotFoundException,
  OnModuleDestroy,
  OnModuleInit,
} from '@nestjs/common';
import { randomUUID } from 'node:crypto';
import {
  access,
  constants,
  mkdir,
  readFile,
  readdir,
  rename,
  rm,
  stat,
  writeFile,
} from 'node:fs/promises';
import { join, relative, resolve, sep } from 'node:path';
import type {
  BackupMeta,
  CreateBackupInput,
  InstanceMeta,
  RestoreBackupInput,
  WorldInfo,
} from '@sc-panel/shared';
import { InstanceStore } from '../instance/instance.store';
import { InstanceSupervisor } from '../instance/instance.supervisor';
import { SettingsService } from '../settings/settings.service';
import { createTarGz, extractTarGz } from './archive';

const BACKUP_DIR = 'backups';
const WORLDS_DIR = 'Worlds';
/** 归档内相对路径（POSIX） */
const CONFIG_ENTRIES = ['Settings.xml', 'ServerSetting.json', 'Configs', 'Plugins'] as const;

/**
 * 存档与备份（plants.md §5.6）。
 *
 * - 列存档：解析实例目录 `Worlds/<name>/`；
 * - 手动/定时备份：把世界目录 + 配置打包为 `backups/<id>.tar.gz`；
 * - 恢复：停止实例 → 预恢复安全快照 → 解压覆盖 → 保留未知配置；
 * - 下载/删除。
 *
 * 归档格式 tar.gz（流式），元数据 `backups/<id>.json`；原子写。
 * 定时间隔与保留数支持**运行期热更新**（来自 `SettingsService`，plants.md §6）。
 */
@Injectable()
export class BackupService implements OnModuleInit, OnModuleDestroy {
  private readonly logger = new Logger('Backup');
  private timer?: NodeJS.Timeout;
  private unsubscribeSettings?: () => void;

  constructor(
    private readonly store: InstanceStore,
    private readonly supervisor: InstanceSupervisor,
    private readonly settings: SettingsService,
  ) {}

  onModuleInit(): void {
    this.applySchedule();
    // 运行期设置变更 → 热更新定时器
    this.unsubscribeSettings = this.settings.onChange(() => this.applySchedule());
  }

  onModuleDestroy(): void {
    if (this.timer) clearInterval(this.timer);
    this.unsubscribeSettings?.();
  }

  /** 按当前设置（SettingsService）重设定时器 */
  private applySchedule(): void {
    if (this.timer) {
      clearInterval(this.timer);
      this.timer = undefined;
    }
    const intervalMs = this.settings.backup.intervalMs;
    if (intervalMs > 0) {
      this.timer = setInterval(() => {
        void this.runScheduledBackups();
      }, intervalMs);
      this.timer.unref?.();
      this.logger.log(
        `定时备份 ${intervalMs}ms 已启动（保留 ${this.settings.backup.keep}）`,
      );
    } else {
      this.logger.log('定时备份已禁用');
    }
  }

  // ── 查询 ─────────────────────────────────────────────

  /** 列出实例的全部世界存档目录 */
  async listWorlds(id: string): Promise<WorldInfo[]> {
    const meta = await this.requireMeta(id);
    const worldsRoot = join(meta.dir, WORLDS_DIR);
    if (!(await this.pathExists(worldsRoot))) return [];

    const current = this.currentWorldName(meta);
    const entries = await readdir(worldsRoot, { withFileTypes: true });
    const worlds: WorldInfo[] = [];
    for (const entry of entries) {
      if (!entry.isDirectory()) continue;
      const dir = join(worldsRoot, entry.name);
      const info = await this.worldInfo(entry.name, dir, current);
      worlds.push(info);
    }
    return worlds.sort((a, b) =>
      a.isCurrent === b.isCurrent ? a.name.localeCompare(b.name) : a.isCurrent ? -1 : 1,
    );
  }

  /** 列出实例的备份（按时间倒序） */
  async listBackups(id: string): Promise<BackupMeta[]> {
    const meta = await this.requireMeta(id);
    const dir = join(meta.dir, BACKUP_DIR);
    if (!(await this.pathExists(dir))) return [];
    const entries = await readdir(dir, { withFileTypes: true });
    const backups: BackupMeta[] = [];
    for (const entry of entries) {
      if (!entry.isFile() || !entry.name.endsWith('.json')) continue;
      try {
        const raw = await readFile(join(dir, entry.name), 'utf8');
        const b = JSON.parse(raw) as BackupMeta;
        // 归档丢失则跳过（保持列表与实际文件一致）
        if (await this.pathExists(join(dir, b.file))) backups.push(b);
      } catch (err) {
        this.logger.warn(`[${id}] 读取备份元数据失败 ${entry.name}: ${String(err)}`);
      }
    }
    return backups.sort((a, b) => b.createdAt.localeCompare(a.createdAt));
  }

  async getBackup(id: string, backupId: string): Promise<BackupMeta> {
    const meta = await this.requireMeta(id);
    const path = this.metaPath(meta.dir, backupId);
    if (!(await this.pathExists(path))) throw new NotFoundException(`备份不存在: ${backupId}`);
    return JSON.parse(await readFile(path, 'utf8')) as BackupMeta;
  }

  /** 备份归档的绝对路径（供下载） */
  async getArchivePath(id: string, backupId: string): Promise<string> {
    const meta = await this.requireMeta(id);
    const backup = await this.getBackup(id, backupId);
    const archive = join(meta.dir, BACKUP_DIR, backup.file);
    if (!(await this.pathExists(archive))) throw new NotFoundException(`备份归档已丢失: ${backupId}`);
    return this.assertInside(meta.dir, archive);
  }

  // ── 备份/恢复 ────────────────────────────────────────

  /** 创建一次备份（手动或定时） */
  async createBackup(
    id: string,
    input: CreateBackupInput = {},
    type: BackupMeta['type'] = 'manual',
    note?: string,
  ): Promise<BackupMeta> {
    const meta = await this.requireMeta(id);
    // 与世界数据一致性：运行中禁止备份，避免快照撕裂
    if (this.supervisor.isAlive(id)) {
      throw new ConflictException('实例正在运行，请先停止后再创建备份（避免存档快照不一致）');
    }

    const worldName = input.world ?? this.currentWorldName(meta);
    const worldDir = join(meta.dir, WORLDS_DIR, worldName);
    if (!(await this.pathExists(worldDir))) {
      throw new BadRequestException(`世界存档不存在: ${WORLDS_DIR}/${worldName}`);
    }

    const includeConfig = input.includeConfig ?? true;
    const entries: string[] = [`${WORLDS_DIR}/${worldName}`];
    if (includeConfig) {
      for (const entry of CONFIG_ENTRIES) {
        if (await this.pathExists(join(meta.dir, entry))) entries.push(entry);
      }
    }

    const backupId = randomUUID();
    const file = `${backupId}.tar.gz`;
    const archivePath = join(meta.dir, BACKUP_DIR, file);
    const size = await createTarGz(archivePath, meta.dir, entries);
    if (size <= 0) throw new Error('归档结果为空');

    const backup: BackupMeta = {
      id: backupId,
      instanceId: id,
      file,
      size,
      createdAt: new Date().toISOString(),
      type,
      world: worldName,
      includes: { world: true, config: includeConfig },
      version: meta.version,
      note,
    };
    await this.writeMeta(meta.dir, backup);
    await this.enforceRetention(meta);
    this.logger.log(
      `[${id}] 备份完成 ${file} (${this.mb(size)}MB, world=${worldName}, config=${includeConfig}, type=${type})`,
    );
    return backup;
  }

  /** 恢复备份：停止实例 → 预快照 → 解压覆盖 */
  async restoreBackup(
    id: string,
    backupId: string,
    input: RestoreBackupInput = {},
  ): Promise<{ restored: BackupMeta; snapshot?: BackupMeta }> {
    const meta = await this.requireMeta(id);
    const backup = await this.getBackup(id, backupId);
    const archivePath = await this.getArchivePath(id, backupId);

    if (this.supervisor.isAlive(id)) {
      await this.supervisor.stop(id);
    }

    // 恢复前自动安全快照（可回滚）
    let snapshot: BackupMeta | undefined;
    if (input.snapshot ?? true) {
      try {
        snapshot = await this.createBackup(
          id,
          { world: this.currentWorldName(meta), includeConfig: true },
          'pre-restore',
          `恢复 ${backupId} 前的自动快照`,
        );
      } catch (err) {
        this.logger.warn(`[${id}] 预恢复快照失败（继续恢复）: ${String(err)}`);
      }
    }

    await extractTarGz(archivePath, meta.dir);
    this.logger.log(`[${id}] 已从备份 ${backupId} 恢复（world=${backup.world}）`);
    return { restored: backup, snapshot };
  }

  async removeBackup(id: string, backupId: string): Promise<void> {
    const meta = await this.requireMeta(id);
    const backup = await this.getBackup(id, backupId);
    const dir = join(meta.dir, BACKUP_DIR);
    await rm(join(dir, backup.file), { force: true });
    await rm(this.metaPath(meta.dir, backupId), { force: true });
    this.logger.log(`[${id}] 已删除备份 ${backupId}`);
  }

  // ── 定时 ─────────────────────────────────────────────

  /** 对所有 stopped 实例执行一次定时备份 */
  async runScheduledBackups(): Promise<void> {
    const metas = await this.store.list();
    for (const meta of metas) {
      if (this.supervisor.isAlive(meta.id)) continue;
      const worldDir = join(meta.dir, WORLDS_DIR, this.currentWorldName(meta));
      if (!(await this.pathExists(worldDir))) continue;
      try {
        await this.createBackup(meta.id, {}, 'auto', '定时自动备份');
      } catch (err) {
        this.logger.warn(`[${meta.id}] 定时备份失败: ${String(err)}`);
      }
    }
  }

  // ── 内部 ─────────────────────────────────────────────

  private async requireMeta(id: string): Promise<InstanceMeta> {
    const meta = await this.store.read(id);
    if (!meta) throw new NotFoundException(`实例不存在: ${id}`);
    return meta;
  }

  /** 从 ServerSetting.json.WorldPath（app:/Worlds/World）解析世界名 */
  private currentWorldName(meta: InstanceMeta): string {
    const wp = meta.worldPath ?? '';
    const normalized = wp.replace(/^app:\/?/i, '');
    const parts = normalized.split(/[\\/]/).filter(Boolean);
    const idx = parts.findIndex((p) => p.toLowerCase() === WORLDS_DIR.toLowerCase());
    if (idx >= 0 && parts[idx + 1]) return parts[idx + 1];
    return meta.worldName || 'World';
  }

  private async worldInfo(name: string, dir: string, current: string): Promise<WorldInfo> {
    const { size, files, lastModified } = await this.directorySize(dir);
    return {
      name,
      relPath: `${WORLDS_DIR}/${name}`,
      dir,
      isCurrent: name === current,
      size,
      files,
      lastModified,
      hasProject: await this.pathExists(join(dir, 'Project.json')),
    };
  }

  private async directorySize(
    dir: string,
  ): Promise<{ size: number; files: number; lastModified?: string }> {
    let size = 0;
    let files = 0;
    let lastModifiedMs = 0;
    const walk = async (current: string): Promise<void> => {
      const entries = await readdir(current, { withFileTypes: true });
      for (const entry of entries) {
        const full = join(current, entry.name);
        if (entry.isDirectory()) {
          await walk(full);
        } else if (entry.isFile()) {
          try {
            const s = await stat(full);
            size += s.size;
            files += 1;
            if (s.mtimeMs > lastModifiedMs) lastModifiedMs = s.mtimeMs;
          } catch {
            // ignore
          }
        }
      }
    };
    await walk(dir);
    return {
      size,
      files,
      lastModified: lastModifiedMs ? new Date(lastModifiedMs).toISOString() : undefined,
    };
  }

  private metaPath(dir: string, backupId: string): string {
    return join(dir, BACKUP_DIR, `${backupId}.json`);
  }

  private async writeMeta(dir: string, backup: BackupMeta): Promise<void> {
    await mkdir(join(dir, BACKUP_DIR), { recursive: true });
    const path = this.metaPath(dir, backup.id);
    const tmp = `${path}.tmp`;
    await writeFile(tmp, JSON.stringify(backup, null, 2), 'utf8');
    await rename(tmp, path);
  }

  /** 超出保留数量则按时间从旧到新删除（pre-restore 快照优先保留） */
  private async enforceRetention(meta: InstanceMeta): Promise<void> {
    const keep = this.settings.backup.keep;
    if (keep <= 0) return;
    const backups = await this.listBackups(meta.id);
    if (backups.length <= keep) return;
    const removable = backups
      .filter((b) => b.type !== 'pre-restore')
      .sort((a, b) => a.createdAt.localeCompare(b.createdAt));
    let overflow = backups.length - keep;
    for (const b of removable) {
      if (overflow <= 0) break;
      await this.removeBackup(meta.id, b.id);
      overflow -= 1;
    }
  }

  private assertInside(root: string, target: string): string {
    const absRoot = resolve(root);
    const absTarget = resolve(target);
    const rel = relative(absRoot, absTarget);
    if (rel.startsWith('..') || rel.split(sep).includes('..')) {
      throw new BadRequestException('路径越界');
    }
    return absTarget;
  }

  private mb(bytes: number): string {
    return (bytes / 1024 / 1024).toFixed(2);
  }

  private async pathExists(path: string): Promise<boolean> {
    try {
      await access(path, constants.F_OK);
      return true;
    } catch {
      return false;
    }
  }
}
