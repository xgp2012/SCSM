import { Global, Module } from '@nestjs/common';
import { EnvModule } from '../config/env.module';
import { AuthModule } from '../auth/auth.module';
import { SettingsController } from './settings.controller';
import { SettingsService } from './settings.service';

/** 面板设置模块（全局：其它服务可注入读取热更新后的策略） */
@Global()
@Module({
  imports: [EnvModule, AuthModule],
  controllers: [SettingsController],
  providers: [SettingsService],
  exports: [SettingsService],
})
export class SettingsModule {}
