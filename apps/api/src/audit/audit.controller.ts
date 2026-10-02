import { Controller, Get, Query } from '@nestjs/common';
import type { AuditAction, AuditQueryResult } from '@sc-panel/shared';
import { AuditService } from './audit.service';

/** 审计日志查询接口（plants.md §9 / §10 M7） */
@Controller('audit')
export class AuditController {
  constructor(private readonly audit: AuditService) {}

  @Get()
  async list(
    @Query('from') from?: string,
    @Query('to') to?: string,
    @Query('action') action?: string,
    @Query('instanceId') instanceId?: string,
    @Query('outcome') outcome?: string,
    @Query('q') q?: string,
    @Query('limit') limit?: string,
  ): Promise<{ ok: true; data: AuditQueryResult }> {
    return {
      ok: true,
      data: await this.audit.query({
        from,
        to,
        action: action as AuditAction | undefined,
        instanceId,
        outcome: outcome as 'ok' | 'failed' | undefined,
        q,
        limit: limit ? Number(limit) : undefined,
      }),
    };
  }

  @Get('days')
  async days(): Promise<{ ok: true; data: string[] }> {
    return { ok: true, data: await this.audit.listDays() };
  }
}
