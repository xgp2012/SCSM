import pino, { type Logger } from 'pino';
import type { PanelEnv } from '../../config/env';

/**
 * 面板主进程日志器（pino）。
 * 后续可按 plants.md §5.9 增加文件轮转。
 */
export function createLogger(env: PanelEnv): Logger {
  return pino({
    level: env.LOG_LEVEL,
    base: { app: 'sc-panel-api' },
    timestamp: pino.stdTimeFunctions.isoTime,
    transport: env.LOG_PRETTY
      ? {
          target: 'pino-pretty',
          options: { colorize: true, translateTime: 'SYS:HH:MM:ss.l' },
        }
      : undefined,
  });
}
