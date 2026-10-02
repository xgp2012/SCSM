/**
 * 审计日志类型（plants.md §9 / §10 M7）。
 *
 * 单管理员场景采用 **JSONL 追加文件**：`<PANEL_DATA_DIR>/audit/YYYY-MM-DD.jsonl`，
 * 每行一个 `AuditRecord`，按天轮转，追加写安全，无需外部依赖。
 */

/** 审计动作分类（覆盖 plants.md §9 要求：登录、启停、命令、配置、备份、下载） */
export type AuditAction =
  | 'auth.login'
  | 'auth.login.failed'
  | 'auth.logout'
  | 'auth.password.change'
  | 'instance.create'
  | 'instance.update'
  | 'instance.remove'
  | 'instance.start'
  | 'instance.stop'
  | 'instance.restart'
  | 'instance.kill'
  | 'instance.crash'
  | 'instance.config.update'
  | 'console.command'
  | 'backup.create'
  | 'backup.restore'
  | 'backup.remove'
  | 'backup.download'
  | 'version.download'
  | 'version.install'
  | 'version.upload'
  | 'settings.update';

/** 审计结果 */
export type AuditOutcome = 'ok' | 'failed';

/** 单条审计记录（JSONL 每行） */
export interface AuditRecord {
  id: string;
  /** ISO 时间戳 */
  ts: string;
  action: AuditAction;
  outcome: AuditOutcome;
  /** 操作者用户名（单管理员场景固定，登录失败时可为提交的用户名） */
  actor?: string;
  /** 目标实例 id */
  instanceId?: string;
  /** 目标实例名（冗余，便于阅读） */
  instanceName?: string;
  /** 人类可读摘要 */
  detail?: string;
  /** 结构化附加信息（命令、文件名、分区、来源 IP 等） */
  meta?: Record<string, unknown>;
  /** 客户端 IP */
  ip?: string;
}

/** 审计查询过滤 */
export interface AuditQuery {
  /** 开始日期（YYYY-MM-DD，含） */
  from?: string;
  /** 结束日期（YYYY-MM-DD，含） */
  to?: string;
  action?: AuditAction | string;
  instanceId?: string;
  outcome?: AuditOutcome;
  /** 文本模糊匹配 detail/actor */
  q?: string;
  limit?: number;
}

/** 审计查询结果（倒序，最新在前） */
export interface AuditQueryResult {
  records: AuditRecord[];
  total: number;
  /** 命中的日期文件列表 */
  days: string[];
}

/** 审计写入入参（服务内部使用；id/ts 由服务生成） */
export interface AuditInput {
  action: AuditAction;
  outcome?: AuditOutcome;
  actor?: string;
  instanceId?: string;
  instanceName?: string;
  detail?: string;
  meta?: Record<string, unknown>;
  ip?: string;
}
