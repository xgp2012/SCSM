import { Module } from '@nestjs/common';
import { APP_GUARD } from '@nestjs/core';
import { ThrottlerGuard, ThrottlerModule } from '@nestjs/throttler';
import { EnvModule } from './config/env.module';
import { ServerConfigModule } from './server-config/server-config.module';
import { AuditModule } from './audit/audit.module';
import { SettingsModule } from './settings/settings.module';
import { AuthModule } from './auth/auth.module';
import { JwtAuthGuard } from './auth/jwt-auth.guard';
import { InstanceModule } from './instance/instance.module';
import { ConsoleModule } from './console/console.module';
import { ProbeModule } from './probe/probe.module';
import { BackupModule } from './backup/backup.module';
import { VersionModule } from './version/version.module';
import { HealthModule } from './health/health.module';

@Module({
  imports: [
    EnvModule,
    // 全局限流基线：默认每 IP 每分钟 300 次；登录/下载/命令等敏感路由用 @Throttle 覆盖收紧（M7）
    ThrottlerModule.forRoot([{ name: 'default', ttl: 60_000, limit: 300 }]),
    ServerConfigModule,
    AuditModule,
    SettingsModule,
    AuthModule,
    BackupModule,
    VersionModule,
    InstanceModule,
    ConsoleModule,
    ProbeModule,
    HealthModule,
  ],
  providers: [
    // 限流守卫先于鉴权守卫（避免未鉴权请求绕过限流）
    { provide: APP_GUARD, useClass: ThrottlerGuard },
    { provide: APP_GUARD, useClass: JwtAuthGuard },
  ],
})
export class AppModule {}
