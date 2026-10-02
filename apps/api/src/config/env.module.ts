import { Global, Module } from '@nestjs/common';
import { ConfigModule } from '@nestjs/config';
import { parseEnv, type PanelEnv } from './env';

/** 注入令牌：全局可用的解析后环境 */
export const PANEL_ENV = 'PANEL_ENV';

/**
 * 提供强类型 `PanelEnv`。`@nestjs/config` 会把 `.env` 载入 `process.env`，
 * 这里统一用 zod 解析一次并作为 provider 注入，避免各处 `as` 断言。
 */
@Global()
@Module({
  imports: [
    ConfigModule.forRoot({
      isGlobal: true,
      envFilePath: ['.env.local', '.env', '../../.env'],
      validate: (raw) => parseEnv(raw),
    }),
  ],
  providers: [
    {
      provide: PANEL_ENV,
      useFactory: (): PanelEnv => parseEnv(process.env as Record<string, unknown>),
    },
  ],
  exports: [PANEL_ENV],
})
export class EnvModule {}
