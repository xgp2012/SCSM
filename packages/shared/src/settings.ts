/**
 * 面板设置类型（plants.md §6 `/settings` · §10 M7）。
 *
 * 持久化于 `<PANEL_DATA_DIR>/panel-settings.json`（原子写），
 * 覆盖备份策略、下载镜像、主题等**运行期可调**项；启动默认值来自环境变量。
 */

/** 运行时备份策略（覆盖 env 的 BACKUP_INTERVAL_MS / BACKUP_KEEP，无需重启） */
export interface BackupPolicy {
  /** 定时备份间隔（ms），0 表示禁用 */
  intervalMs: number;
  /** 每实例保留备份数，0 不限制 */
  keep: number;
}

/** 下载/镜像设置 */
export interface DownloadPolicy {
  /** Gitee 镜像 host（空表示直连 gitee.com） */
  giteeMirror: string;
  /** 单个版本包大小上限（字节） */
  maxBytes: number;
  /** releases 列表缓存时长（ms），0 不缓存 */
  cacheMs: number;
}

/** 界面偏好 */
export type ThemePreference = 'dark' | 'light' | 'system';

export interface UiPreferences {
  theme: ThemePreference;
}

/** 面板设置快照（含只读的运行时信息） */
export interface PanelSettings {
  backup: BackupPolicy;
  download: DownloadPolicy;
  ui: UiPreferences;
  /** 只读：只读信息，展示用 */
  runtime: {
    version: string;
    node: string;
    platform: string;
    dataDir: string;
    uptimeSec: number;
  };
}

/** 更新入参（部分字段） */
export interface UpdatePanelSettingsInput {
  backup?: Partial<BackupPolicy>;
  download?: Partial<DownloadPolicy>;
  ui?: Partial<UiPreferences>;
}

/** 修改管理员密码入参 */
export interface ChangePasswordInput {
  currentPassword: string;
  newPassword: string;
}
