import { z } from 'zod';

/** 面板后端环境变量 schema（.env / process.env） */
export const envSchema = z.object({
  NODE_ENV: z
    .enum(['development', 'production', 'test'])
    .default('development'),
  PANEL_HOST: z.string().default('127.0.0.1'),
  PANEL_PORT: z.coerce.number().int().positive().default(3001),
  PANEL_USER: z.string().default('admin'),
  PANEL_PASSWORD: z.string().optional(),
  PANEL_PASSWORD_HASH: z.string().optional(),
  JWT_SECRET: z.string().default('dev-insecure-secret-change-me'),
  JWT_EXPIRES_IN: z.string().default('12h'),
  PANEL_CORS_ORIGIN: z.string().default('http://localhost:3000'),
  PANEL_DATA_DIR: z.string().default('./data'),
  /** 缺省实例模板目录（服务端本体），相对后端进程工作目录 */
  SERVER_TEMPLATE_DIR: z.string().default('../../../SurvivalcraftNet/服务端本体'),
  /** 优雅停止等待时间（ms），超时后 SIGTERM/SIGKILL */
  STOP_GRACE_MS: z.coerce.number().int().nonnegative().default(15000),
  LOG_LEVEL: z.enum(['fatal', 'error', 'warn', 'info', 'debug', 'trace']).default('info'),
  LOG_PRETTY: z
    .string()
    .default('true')
    .transform((v) => v.toLowerCase() !== 'false'),
  GITEE_REPO: z.string().default('SC-SPM/SurvivalcraftNet'),
  GITEE_MIRROR: z.string().default(''),
  /** 版本下载源（默认 gitee，保留第三方源扩展） */
  VERSION_SOURCE: z.string().default('gitee'),
  /** releases 列表缓存时长（ms），0 表示不缓存 */
  VERSION_CACHE_MS: z.coerce.number().int().nonnegative().default(300000),
  /** 单个版本包下载大小上限（字节），默认 2GB，防滥用 */
  VERSION_MAX_BYTES: z.coerce.number().int().positive().default(2 * 1024 * 1024 * 1024),
  /** UDP 探测间隔（ms），0 表示禁用周期探测（plants.md §5.7） */
  PROBE_INTERVAL_MS: z.coerce.number().int().nonnegative().default(5000),
  /** 指标采样间隔（ms），0 表示禁用周期采样 */
  METRICS_INTERVAL_MS: z.coerce.number().int().nonnegative().default(3000),
  /** 定时自动备份间隔（ms），0 表示禁用（plants.md §5.6） */
  BACKUP_INTERVAL_MS: z.coerce.number().int().nonnegative().default(0),
  /** 每个实例保留的最大备份数量，超出按时间从旧到新删除，0 不限制 */
  BACKUP_KEEP: z.coerce.number().int().nonnegative().default(10),
});

export type PanelEnv = z.infer<typeof envSchema>;

export function parseEnv(raw: Record<string, unknown>): PanelEnv {
  const result = envSchema.safeParse(raw);
  if (!result.success) {
    const issues = result.error.issues
      .map((i) => `  - ${i.path.join('.') || '(root)'}: ${i.message}`)
      .join('\n');
    throw new Error(`环境变量校验失败:\n${issues}`);
  }
  return result.data;
}
