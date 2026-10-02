import {
  CanActivate,
  type ExecutionContext,
  Injectable,
} from '@nestjs/common';
import { Reflector } from '@nestjs/core';
import type { Request } from 'express';
import { AUTH_COOKIE_NAME, type AuthUser } from '@sc-panel/shared';
import { AuthService } from './auth.service';
import { IS_PUBLIC_KEY } from './public.decorator';

/** 全局 JWT 守卫；除标注 @Public() 的路由外一律要求登录 */
@Injectable()
export class JwtAuthGuard implements CanActivate {
  constructor(
    private readonly auth: AuthService,
    private readonly reflector: Reflector,
  ) {}

  async canActivate(context: ExecutionContext): Promise<boolean> {
    const isPublic = this.reflector.getAllAndOverride<boolean>(IS_PUBLIC_KEY, [
      context.getHandler(),
      context.getClass(),
    ]);
    if (isPublic) {
      return true;
    }

    const request = context.switchToHttp().getRequest<Request & { user?: AuthUser }>();
    const fromCookie = request.cookies?.[AUTH_COOKIE_NAME] as string | undefined;
    const header = request.headers.authorization;
    const fromHeader = header?.startsWith('Bearer ') ? header.slice(7) : undefined;

    const user = await this.auth.verify(fromCookie ?? fromHeader);
    request.user = user;
    return true;
  }
}
