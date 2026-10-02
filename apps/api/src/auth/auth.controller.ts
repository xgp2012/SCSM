import { Body, Controller, Get, Inject, Post, Req, Res } from '@nestjs/common';
import { Throttle } from '@nestjs/throttler';
import type { Request, Response } from 'express';
import { AUTH_COOKIE_NAME, type AuthUser } from '@sc-panel/shared';
import type { PanelEnv } from '../config/env';
import { PANEL_ENV } from '../config/env.module';
import { AuditService } from '../audit/audit.service';
import { AuthService } from './auth.service';
import { Public } from './public.decorator';
import { CurrentUser } from './current-user.decorator';
import { LoginDto } from './dto/login.dto';

@Controller('auth')
export class AuthController {
  constructor(
    private readonly auth: AuthService,
    @Inject(PANEL_ENV) private readonly env: PanelEnv,
    private readonly audit: AuditService,
  ) {}

  /** 登录：严格限流（每 IP 每分 5 次）防暴力破解（plants.md §9） */
  @Public()
  @Throttle({ default: { limit: 5, ttl: 60_000 } })
  @Post('login')
  async login(
    @Body() dto: LoginDto,
    @Res({ passthrough: true }) res: Response,
    @Req() req: Request,
  ) {
    let user: AuthUser;
    try {
      ({ user } = await this.auth.login({
        username: dto.username,
        password: dto.password,
      }));
    } catch (err) {
      void this.audit.log({
        action: 'auth.login.failed',
        outcome: 'failed',
        actor: dto.username,
        detail: '登录失败',
        ip: clientIp(req),
      });
      throw err;
    }
    const token = await this.auth.sign(user);
    res.cookie(AUTH_COOKIE_NAME, token, {
      httpOnly: true,
      sameSite: 'lax',
      secure: this.env.NODE_ENV === 'production',
      maxAge: 12 * 60 * 60 * 1000,
      path: '/',
    });
    void this.audit.log({
      action: 'auth.login',
      actor: user.username,
      detail: '登录成功',
      ip: clientIp(req),
    });
    return { ok: true as const, data: { user } };
  }

  @Post('logout')
  logout(@Res({ passthrough: true }) res: Response, @Req() req: Request) {
    res.clearCookie(AUTH_COOKIE_NAME, { path: '/' });
    void this.audit.log({ action: 'auth.logout', actor: this.env.PANEL_USER, ip: clientIp(req) });
    return { ok: true as const, data: { user: null } };
  }

  @Get('me')
  me(@CurrentUser() user: AuthUser) {
    return { ok: true as const, data: { user } };
  }
}

function clientIp(req: Request): string | undefined {
  const fwd = req.headers['x-forwarded-for'];
  if (typeof fwd === 'string' && fwd) return fwd.split(',')[0].trim();
  return req.ip ?? req.socket?.remoteAddress ?? undefined;
}
