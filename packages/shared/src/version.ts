/**
 * 服务端版本下载与安装类型（plants.md §0.4 / §5.8 / §10 M6）。
 *
 * 设计目标：默认从 Gitee Releases 拉取，同时保留可扩展的第三方下载源抽象
 * （`VersionSource` + 可配置 `downloadBase`），以便接入镜像/自建源。
 */

/** 版本来源类型（默认 gitee；预留第三方镜像/自建源） */
export type VersionSourceId = 'gitee' | string;

/** 服务端目标平台（用于自动匹配 asset） */
export type ServerPlatform = 'windows' | 'linux' | 'unknown';

/** 一个 release 资源（下载资产） */
export interface ReleaseAsset {
  name: string;
  /** 下载直链（Gitee API 的 browser_download_url，含 302 跳转） */
  url: string;
  size?: number;
  /** 是否被识别为服务端包 */
  isServerPackage: boolean;
  /** 解析出的目标平台 */
  platform: ServerPlatform;
  /** 解析出的架构（如 x64 / arm64），未知时缺省 */
  arch?: string;
  /** 是否为源码归档（zip/tar.gz 自动生成），一般不用于安装 */
  isSourceArchive: boolean;
}

/** 一个 release（版本） */
export interface ReleaseInfo {
  tag: string;
  name: string;
  /** 发布说明（可能为空） */
  body?: string;
  publishedAt?: string;
  /** 是否为预发布 */
  prerelease?: boolean;
  assets: ReleaseAsset[];
  /** 与面板主机平台匹配的服务端包建议（存在时） */
  recommendedAsset?: string;
}

/** 版本源元信息（供前端展示与选择） */
export interface VersionSourceInfo {
  id: VersionSourceId;
  label: string;
  /** 仓库标识（gitee: owner/repo） */
  repo: string;
  /** 是否为面板当前默认源 */
  isDefault: boolean;
  /** 该源是否可用（最近一次拉取是否成功） */
  available: boolean;
}

/** releases 列表响应 */
export interface ReleaseListResponse {
  /** 实际使用的源 */
  source: VersionSourceId;
  /** 是否走了兜底路径（如 Gitee API 不可用） */
  fallback: boolean;
  /** 兜底/降级原因（可读） */
  notice?: string;
  releases: ReleaseInfo[];
  /** 面板主机平台（用于前端预选服务端包） */
  hostPlatform: ServerPlatform;
  /** 数据获取时间戳（ms） */
  fetchedAt: number;
  /** 是否来自缓存 */
  cached: boolean;
}

/** 安装目标：新建实例 or 覆盖既有实例 */
export type InstallMode = 'new' | 'overwrite';

/** 触发下载/安装任务的入参 */
export interface CreateDownloadInput {
  /** release tag */
  tag: string;
  /** asset 名称（从该 release 的 assets 中选择） */
  asset: string;
  /** 来源（默认 default=gitee） */
  source?: VersionSourceId;
  /** 安装模式：new / overwrite */
  mode: InstallMode;
  /** new 模式下的实例名（缺省用 tag） */
  name?: string;
  /** overwrite 模式下的目标实例 ID */
  instanceId?: string;
  /** 覆盖安装时：是否保留既有 Configs/Worlds（默认 true） */
  preserve?: boolean;
}

/** 下载任务阶段 */
export type DownloadPhase =
  | 'queued'
  | 'downloading'
  | 'extracting'
  | 'installing'
  | 'done'
  | 'failed';

/** 下载任务状态（REST 轮询） */
export interface DownloadTask {
  id: string;
  phase: DownloadPhase;
  tag: string;
  asset: string;
  mode: InstallMode;
  /** 总字节数（未知时缺省） */
  totalBytes?: number;
  /** 已下载字节数 */
  receivedBytes: number;
  /** 进度百分比 0-100（totalBytes 已知时） */
  percent?: number;
  /** 速度（字节/秒）与预计剩余（ms），可选 */
  speedBps?: number;
  etaMs?: number;
  /** new 模式：创建的实例 ID */
  instanceId?: string;
  /** 结果实例名 */
  instanceName?: string;
  /** 可读错误 */
  error?: string;
  /** 最近一次更新（ISO） */
  updatedAt: string;
  createdAt: string;
}

/** 手动上传版本包的入参 */
export interface UploadInstallInput {
  /** 安装模式：new / overwrite */
  mode: InstallMode;
  /** new 模式下的实例名 */
  name?: string;
  /** overwrite 模式下的目标实例 ID */
  instanceId?: string;
  /** 覆盖安装时：是否保留既有 Configs/Worlds（默认 true） */
  preserve?: boolean;
  /** 上传文件原始名（用于识别版本/平台，可选） */
  filename?: string;
}
