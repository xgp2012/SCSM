import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import { mkdtemp, rm, writeFile, mkdir } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { parseEnv } from '../src/config/env';
import { VersionService } from '../src/version/version.service';
import { GiteeVersionSource } from '../src/version/gitee-source';
import { SettingsService } from '../src/settings/settings.service';

/** 构造一个返回固定 releases 的假 fetch */
function fakeFetch(handler: () => Response | Promise<Response>): typeof fetch {
  return (async () => handler()) as unknown as typeof fetch;
}

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'content-type': 'application/json' },
  });
}

const SAMPLE = [
  {
    tag_name: 'x26.06.19',
    name: 'x26.06.19',
    created_at: '2026-06-19T12:48:25+08:00',
    assets: [
      {
        name: '[服务端]SCNETx26.06.19z1.zip',
        browser_download_url: 'https://gitee.com/SC-SPM/SurvivalcraftNet/releases/download/x26.06.19/a.zip',
        size: 23114052,
      },
      {
        name: '[电脑版][Windows]SCNET.zip',
        browser_download_url: 'https://gitee.com/dl/client.zip',
        size: 1,
      },
    ],
  },
];

describe('VersionService (M6)', () => {
  let dataDir: string;

  beforeEach(async () => {
    dataDir = await mkdtemp(join(tmpdir(), 'sc-version-'));
  });

  afterEach(async () => {
    await rm(dataDir, { recursive: true, force: true });
  });

  function service(fetchImpl: typeof fetch) {
    const env = parseEnv({ PANEL_DATA_DIR: dataDir, VERSION_CACHE_MS: '0' });
    const svc = new VersionService(env, new SettingsService(env));
    // 用假 Gitee 源替换默认源
    (svc as unknown as { sources: Map<string, unknown> }).sources.set(
      'gitee',
      new GiteeVersionSource('SC-SPM/SurvivalcraftNet', '', fetchImpl),
    );
    return svc;
  }

  it('lists releases, marks server packages, recommends host platform asset', async () => {
    const svc = service(fakeFetch(() => jsonResponse(SAMPLE)));
    const res = await svc.listReleases();
    expect(res.source).toBe('gitee');
    expect(res.fallback).toBe(false);
    expect(res.releases).toHaveLength(1);
    const rel = res.releases[0];
    expect(rel.assets[0].isServerPackage).toBe(true);
    expect(rel.recommendedAsset).toBe('[服务端]SCNETx26.06.19z1.zip');
    expect(res.hostPlatform).toBe(process.platform === 'win32' ? 'windows' : 'linux');
  });

  it('falls back to persisted cache when the source fails', async () => {
    const good = service(fakeFetch(() => jsonResponse(SAMPLE)));
    await good.listReleases(); // 写入磁盘缓存

    const bad = service(
      fakeFetch(() => Promise.reject(new Error('network down'))),
    );
    const res = await bad.listReleases();
    expect(res.fallback).toBe(true);
    expect(res.notice).toContain('本地缓存');
    expect(res.releases).toHaveLength(1);
  });

  it('degrades with readable notice when source fails and no cache exists', async () => {
    const svc = service(fakeFetch(() => jsonResponse({}, 500)));
    const res = await svc.listReleases();
    expect(res.fallback).toBe(true);
    expect(res.releases).toHaveLength(0);
    expect(res.notice).toContain('手动上传');
  });

  it('resolves a specific asset url and errors clearly on missing tag/asset', async () => {
    const svc = service(fakeFetch(() => jsonResponse(SAMPLE)));
    const hit = await svc.resolveAsset(undefined, 'x26.06.19', '[服务端]SCNETx26.06.19z1.zip');
    expect(hit.assetUrl).toContain('releases/download');
    await expect(svc.resolveAsset(undefined, 'nope', 'x')).rejects.toThrow('未找到版本');
    await expect(
      svc.resolveAsset(undefined, 'x26.06.19', 'missing.zip'),
    ).rejects.toThrow('不包含资源');
  });

  it('applies mirror host substitution when configured', async () => {
    const env = parseEnv({ PANEL_DATA_DIR: dataDir, VERSION_CACHE_MS: '0' });
    const src = new GiteeVersionSource(
      'SC-SPM/SurvivalcraftNet',
      'https://mirror.example.com',
      fakeFetch(() => jsonResponse(SAMPLE)),
    );
    const svc2 = new VersionService(env, new SettingsService(env));
    (svc2 as unknown as { sources: Map<string, unknown> }).sources.set('gitee', src);
    const res = await svc2.listReleases();
    expect(res.releases[0].assets[0].url.startsWith('https://mirror.example.com/')).toBe(true);
  });

  it('handles empty release list without error', async () => {
    const svc = service(fakeFetch(() => jsonResponse([])));
    const res = await svc.listReleases();
    expect(res.releases).toHaveLength(0);
    expect(res.fallback).toBe(false);
  });

  it('reads pre-seeded cache file (no network) ', async () => {
    await mkdir(join(dataDir, 'versions'), { recursive: true });
    await writeFile(
      join(dataDir, 'versions', 'releases-cache-gitee.json'),
      JSON.stringify({
        fetchedAt: 123,
        releases: [
          {
            tag: 'x1',
            name: 'x1',
            assets: [
              {
                name: '[服务端]x.zip',
                url: 'u',
                isServerPackage: true,
                platform: 'windows',
                isSourceArchive: false,
              },
            ],
          },
        ],
      }),
    );
    const svc = service(fakeFetch(() => Promise.reject(new Error('offline'))));
    const res = await svc.listReleases();
    expect(res.fallback).toBe(true);
    expect(res.releases[0].tag).toBe('x1');
  });
});
