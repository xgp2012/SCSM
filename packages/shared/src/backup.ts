/** 备份类型（plants.md §5.6 / §9.5.2 M5） */
export type BackupType = 'manual' | 'auto' | 'pre-restore';

export const BACKUP_TYPES: readonly BackupType[] = ['manual', 'auto', 'pre-restore'] as const;

/** 备份归档包含的内容范围 */
export interface BackupIncludes {
  /** 世界存档目录（Worlds/<world>） */
  world: boolean;
  /** 配置（Settings.xml / ServerSetting.json / Configs / Plugins） */
  config: boolean;
}

/** 备份元数据（持久化于实例目录 backups/<id>.json） */
export interface BackupMeta {
  id: string;
  instanceId: string;
  /** 归档文件名（相对实例目录 backups/） */
  file: string;
  /** 归档字节大小 */
  size: number;
  createdAt: string;
  type: BackupType;
  /** 备份的世界名 */
  world: string;
  includes: BackupIncludes;
  /** 备份时的服务端版本（可选） */
  version?: string;
  /** 备注（如定时触发/恢复前自动快照） */
  note?: string;
}

/** 一个世界存档目录的信息 */
export interface WorldInfo {
  /** 世界名（Worlds/ 下的目录名） */
  name: string;
  /** 相对实例目录的路径，如 Worlds/World */
  relPath: string;
  /** 绝对路径 */
  dir: string;
  /** 是否 ServerSetting.json.WorldPath 指向的当前世界 */
  isCurrent: boolean;
  /** 目录总字节大小 */
  size: number;
  /** 文件数量 */
  files: number;
  /** 最近修改时间（ISO） */
  lastModified?: string;
  /** 是否存在 Project.json（世界数据） */
  hasProject: boolean;
}

/** 创建备份入参 */
export interface CreateBackupInput {
  /** 指定备份的世界名；缺省使用当前世界 */
  world?: string;
  /** 是否包含配置，默认 true */
  includeConfig?: boolean;
  /** 备注 */
  note?: string;
}

/** 恢复备份入参 */
export interface RestoreBackupInput {
  /** 恢复前是否自动生成安全快照，默认 true */
  snapshot?: boolean;
}
