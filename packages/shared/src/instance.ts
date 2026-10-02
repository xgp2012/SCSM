/** 实例生命周期状态机（对齐 plants.md §4.1 / §7） */
export type InstanceStatus =
  | 'stopped'
  | 'starting'
  | 'running'
  | 'stopping'
  | 'crashed';

export const INSTANCE_STATUSES: readonly InstanceStatus[] = [
  'stopped',
  'starting',
  'running',
  'stopping',
  'crashed',
] as const;

/** 游戏模式枚举（对齐服务端 ServerSetting.json.GameMode） */
export type GameMode = 'Creative' | 'Survival' | 'Cruel' | 'Adventure' | string;

/** 实例的基础包来源（用于独立目录初始化） */
export type InstanceSource = 'template' | 'upload' | 'import' | 'version';

/** 实例元数据（持久化部分） */
export interface InstanceMeta {
  id: string;
  name: string;
  /** 基础包来源：模板拷贝 / 上传 zip / 导入现有目录 */
  source: InstanceSource;
  /** 工作目录（含 Survivalcraft.dll / Settings.xml / ServerSetting.json） */
  dir: string;
  /** Settings.xml 的 ServerPort（游戏 UDP 端口） */
  serverPort: number;
  /** 广播端口（来源待确认，见 plants.md §11-Q3） */
  broadcastPort?: number;
  version: string;
  launchArgs: string[];
  /** ServerSetting.json.WorldPath，如 app:/Worlds/World */
  worldPath: string;
  /** ServerSetting.json.WorldName */
  worldName: string;
  /** ServerSetting.json.WorldMaxPlayers */
  maxPlayers: number;
  autoRestart: boolean;
  autoStart: boolean;
  /** ServerSetting.json.Autorun（无人值守开服） */
  autoRun: boolean;
  createdAt: string;
}

/** 实例运行时状态（内存，不持久化） */
export interface InstanceRuntime {
  status: InstanceStatus;
  pid?: number;
  online?: number;
  maxOnline?: number;
  gameMode?: GameMode;
  hasPassword?: boolean;
  /** 进程 CPU 占用百分比（0-100 * 核数） */
  cpu?: number;
  /** 进程内存占用（MB） */
  memMB?: number;
  startedAt?: string;
  uptimeMs?: number;
  /** 从启动日志解析出的实际监听端口（校验配置一致性） */
  serverPort?: number;
  /** UDP 探测是否在线（独立于进程状态，plant.md §5.7） */
  probeOnline?: boolean;
  /** 最近一次成功探测时间戳（ISO） */
  lastProbeAt?: string;
  /** 世界时间（0-1，来自探测） */
  timeOfDay?: number;
}

/** UDP ServerInfo 探测结果（plants.md §0.3 / §5.7） */
export interface ProbeResult {
  instanceId: string;
  online: boolean;
  /** 服务端版本字符串，如 x26.06.19 */
  version?: string;
  /** 在线玩家数 */
  playerCount?: number;
  /** 最大玩家数 */
  maxCount?: number;
  gameMode?: GameMode;
  /** 是否需要社区登录 */
  needLogin?: boolean;
  /** 是否需要密码 */
  needPassword?: boolean;
  /** 世界时间 0-1 */
  timeOfDay?: number;
  /** 探测往返耗时（ms） */
  pingMs?: number;
  /** 错误信息（离线时） */
  error?: string;
}

/** 系统/主机资源快照（plants.md §5.7） */
export interface SystemStats {
  /** 主机 CPU 总使用率百分比 */
  cpu: number;
  /** 主机内存使用率百分比 */
  memPercent: number;
  memUsedMB: number;
  memTotalMB: number;
  /** 面板数据盘使用情况 */
  diskPercent?: number;
  diskUsedMB?: number;
  diskTotalMB?: number;
  cpuCount: number;
  platform: string;
  uptimeSec: number;
  loadAvg: number[];
  ts: number;
}

/** 对外暴露的实例（元数据 + 运行时） */
export interface Instance extends InstanceMeta {
  runtime: InstanceRuntime;
}

/** 创建实例入参 */
export interface CreateInstanceInput {
  name: string;
  /** 显式指定工作目录；缺省时由面板在 data/instances/<id> 下生成 */
  dir?: string;
  /** 基础包来源（默认 template） */
  source?: InstanceSource;
  serverPort?: number;
  broadcastPort?: number;
  version?: string;
  launchArgs?: string[];
  worldName?: string;
  maxPlayers?: number;
  autoRestart?: boolean;
  autoStart?: boolean;
  autoRun?: boolean;
  /** 从 template 初始化时覆盖基础包路径（默认服务端本体模板） */
  templateDir?: string;
}

/** 更新实例入参（仅可持久化字段，且不含 dir/source） */
export type UpdateInstanceInput = Partial<
  Pick<
    InstanceMeta,
    | 'name'
    | 'serverPort'
    | 'broadcastPort'
    | 'version'
    | 'launchArgs'
    | 'worldPath'
    | 'worldName'
    | 'maxPlayers'
    | 'autoRestart'
    | 'autoStart'
    | 'autoRun'
  >
> & {
  /** ServerSetting.json 世界参数（直写，不落 meta） */
  autoGenerateWorld?: boolean;
  worldSeed?: string;
  gameMode?: number;
  pvpEnabled?: boolean;
  seasonChanging?: boolean;
  /** 明文或 SECRET_PLACEHOLDER（保持原值） */
  worldPassword?: string;
};

/** 实例操作类型 */
export type InstanceAction = 'start' | 'stop' | 'restart' | 'kill';

/** 概览统计 */
export interface OverviewResponse {
  instances: Instance[];
  total: number;
  running: number;
}

/** 实例创建前可用的基础包模板信息 */
export interface TemplateInfo {
  /** 模板目录绝对路径 */
  dir: string;
  exists: boolean;
  /** 是否包含 Survivalcraft.dll */
  hasServerBinary: boolean;
  version?: string;
}

