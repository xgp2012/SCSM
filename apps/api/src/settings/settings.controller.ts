import { Body, Controller, Get, Post, Put, Req } from '@nestjs/common';
import type { Request } from 'express';
import type {
  AuthUser,
  ChangePasswordInput,
  PanelSettings,
  UpdatePanelSettingsInput,
} from '@sc-panel/shared';
import { AuthService } from '../auth/auth.service';
import { CurrentUser } from '../auth/current-user.decorator';
import { AuditService } from '../audit/audit.service';
import { SettingsService } from './settings.service';
import { ChangePasswordDto, UpdatePanelSettingsDto } from './dto/settings.dto';

/** 面板设置（plants.md §6 `/settings` · §10 M7） */
@Controller('settings')
export class SettingsController {
  constructor(
    private readonly settings: SettingsService,
    private readonly auth: AuthService,
    private readonly audit: AuditService,
  ) {}

  @Get()
  get(): { ok: true; data: PanelSettings } {
    return { ok: true, data: this.settings.get() };
  }

  @Put()
  async update(
    @Body() body: UpdatePanelSettingsDto,
    @CurrentUser() user: AuthUser,
    @Req() req: Request,
  ): Promise<{ ok: true; data: PanelSettings }> {
    const next = await this.settings.update(body as UpdatePanelSettingsInput);
    void this.audit.log({
      action: 'settings.update',
      actor: user.username,
      detail: '更新面板设置',
      meta: body as unknown as Record<string, unknown>,
      ip: clientIp(req),
    });
    return { ok: true, data: next };
  }

  /** 修改管理员密码（需当前密码） */
  @Post('password')
  async changePassword(
    @Body() body: ChangePasswordDto,
    @CurrentUser() user: AuthUser,
    @Req() req: Request,
  ): Promise<{ ok: true; data: { changed: true } }> {
    await this.auth.changePassword(
      user.username,
      (body as ChangePasswordInput).currentPassword,
      (body as ChangePasswordInput).newPassword,
    );
    void this.audit.log({
      action: 'auth.password.change',
      actor: user.username,
      detail: '修改管理员密码',
      ip: clientIp(req),
    });
    return { ok: true, data: { changed: true } };
  }
}

function clientIp(req: Request): string | undefined {
  const fwd = req.headers['x-forwarded-for'];
  if (typeof fwd === 'string' && fwd) return fwd.split(',')[0].trim();
  return req.ip ?? req.socket?.remoteAddress ?? undefined;
}
