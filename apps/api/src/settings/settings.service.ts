import { Inject, Injectable, Logger, OnModuleInit } from '@nestjs/common';
import { mkdir, readFile, rename, writeFile } from 'node:fs/promises';
import { join } from 'node:path';
import type {
  BackupPolicy,
  DownloadPolicy,
  PanelSettings,
  UiPreferences,
  UpdatePanelSettingsInput,
} from '@sc-panel/shared';
import type { PanelEnv } from '../config/env';
import { PANEL_ENV } from '../config/env.module';
import { toAbsolute } from '../common/utils/paths';

const FILE = 'panel-settings.json';

/**
 * 面板运行期设置（plants.md §6 `/settings` · §10 M7）。
 *
 * 持久化 `<PANEL_DATA_DIR>/panel-settings.json`（原子写）。
 * 备份策略、下载镜像等**运行期可调**，无需重启：相关服务订阅 `changed` 事件热更新。
 */
@Injectable()
export class SettingsService implements OnModuleInit {
  private readonly logger = new Logger('Settings');
  private readonly root: string;
  private current!: PanelSettings;
  private readonly listeners = new Set<(s: PanelSettings) => void>();

  constructor(@Inject(PANEL_ENV) private readonly env: PanelEnv) {
    this.root = toAbsolute(env.PANEL_DATA_DIR);
    // 同步初始化默认值，避免其它服务在 onModuleInit 之前读取时拿到 undefined
    this.current = this.defaults();
  }

  async onModuleInit(): Promise<void> {
    this.current = await this.load();
    // 已落盘设置可能覆盖构造期默认值，通知订阅者（如 VersionService 重建来源）
    for (const l of this.listeners) {
      try {
        l(this.current);
      } catch {
        // ignore
      }
    }
  }

  /** 当前设置快照 */
  get(): PanelSettings {
    return this.current;
  }

  /** 备份策略（供备份服务热读取） */
  get backup(): BackupPolicy {
    return this.current.backup;
  }

  /** 下载策略（供版本服务热读取） */
  get download(): DownloadPolicy {
    return this.current.download;
  }

  /** 订阅设置变更（返回取消订阅函数） */
  onChange(listener: (s: PanelSettings) => void): () => void {
    this.listeners.add(listener);
    return () => this.listeners.delete(listener);
  }

  /** 更新设置（浅合并各分组） */
  async update(input: UpdatePanelSettingsInput): Promise<PanelSettings> {
    const next: PanelSettings = {
      backup: { ...this.current.backup, ...sanitizeBackup(input.backup) },
      download: { ...this.current.download, ...sanitizeDownload(input.download) },
      ui: { ...this.current.ui, ...sanitizeUi(input.ui) },
      runtime: this.current.runtime,
    };
    await this.persist(next);
    this.current = next;
    for (const l of this.listeners) {
      try {
        l(next);
      } catch {
        // 单个监听者异常不影响其它
      }
    }
    this.logger.log(
      `设置已更新 interval=${next.backup.intervalMs} keep=${next.backup.keep} mirror=${next.download.giteeMirror || '-'}`,
    );
    return next;
  }

  private async load(): Promise<PanelSettings> {
    const defaults = this.defaults();
    try {
      const raw = await readFile(join(this.root, FILE), 'utf8');
      const parsed = JSON.parse(raw) as Partial<PanelSettings>;
      return {
        backup: { ...defaults.backup, ...sanitizeBackup(parsed.backup) },
        download: { ...defaults.download, ...sanitizeDownload(parsed.download) },
        ui: { ...defaults.ui, ...sanitizeUi(parsed.ui) },
        runtime: defaults.runtime,
      };
    } catch {
      return defaults;
    }
  }

  private defaults(): PanelSettings {
    return {
      backup: {
        intervalMs: this.env.BACKUP_INTERVAL_MS,
        keep: this.env.BACKUP_KEEP,
      },
      download: {
        giteeMirror: this.env.GITEE_MIRROR,
        maxBytes: this.env.VERSION_MAX_BYTES,
        cacheMs: this.env.VERSION_CACHE_MS,
      },
      ui: { theme: 'dark' },
      runtime: {
        version: process.env.npm_package_version ?? '0.0.0',
        node: process.version,
        platform: `${process.platform}-${process.arch}`,
        dataDir: this.root,
        uptimeSec: Math.round(process.uptime()),
      },
    };
  }

  private async persist(s: PanelSettings): Promise<void> {
    await mkdir(this.root, { recursive: true });
    const path = join(this.root, FILE);
    const tmp = `${path}.tmp`;
    await writeFile(tmp, JSON.stringify(s, null, 2), 'utf8');
    await rename(tmp, path);
  }
}

function sanitizeBackup(input: Partial<BackupPolicy> | undefined): Partial<BackupPolicy> {
  const out: Partial<BackupPolicy> = {};
  if (input?.intervalMs !== undefined && input.intervalMs >= 0) out.intervalMs = Math.floor(input.intervalMs);
  if (input?.keep !== undefined && input.keep >= 0) out.keep = Math.floor(input.keep);
  return out;
}

function sanitizeDownload(input: Partial<DownloadPolicy> | undefined): Partial<DownloadPolicy> {
  const out: Partial<DownloadPolicy> = {};
  if (input?.giteeMirror !== undefined) out.giteeMirror = String(input.giteeMirror).trim();
  if (input?.maxBytes !== undefined && input.maxBytes > 0) out.maxBytes = Math.floor(input.maxBytes);
  if (input?.cacheMs !== undefined && input.cacheMs >= 0) out.cacheMs = Math.floor(input.cacheMs);
  return out;
}

function sanitizeUi(input: Partial<UiPreferences> | undefined): Partial<UiPreferences> {
  const out: Partial<UiPreferences> = {};
  if (input?.theme && ['dark', 'light', 'system'].includes(input.theme)) out.theme = input.theme;
  return out;
}
