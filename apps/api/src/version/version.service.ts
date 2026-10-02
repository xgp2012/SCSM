import { Inject, Injectable, Logger } from '@nestjs/common';
import {
  access,
  constants,
  mkdir,
  readFile,
  rm,
  stat,
  writeFile,
} from 'node:fs/promises';
import { join } from 'node:path';
import type {
  ReleaseInfo,
  ReleaseListResponse,
  ServerPlatform,
  VersionSourceId,
} from '@sc-panel/shared';
import type { PanelEnv } from '../config/env';
import { PANEL_ENV } from '../config/env.module';
import { toAbsolute } from '../common/utils/paths';
import { GiteeVersionSource } from './gitee-source';
import { SettingsService } from '../settings/settings.service';
import { serverPackageScore, toReleaseInfo, type VersionSource } from './version-source';

interface CacheEntry {
  releases: ReleaseInfo[];
  fetchedAt: number;
  fallback: boolean;
  notice?: string;
}

/**
 * 版本来源与 release 列表服务（plants.md §5.8）。
 *
 * - 默认来源 Gitee；`VERSION_SOURCE`/`GITEE_MIRROR` 预留第三方源扩展；
 * - 列表结果按 `VERSION_CACHE_MS` 缓存；接口失败时**降级到本地缓存文件**
 *   （`data/versions/releases-cache.json`），保证界面仍可展示并有可读提示；
 * - 镜像/缓存时长支持**运行期热更新**（来自 `SettingsService`，M7）。
 */
@Injectable()
export class VersionService {
  private readonly logger = new Logger('Version');
  private readonly sources = new Map<VersionSourceId, VersionSource>();
  private readonly cache = new Map<VersionSourceId, CacheEntry>();
  private readonly cacheDir: string;

  constructor(
    @Inject(PANEL_ENV) private readonly env: PanelEnv,
    private readonly settings: SettingsService,
  ) {
    this.cacheDir = join(toAbsolute(env.PANEL_DATA_DIR), 'versions');
    this.sources.set('gitee', this.buildGitee());
    // 镜像等设置热更新时重建来源并清缓存
    this.settings.onChange(() => {
      this.sources.set('gitee', this.buildGitee());
      void this.clearCache('gitee');
    });
    // 预留：第三方镜像/自建源可在此按 VERSION_SOURCE 注册到 this.sources
  }

  private buildGitee(): VersionSource {
    return new GiteeVersionSource(this.env.GITEE_REPO, this.settings.download.giteeMirror);
  }

  hostPlatform(): ServerPlatform {
    const p = process.platform;
    if (p === 'win32') return 'windows';
    if (p === 'linux') return 'linux';
    return 'unknown';
  }

  sourceInfo(sourceId?: VersionSourceId): {
    id: VersionSourceId;
    label: string;
    repo: string;
    isDefault: boolean;
    available: boolean;
  } {
    const id = this.resolveSourceId(sourceId);
    const source = this.sources.get(id);
    return {
      id,
      label: source?.label ?? id,
      repo: this.env.GITEE_REPO,
      isDefault: id === this.resolveSourceId(),
      available: this.sources.has(id),
    };
  }

  private resolveSourceId(sourceId?: VersionSourceId): VersionSourceId {
    const requested = sourceId ?? this.env.VERSION_SOURCE;
    if (this.sources.has(requested)) return requested;
    return 'gitee';
  }

  /** 拉取 release 列表（带缓存 + 降级） */
  async listReleases(sourceId?: VersionSourceId, useCache = true): Promise<ReleaseListResponse> {
    const id = this.resolveSourceId(sourceId);
    const source = this.sources.get(id);
    if (!source) {
      return this.degraded(id, `版本来源不可用: ${id}`);
    }

    const cache = this.cache.get(id);
    const cacheMs = this.settings.download.cacheMs;
    const fresh =
      useCache &&
      cache &&
      cacheMs > 0 &&
      Date.now() - cache.fetchedAt < cacheMs;
    if (fresh && cache) {
      return this.toResponse(id, cache.releases, cache.fetchedAt, {
        fallback: cache.fallback,
        notice: cache.notice,
        cached: true,
      });
    }

    try {
      const raw = await source.listReleases();
      const releases = raw
        .map(toReleaseInfo)
        .map((r) => this.withRecommendation(r));
      const fetchedAt = Date.now();
      this.cache.set(id, { releases, fetchedAt, fallback: false });
      await this.persistCache(id, releases, fetchedAt);
      this.logger.log(`[${id}] 拉取 release ${releases.length} 个`);
      return this.toResponse(id, releases, fetchedAt, { fallback: false, cached: false });
    } catch (err) {
      const msg = `拉取 release 列表失败：${this.errText(err)}`;
      this.logger.warn(`[${id}] ${msg}，尝试本地降级`);
      const local = cache?.releases ?? (await this.loadPersistedCache(id));
      if (local && local.length > 0) {
        const fetchedAt = cache?.fetchedAt ?? (await this.persistedFetchedAt(id)) ?? Date.now();
        return this.toResponse(id, local, fetchedAt, {
          fallback: true,
          notice: `${msg}（展示本地缓存）`,
          cached: true,
        });
      }
      return this.degraded(id, `${msg}（无本地缓存，可改用手动上传）`);
    }
  }

  /** 在 source 的 release 列表里查找指定 tag+asset 并返回下载直链 */
  async resolveAsset(
    sourceId: VersionSourceId | undefined,
    tag: string,
    asset: string,
  ): Promise<{ release: ReleaseInfo; assetUrl: string; assetSize?: number; source: VersionSource }> {
    const id = this.resolveSourceId(sourceId);
    const source = this.sources.get(id);
    if (!source) throw new Error(`版本来源不可用: ${id}`);

    const { releases } = await this.listReleases(id, false);
    const release = releases.find((r) => r.tag === tag);
    if (!release) throw new Error(`未找到版本: ${tag}`);
    const found = release.assets.find((a) => a.name === asset);
    if (!found) throw new Error(`版本 ${tag} 不包含资源: ${asset}`);
    return { release, assetUrl: found.url, assetSize: found.size, source };
  }

  headers(sourceId?: VersionSourceId): Record<string, string> {
    const source = this.sources.get(this.resolveSourceId(sourceId));
    return source?.headers() ?? {};
  }

  /** 预选面板主机平台对应的服务端包（优先托管式 Survivalcraft 包） */
  private withRecommendation(release: ReleaseInfo): ReleaseInfo {
    const host = this.hostPlatform();
    const serverAssets = release.assets
      .filter((a) => a.isServerPackage)
      .sort((a, b) => serverPackageScore(b.name) - serverPackageScore(a.name));
    const preferred =
      serverAssets.find((a) => a.platform === host) ??
      serverAssets.find((a) => a.platform === 'unknown') ??
      serverAssets[0];
    return { ...release, recommendedAsset: preferred?.name };
  }

  private toResponse(
    source: VersionSourceId,
    releases: ReleaseInfo[],
    fetchedAt: number,
    opts: { fallback: boolean; notice?: string; cached: boolean },
  ): ReleaseListResponse {
    return {
      source,
      fallback: opts.fallback,
      notice: opts.notice,
      releases,
      hostPlatform: this.hostPlatform(),
      fetchedAt,
      cached: opts.cached,
    };
  }

  private degraded(source: VersionSourceId, notice: string): ReleaseListResponse {
    return {
      source,
      fallback: true,
      notice,
      releases: [],
      hostPlatform: this.hostPlatform(),
      fetchedAt: Date.now(),
      cached: false,
    };
  }

  // ── 持久缓存降级 ──────────────────────────────────────

  private cacheFile(id: VersionSourceId): string {
    return join(this.cacheDir, `releases-cache-${id}.json`);
  }

  private async persistCache(
    id: VersionSourceId,
    releases: ReleaseInfo[],
    fetchedAt: number,
  ): Promise<void> {
    try {
      await mkdir(this.cacheDir, { recursive: true });
      await writeFile(
        this.cacheFile(id),
        JSON.stringify({ fetchedAt, releases }, null, 2),
        'utf8',
      );
    } catch (err) {
      this.logger.warn(`[${id}] 写入 release 缓存失败: ${String(err)}`);
    }
  }

  private async loadPersistedCache(id: VersionSourceId): Promise<ReleaseInfo[] | undefined> {
    const file = this.cacheFile(id);
    if (!(await this.pathExists(file))) return undefined;
    try {
      const parsed = JSON.parse(await readFile(file, 'utf8')) as {
        releases?: ReleaseInfo[];
      };
      if (!parsed.releases || parsed.releases.length === 0) return undefined;
      this.cache.set(id, { releases: parsed.releases, fetchedAt: Date.now(), fallback: true });
      return parsed.releases;
    } catch {
      return undefined;
    }
  }

  private async persistedFetchedAt(id: VersionSourceId): Promise<number | undefined> {
    const file = this.cacheFile(id);
    if (!(await this.pathExists(file))) return undefined;
    try {
      const parsed = JSON.parse(await readFile(file, 'utf8')) as { fetchedAt?: number };
      return parsed.fetchedAt;
    } catch {
      return undefined;
    }
  }

  /** 清空内存+磁盘缓存（调试/强制刷新） */
  async clearCache(id?: VersionSourceId): Promise<void> {
    if (id) {
      this.cache.delete(id);
      await rm(this.cacheFile(id), { force: true });
      return;
    }
    for (const key of this.cache.keys()) this.cache.delete(key);
    await rm(this.cacheDir, { recursive: true, force: true });
  }

  private errText(err: unknown): string {
    return err instanceof Error ? err.message : String(err);
  }

  private async pathExists(path: string): Promise<boolean> {
    try {
      await access(path, constants.F_OK);
      return true;
    } catch {
      return false;
    }
  }

  /** 暴露给下载阶段：暂存目录 */
  tmpDir(): string {
    return join(this.cacheDir, 'downloads');
  }

  /** 计算文件字节数（供进度/校验） */
  async fileSize(path: string): Promise<number> {
    const s = await stat(path);
    return s.size;
  }
}
