import { Logger } from '@nestjs/common';
import type { VersionRawRelease, VersionSource } from './version-source';

/**
 * Gitee Releases 来源（plants.md §0.4 / §5.8）。
 *
 * - 列表：`GET /api/v5/repos/{owner}/{repo}/releases`
 * - 下载直链：Gitee 的 `browser_download_url` 会 302 跳转到带临时 token 的
 *   `foruda.gitee.com` 链接；下载时由 VersionService 跟随重定向并带 Referer/UA。
 * - 镜像：配置 `GITEE_MIRROR` 时可把 host 从 `gitee.com` 替换为该镜像域，
 *   便于接入第三方下载源（保留扩展空间）。
 */
export class GiteeVersionSource implements VersionSource {
  readonly id = 'gitee';
  readonly label = 'Gitee Releases';

  private readonly logger = new Logger('VersionSource');

  constructor(
    private readonly repo: string,
    private readonly mirror = '',
    private readonly fetchImpl: typeof fetch = fetch,
  ) {}

  private apiBase(): string {
    return 'https://gitee.com/api/v5';
  }

  headers(): Record<string, string> {
    return {
      'User-Agent':
        'Mozilla/5.0 (Windows NT 10.0; Win64; x64) SC-Panel/1.0 (+https://gitee.com/SC-SPM/SurvivalcraftNet)',
      Referer: `https://gitee.com/${this.repo}/releases`,
      Accept: 'application/json, application/octet-stream, */*',
    };
  }

  async listReleases(): Promise<VersionRawRelease[]> {
    const url = `${this.apiBase()}/repos/${this.repo}/releases?page=1&per_page=50`;
    const res = await this.fetchImpl(url, { headers: this.headers() });
    if (!res.ok) {
      throw new Error(`Gitee releases 接口返回 ${res.status} ${res.statusText}`);
    }
    const data = (await res.json()) as Array<{
      tag_name: string;
      name?: string;
      body?: string;
      created_at?: string;
      published_at?: string;
      prerelease?: boolean;
      assets?: Array<{ name: string; browser_download_url: string; size?: number }>;
    }>;
    return data.map((r) => ({
      tag: r.tag_name,
      name: r.name || r.tag_name,
      body: r.body,
      publishedAt: r.published_at || r.created_at,
      prerelease: r.prerelease,
      assets: (r.assets ?? []).map((a) => ({
        name: a.name,
        url: this.applyMirror(a.browser_download_url),
        size: a.size,
      })),
    }));
  }

  /** 配了镜像时把下载 host 替换为镜像域（保留路径与文件名） */
  private applyMirror(url: string): string {
    if (!this.mirror) return url;
    try {
      const u = new URL(url);
      const m = this.mirror.replace(/^https?:\/\//, '').replace(/\/$/, '');
      return `${u.protocol}//${m}${u.pathname}${u.search}`;
    } catch (err) {
      this.logger.warn(`镜像地址解析失败，回退原始直链: ${String(err)}`);
      return url;
    }
  }
}
