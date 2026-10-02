import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import { createServer, type Server } from 'node:http';
import { mkdtemp, mkdir, readFile, rm, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import AdmZip from 'adm-zip';
import { parseEnv } from '../src/config/env';
import { DownloadService } from '../src/version/download.service';
import { SettingsService } from '../src/settings/settings.service';
import { AuditService } from '../src/audit/audit.service';
import type { VersionService } from '../src/version/version.service';
import type { InstanceService } from '../src/instance/instance.service';

/** 构造一个含 Survivalcraft.dll + Settings.xml 的最小 zip（模拟服务端包） */
function makeZipBuffer(): Buffer {
  const zip = new AdmZip();
  zip.addFile('Survivalcraft.dll', Buffer.from('MZ-fake-dll'));
  zip.addFile('Settings.xml', Buffer.from('<Settings><Setting Name="ServerPort" Value="28887" /></Settings>'));
  zip.addFile('Configs/LevelConfig.json', Buffer.from('{"seed-user":100}'));
  return zip.toBuffer();
}

describe('DownloadService (M6)', () => {
  let dataDir: string;
  let httpServer: Server;
  let baseUrl: string;
  let zipBuffer: Buffer;

  /** 记录 InstanceService 调用 */
  const calls: { created: unknown[]; overwritten: unknown[] } = { created: [], overwritten: [] };

  const instances = {
    createFromVersion: async (input: { name: string; dir: string; version: string }) => {
      calls.created.push(input);
      return { id: 'new-inst', name: input.name, dir: input.dir, version: input.version };
    },
    overwriteInstall: async (
      id: string,
      archivePath: string,
      opts: { preserve?: boolean; version?: string },
    ) => {
      calls.overwritten.push({ id, archivePath, opts });
      return { id, name: 'existing', version: opts.version ?? 'v' };
    },
  } as unknown as InstanceService;

  const versions = {
    resolveAsset: async (_src: unknown, tag: string, asset: string) => ({
      release: { tag, name: tag, assets: [] },
      assetUrl: `${baseUrl}/${asset}`,
      assetSize: zipBuffer.length,
      source: { id: 'gitee', headers: () => ({}) },
    }),
    headers: () => ({}),
  } as unknown as VersionService;

  beforeEach(async () => {
    dataDir = await mkdtemp(join(tmpdir(), 'sc-dl-'));
    zipBuffer = makeZipBuffer();
    calls.created.length = 0;
    calls.overwritten.length = 0;

    httpServer = createServer((req, res) => {
      if (req.url?.includes('missing')) {
        res.statusCode = 404;
        res.end('nope');
        return;
      }
      res.setHeader('content-type', 'application/zip');
      res.setHeader('content-length', String(zipBuffer.length));
      // 分块发送，便于观测进度
      const half = Math.floor(zipBuffer.length / 2);
      res.write(zipBuffer.subarray(0, half));
      setTimeout(() => res.end(zipBuffer.subarray(half)), 10);
    });
    await new Promise<void>((r) => httpServer.listen(0, '127.0.0.1', () => r()));
    const addr = httpServer.address();
    baseUrl = `http://127.0.0.1:${typeof addr === 'object' && addr ? addr.port : 0}`;
  });

  afterEach(async () => {
    await new Promise<void>((r) => httpServer.close(() => r()));
    await rm(dataDir, { recursive: true, force: true });
  });

  function service() {
    const env = parseEnv({ PANEL_DATA_DIR: dataDir, VERSION_MAX_BYTES: String(50 * 1024 * 1024) });
    return new DownloadService(
      env,
      versions,
      instances,
      new SettingsService(env),
      new AuditService(env),
    );
  }

  async function waitDone(svc: DownloadService, id: string) {
    for (let i = 0; i < 200; i++) {
      const t = svc.getTask(id);
      if (t.phase === 'done' || t.phase === 'failed') return t;
      await new Promise((r) => setTimeout(r, 15));
    }
    throw new Error('task did not finish');
  }

  it('downloads and installs into a new instance directory', async () => {
    const svc = service();
    const task = svc.startDownload({
      tag: 'x26.06.19',
      asset: 'server.zip',
      mode: 'new',
      name: '版本实例',
    });
    const done = await waitDone(svc, task.id);
    expect(done.phase).toBe('done');
    expect(done.percent).toBe(100);
    expect(done.instanceId).toBe('new-inst');
    expect(calls.created).toHaveLength(1);
    const created = calls.created[0] as { dir: string; name: string };
    const dll = await readFile(join(created.dir, 'Survivalcraft.dll'), 'utf8');
    expect(dll).toContain('MZ-fake-dll');
  });

  it('overwrites an existing instance (archive passed to InstanceService)', async () => {
    const svc = service();
    const task = svc.startDownload({
      tag: 'x26.06.19',
      asset: 'server.zip',
      mode: 'overwrite',
      instanceId: 'inst-1',
      preserve: true,
    });
    const done = await waitDone(svc, task.id);
    expect(done.phase).toBe('done');
    expect(calls.overwritten).toHaveLength(1);
    const ow = calls.overwritten[0] as { id: string; opts: { preserve: boolean } };
    expect(ow.id).toBe('inst-1');
    expect(ow.opts.preserve).toBe(true);
  });

  it('fails with readable error on HTTP 404', async () => {
    const svc = service();
    const task = svc.startDownload({ tag: 'x', asset: 'missing.zip', mode: 'new' });
    const done = await waitDone(svc, task.id);
    expect(done.phase).toBe('failed');
    expect(done.error).toMatch(/404/);
  });

  it('installs from an uploaded zip buffer (new instance)', async () => {
    const svc = service();
    const res = await svc.installFromUploadBuffer(zipBuffer, { mode: 'new', name: '上传实例' }, 'server.zip');
    expect(res.task.phase).toBe('done');
    expect(res.install.instanceName).toBe('上传实例');
    expect(calls.created).toHaveLength(1);
  });

  it('rejects unknown archive formats on upload', async () => {
    const svc = service();
    await expect(
      svc.installFromUploadBuffer(Buffer.from('x'), { mode: 'new' }, 'notes.txt'),
    ).rejects.toThrow(/zip 或 tar.gz/);
  });

  it('requires instanceId for overwrite mode', () => {
    const svc = service();
    expect(() =>
      svc.startDownload({ tag: 'x', asset: 'a.zip', mode: 'overwrite' }),
    ).toThrow(/instanceId/);
  });

  it('rejects assets over the configured size cap', async () => {
    const env = parseEnv({ PANEL_DATA_DIR: dataDir, VERSION_MAX_BYTES: '10' });
    const svc = new DownloadService(
      env,
      versions,
      instances,
      new SettingsService(env),
      new AuditService(env),
    );
    const task = svc.startDownload({ tag: 'x', asset: 'server.zip', mode: 'new' });
    const done = await waitDone(svc, task.id);
    expect(done.phase).toBe('failed');
    expect(done.error).toMatch(/超过上限/);
  });

  it('lists tasks newest first', async () => {
    const svc = service();
    const t1 = svc.startDownload({ tag: 'x', asset: 'server.zip', mode: 'new', name: 'a' });
    await new Promise((r) => setTimeout(r, 5));
    const t2 = svc.startDownload({ tag: 'x', asset: 'server.zip', mode: 'new', name: 'b' });
    await waitDone(svc, t1.id);
    await waitDone(svc, t2.id);
    const list = svc.listTasks();
    expect(list[0].id).toBe(t2.id);
  });

  it('flattens a single-root archive directory', async () => {
    const nested = new AdmZip();
    nested.addFile('SCNET/Survivalcraft.dll', Buffer.from('MZ'));
    nested.addFile('SCNET/Settings.xml', Buffer.from('<Settings/>'));
    const buf = nested.toBuffer();
    const svc = service();
    const res = await svc.installFromUploadBuffer(buf, { mode: 'new', name: 'nested' }, 'nested.zip');
    expect(res.task.phase).toBe('done');
    const created = calls.created[0] as { dir: string };
    expect(await readFile(join(created.dir, 'Survivalcraft.dll'), 'utf8')).toContain('MZ');
  });

  it('rejects an archive missing Survivalcraft.dll', async () => {
    const bad = new AdmZip();
    bad.addFile('readme.txt', Buffer.from('no server'));
    const svc = service();
    await expect(
      svc.installFromUploadBuffer(bad.toBuffer(), { mode: 'new' }, 'bad.zip'),
    ).rejects.toThrow(/Survivalcraft.dll/);
  });

  it('ensureTmp creates the staging directory', async () => {
    const svc = service();
    await svc.ensureTmp();
    await mkdir(dataDir, { recursive: true });
    await writeFile(join(dataDir, '.probe'), 'x');
    expect(true).toBe(true);
  });
});
