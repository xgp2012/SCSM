import { Global, Module } from '@nestjs/common';
import { EnvModule } from '../config/env.module';
import { AuditController } from './audit.controller';
import { AuditService } from './audit.service';

/**
 * 审计模块（全局）：任何模块可注入 `AuditService` 记录操作，
 * 无需在各业务模块重复 imports。
 */
@Global()
@Module({
  imports: [EnvModule],
  controllers: [AuditController],
  providers: [AuditService],
  exports: [AuditService],
})
export class AuditModule {}
