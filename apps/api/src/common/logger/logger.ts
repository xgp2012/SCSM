import pino, { type Logger } from 'pino';
import type { PanelEnv } from '../../config/env';

/**
 * 面板主进程日志器（pino）。
 * 后续可按 plants.md §5.9 增加文件轮转。
 *
 * `pino-pretty` 仅在 devDependencies，生产安装（`pnpm deploy --prod` / pkg 单文件
 * 二进制）中不存在；而 pino 的 transport 目标由**运行时** worker_threads 解析，
 * pkg 无法静态收录。若强行启用，`pino()` 会抛
 * `unable to determine transport target for "pino-pretty"`——该异常发生在
 * `app.listen()` 之前，会让面板完全起不来。故先探测可用性，不可用则回退 JSON 日志，
 * 保证面板始终能启动（生产环境本就应输出结构化 JSON，便于采集）。
 */
function canUsePretty(): boolean {
  try {
    require.resolve('pino-pretty');
    return true;
  } catch {
    return false;
  }
}

export function createLogger(env: PanelEnv): Logger {
  const usePretty = env.LOG_PRETTY && canUsePretty();
  return pino({
    level: env.LOG_LEVEL,
    base: { app: 'sc-panel-api' },
    timestamp: pino.stdTimeFunctions.isoTime,
    transport: usePretty
      ? {
          target: 'pino-pretty',
          options: { colorize: true, translateTime: 'SYS:HH:MM:ss.l' },
        }
      : undefined,
  });
}
