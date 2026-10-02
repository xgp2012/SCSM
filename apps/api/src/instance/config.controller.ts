import { BadRequestException, Body, Controller, Get, Param, Put, Req } from '@nestjs/common';
import type { Request } from 'express';
import type { AuthUser, ConfigBundle, ConfigSection } from '@sc-panel/shared';
import { CurrentUser } from '../auth/current-user.decorator';
import { AuditService } from '../audit/audit.service';
import { InstanceService } from './instance.service';
import { CONFIG_SECTION_KEYS } from '../server-config/server-config.service';

@Controller('instances')
export class ConfigController {
  constructor(
    private readonly service: InstanceService,
    private readonly audit: AuditService,
  ) {}

  /** 读取实例完整配置快照（敏感字段脱敏） */
  @Get(':id/config')
  async getConfig(@Param('id') id: string): Promise<{ ok: true; data: ConfigBundle }> {
    return { ok: true, data: await this.service.readConfig(id) };
  }

  /** 写入单个配置分区（保留未知字段，原子写） */
  @Put(':id/config/:section')
  async putConfig(
    @Param('id') id: string,
    @Param('section') section: string,
    @Body() body: unknown,
    @CurrentUser() user: AuthUser,
    @Req() req: Request,
  ): Promise<{ ok: true; data: ConfigBundle }> {
    if (!CONFIG_SECTION_KEYS.includes(section as ConfigSection)) {
      throw new BadRequestException(`不支持的分区: ${section}`);
    }
    await this.service.writeSection(id, section as ConfigSection, body);
    void this.audit.log({
      action: 'instance.config.update',
      actor: user.username,
      instanceId: id,
      detail: `写入配置分区 ${section}`,
      meta: { section },
      ip: clientIp(req),
    });
    return { ok: true, data: await this.service.readConfig(id) };
  }
}

function clientIp(req: Request): string | undefined {
  const fwd = req.headers['x-forwarded-for'];
  if (typeof fwd === 'string' && fwd) return fwd.split(',')[0].trim();
  return req.ip ?? req.socket?.remoteAddress ?? undefined;
}
