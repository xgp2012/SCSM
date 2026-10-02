/**
 * 服务端配置管理类型（plants.md §5.5 / §9.5.2 M4）。
 *
 * 编译产物实测（`Survivalcraft.dll`，x26.06.19）：
 * - 由 `ServerEssentialPlugin` 真实读写的文件为
 *   `Settings.xml`、`ServerSetting.json`、`Configs/LevelConfig.json`、`Configs/BanConfig.json`；
 * - 源码中的 `Configs/LimitConfig.json`、`Plugins/Limit.json`、`Plugins/Password.json`
 *   在编译产物中**未出现**（`LimitView` 等字段亦未编译），处理为「源码/遗留文件」，
 *   面板仍提供读写，但界面标注其可能不被当前服务端消费。
 */

/** 密码在界面与接口中的占位符（回显时脱敏） */
export const SECRET_PLACEHOLDER = '********';

/** 权限等级（`Configs/LevelConfig.json`：`{ 玩家名: 等级 }`） */
export type LevelConfig = Record<string, number>;

/** 封禁配置（`Configs/BanConfig.json`） */
export interface BanConfig {
  BanUserList: string[];
  BanUserIpList: string[];
  BanIpList: string[];
  [key: string]: unknown;
}

/**
 * 限制插件配置（`Configs/LimitConfig.json` 遗留文件 / `Plugins/Limit.json` 源码）。
 * 保留未知键，避免破坏其它字段。
 */
export interface LimitConfig {
  WaterLength: number;
  MagmaLength: number;
  LimitBlockBreak: boolean;
  LimitExplode: boolean;
  LimitView?: boolean;
  LimitViewLength?: number;
  [key: string]: unknown;
}

/** 玩家密码插件配置（`Plugins/Password.json`，源码未编译进当前服务端） */
export interface PasswordConfig {
  IsUse: boolean;
  /** 回显时为 `SECRET_PLACEHOLDER`；提交占位符表示保持原值不变 */
  DefaultPassword: string;
  /** 回显时各值为 `SECRET_PLACEHOLDER`；提交占位符表示保持原值 */
  PlayerPassword: Record<string, string>;
  [key: string]: unknown;
}

/** `ServerSetting.json` 的可编辑世界参数（对齐服务端实测字段，保留未知键） */
export interface ServerSettingConfig {
  CheckLogin?: boolean;
  ScKeyServerId?: string;
  ScKeyServerName?: string;
  Autorun?: boolean;
  AutoGenerateWorld?: boolean;
  WorldPath?: string;
  WorldName?: string;
  WorldSeed?: string;
  /** 回显时为 `SECRET_PLACEHOLDER`；提交占位符表示保持原值 */
  WorldPassword?: string;
  WorldKeywordBlocking?: string;
  WorldMaxPlayers?: number;
  WorldDaySpeed?: number;
  WorldRecoverySpeed?: number;
  WorldDisableBlocks?: string;
  RandomSpawnPosition?: boolean;
  /** 0=Creative 1=Cruel 2=Survival 3=Adventure（实测 2=Survival） */
  GameMode?: number;
  SeasonChanging?: boolean;
  PVPEnabled?: boolean;
  [key: string]: unknown;
}

/** 配置分区标识 */
export type ConfigSection =
  | 'settings'
  | 'serverSetting'
  | 'level'
  | 'ban'
  | 'limit'
  | 'password';

/** 分区可写性 / 生效说明（供前端展示） */
export interface ConfigSectionMeta {
  key: ConfigSection;
  label: string;
  /** 对应服务端文件（相对实例目录） */
  file: string;
  /** 是否由当前编译版服务端真实消费（否则标注为源码/遗留） */
  effective: boolean;
  /** 运行中是否允许写入 */
  writableWhileRunning: boolean;
  note?: string;
}

export interface ConfigSectionMetaEntry {
  key: ConfigSection;
  label: string;
  file: string;
  effective: boolean;
  writableWhileRunning: boolean;
  note?: string;
}

/**
 * 面板配置快照。
 * - `settings`：`Settings.xml` 的 `{ Name: Value }`（字符串）
 * - `serverSetting`：`ServerSetting.json` 解析结果（敏感字段脱敏）
 * - 其余为 JSON 配置分区（密码类脱敏）
 */
export interface ConfigBundle {
  instanceId: string;
  running: boolean;
  sections: {
    settings: Record<string, string>;
    serverSetting: ServerSettingConfig;
    level: LevelConfig;
    ban: BanConfig;
    /** 无文件时为 null */
    limit: LimitConfig | null;
    /** 无文件时为 null */
    password: PasswordConfig | null;
  };
  meta: ConfigSectionMeta[];
}

/** 更新实例设置入参（PATCH /instances/:id 的扩展字段） */
export interface InstanceSettingsInput {
  serverPort?: number;
  worldName?: string;
  maxPlayers?: number;
  autoRun?: boolean;
  autoRestart?: boolean;
  autoGenerateWorld?: boolean;
  worldSeed?: string;
  gameMode?: number;
  pvpEnabled?: boolean;
  seasonChanging?: boolean;
  /** 明文或 `SECRET_PLACEHOLDER`（保持原值） */
  worldPassword?: string;
}
