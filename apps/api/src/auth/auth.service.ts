import { Inject, Injectable, Logger, UnauthorizedException } from '@nestjs/common';
import { JwtService } from '@nestjs/jwt';
import { compare, hashSync, genSaltSync, hash } from 'bcryptjs';
import { mkdir, readFile, rename, writeFile } from 'node:fs/promises';
import { join } from 'node:path';
import type { AuthUser, LoginRequest } from '@sc-panel/shared';
import type { PanelEnv } from '../config/env';
import { PANEL_ENV } from '../config/env.module';
import { toAbsolute } from '../common/utils/paths';

@Injectable()
export class AuthService {
  private readonly logger = new Logger('Auth');
  private readonly username: string;
  private passwordHash: string;
  private readonly jwt: JwtService;
  private readonly hashFile: string;

  constructor(@Inject(PANEL_ENV) env: PanelEnv, jwt: JwtService) {
    this.jwt = jwt;
    this.username = env.PANEL_USER;
    this.hashFile = join(toAbsolute(env.PANEL_DATA_DIR), 'admin-password.json');

    if (env.PANEL_PASSWORD_HASH) {
      this.passwordHash = env.PANEL_PASSWORD_HASH;
    } else if (env.PANEL_PASSWORD) {
      this.passwordHash = hashSync(env.PANEL_PASSWORD, genSaltSync(10));
      this.logger.log(`管理员密码来自 PANEL_PASSWORD（用户 ${this.username}）`);
    } else {
      const generated = genSaltSync(8);
      this.passwordHash = hashSync(generated, genSaltSync(10));
      // 打印一次，供首次登录（plants.md §5.1）
      this.logger.warn(
        `未配置 PANEL_PASSWORD/PANEL_PASSWORD_HASH，已生成随机密码（仅打印一次）: ${generated}`,
      );
    }

    // 运行期改密优先（优先于 .env，但不覆盖显式 PANEL_PASSWORD_HASH）
    void this.loadOverrideHash();
  }

  /** 运行期修改后的密码哈希持久化于数据目录，优先级高于 .env 明文密码 */
  private async loadOverrideHash(): Promise<void> {
    try {
      const raw = await readFile(this.hashFile, 'utf8');
      const parsed = JSON.parse(raw) as { username?: string; hash?: string };
      if (parsed.hash && (!parsed.username || parsed.username === this.username)) {
        this.passwordHash = parsed.hash;
        this.logger.log('已加载运行期修改的管理员密码');
      }
    } catch {
      // 无改密记录，使用 .env 基线
    }
  }

  async login(input: LoginRequest): Promise<{ user: AuthUser }> {
    if (input.username !== this.username) {
      throw new UnauthorizedException('用户名或密码错误');
    }
    const ok = await compare(input.password, this.passwordHash);
    if (!ok) {
      throw new UnauthorizedException('用户名或密码错误');
    }
    const user: AuthUser = { username: this.username, role: 'admin' };
    return { user };
  }

  /** 为已认证用户签发 JWT */
  async sign(user: AuthUser): Promise<string> {
    return this.jwt.signAsync({ sub: user.username, role: user.role });
  }

  async verify(token: string | undefined): Promise<AuthUser> {
    if (!token) {
      throw new UnauthorizedException('未登录');
    }
    try {
      const payload = await this.jwt.verifyAsync<{ sub: string; role: 'admin' }>(token);
      return { username: payload.sub, role: payload.role };
    } catch {
      throw new UnauthorizedException('登录已失效');
    }
  }

  /** 修改管理员密码（校验当前密码），持久化到数据目录 */
  async changePassword(username: string, current: string, next: string): Promise<void> {
    if (username !== this.username) {
      throw new UnauthorizedException('用户不存在');
    }
    const ok = await compare(current, this.passwordHash);
    if (!ok) {
      throw new UnauthorizedException('当前密码不正确');
    }
    this.passwordHash = await hash(next, genSaltSync(10));
    await mkdir(join(this.hashFile, '..'), { recursive: true });
    const tmp = `${this.hashFile}.tmp`;
    await writeFile(
      tmp,
      JSON.stringify({ username: this.username, hash: this.passwordHash }, null, 2),
      'utf8',
    );
    await rename(tmp, this.hashFile);
    this.logger.log('管理员密码已更新');
  }
}
