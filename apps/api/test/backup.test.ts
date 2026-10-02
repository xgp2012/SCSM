import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import { mkdir, mkdtemp, readFile, rm, stat, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import type { InstanceMeta } from '@sc-panel/shared';
import { parseEnv } from '../src/config/env';
import { BackupService } from '../src/backup/backup.service';
import { createTarGz, extractTarGz } from '../src/backup/archive';
import { SettingsService } from '../src/settings/settings.service';
import type { InstanceStore } from '../src/instance/instance.store';
import type { InstanceSupervisor } from '../src/instance/instance.supervisor';

function makeMeta(over: Partial<InstanceMeta> = {}): InstanceMeta {
  return {
    id: 'inst-1',
    name: 'demo',
    source: 'template',
    dir: '',
    serverPort: 28887,
    version: 'x26.06.19',
    launchArgs: ['--enhanced'],
    worldPath: 'app:/Worlds/World',
    worldName: 'World',
    maxPlayers: 20,
    autoRestart: false,
    autoStart: false,
    autoRun: true,
    createdAt: new Date(0).toISOString(),
    ...over,
  };
}

describe('BackupService (M5)', () => {
  let dir: string;
  let meta: InstanceMeta;
  let service: BackupService;
  let alive = false;

  const store = {
    read: async (id: string) => (id === meta.id ? meta : null),
    list: async () => [meta],
  } as unknown as InstanceStore;
  const supervisor = {
    isAlive: () => alive,
    stop: async () => {
      alive = false;
    },
  } as unknown as InstanceSupervisor;

  beforeEach(async () => {
    dir = await mkdtemp(join(tmpdir(), 'sc-backup-'));
    meta = makeMeta({ dir });
    alive = false;
    await mkdir(join(dir, 'Worlds', 'World', 'Regions'), { recursive: true });
    await writeFile(join(dir, 'Worlds', 'World', 'Project.json'), '{"Name":["string","W"]}');
    await writeFile(join(dir, 'Worlds', 'World', 'Regions', 'r0.region'), 'REGION-BYTES');
    await writeFile(join(dir, 'Settings.xml'), '<Settings><Setting Name="ServerPort" Value="28887" /></Settings>');
    await writeFile(join(dir, 'ServerSetting.json'), '{"WorldPath":"app:/Worlds/World","Unknown":1}');
    await mkdir(join(dir, 'Configs'), { recursive: true });
    await writeFile(join(dir, 'Configs', 'LevelConfig.json'), '{"Alice":100}');

    const env = parseEnv({ BACKUP_KEEP: '10' });
    service = new BackupService(store, supervisor, new SettingsService(env));
    await service.onModuleInit();
  });

  afterEach(async () => {
    await rm(dir, { recursive: true, force: true });
  });

  it('lists world saves with size/files/current marker', async () => {
    const worlds = await service.listWorlds(meta.id);
    expect(worlds).toHaveLength(1);
    expect(worlds[0].name).toBe('World');
    expect(worlds[0].isCurrent).toBe(true);
    expect(worlds[0].hasProject).toBe(true);
    expect(worlds[0].files).toBeGreaterThanOrEqual(2);
    expect(worlds[0].size).toBeGreaterThan(0);
  });

  it('creates a backup archive containing world + config', async () => {
    const backup = await service.createBackup(meta.id, {}, 'manual');
    expect(backup.world).toBe('World');
    expect(backup.includes.config).toBe(true);
    expect(backup.size).toBeGreaterThan(0);

    const archivePath = join(dir, 'backups', backup.file);
    const s = await stat(archivePath);
    expect(s.size).toBe(backup.size);

    // 解压到临时目录校验内容
    const dest = join(dir, '__verify');
    await extractTarGz(archivePath, dest);
    const project = await readFile(join(dest, 'Worlds', 'World', 'Project.json'), 'utf8');
    expect(project).toContain('"W"');
    const level = await readFile(join(dest, 'Configs', 'LevelConfig.json'), 'utf8');
    expect(JSON.parse(level).Alice).toBe(100);
    expect(await readFile(join(dest, 'ServerSetting.json'), 'utf8')).toContain('"Unknown":1');
    await rm(dest, { recursive: true, force: true });
  });

  it('rejects backup while instance is running', async () => {
    alive = true;
    await expect(service.createBackup(meta.id, {})).rejects.toThrow(/运行/);
  });

  it('restores a backup and creates a pre-restore snapshot', async () => {
    const backup = await service.createBackup(meta.id, {}, 'manual');

    // 篡改世界数据，模拟误操作
    await writeFile(join(dir, 'Worlds', 'World', 'Project.json'), '{"Name":["string","CORRUPTED"]}');

    const res = await service.restoreBackup(meta.id, backup.id, { snapshot: true });
    expect(res.restored.id).toBe(backup.id);
    expect(res.snapshot?.type).toBe('pre-restore');

    const restored = await readFile(join(dir, 'Worlds', 'World', 'Project.json'), 'utf8');
    expect(restored).toContain('"W"');
    expect(restored).not.toContain('CORRUPTED');

    const list = await service.listBackups(meta.id);
    expect(list.some((b) => b.type === 'pre-restore')).toBe(true);
  });

  it('stops a running instance before restore', async () => {
    const backup = await service.createBackup(meta.id, {}, 'manual');
    alive = true;
    await service.restoreBackup(meta.id, backup.id, { snapshot: false });
    expect(alive).toBe(false);
  });

  it('removes a backup', async () => {
    const b = await service.createBackup(meta.id, {}, 'manual');
    await service.removeBackup(meta.id, b.id);
    expect(await service.listBackups(meta.id)).toHaveLength(0);
    await expect(service.getBackup(meta.id, b.id)).rejects.toThrow();
  });

  it('enforces retention keeping the newest backups', async () => {
    const env = parseEnv({ BACKUP_KEEP: '2' });
    const svc = new BackupService(store, supervisor, new SettingsService(env));
    await svc.onModuleInit();
    const first = await svc.createBackup(meta.id, {}, 'auto', '1');
    await new Promise((r) => setTimeout(r, 10));
    const second = await svc.createBackup(meta.id, {}, 'auto', '2');
    await new Promise((r) => setTimeout(r, 10));
    const third = await svc.createBackup(meta.id, {}, 'auto', '3');

    const list = await svc.listBackups(meta.id);
    expect(list).toHaveLength(2);
    const ids = list.map((b) => b.id);
    expect(ids).toContain(third.id);
    expect(ids).toContain(second.id);
    expect(ids).not.toContain(first.id);
  });

  it('runScheduledBackups backs up stopped instances only', async () => {
    await service.runScheduledBackups();
    let list = await service.listBackups(meta.id);
    expect(list).toHaveLength(1);
    expect(list[0].type).toBe('auto');

    alive = true;
    await service.runScheduledBackups();
    list = await service.listBackups(meta.id);
    expect(list).toHaveLength(1); // 运行中未新增
  });

  it('rejects a backup for a missing world', async () => {
    await expect(service.createBackup(meta.id, { world: 'Nope' })).rejects.toThrow(/不存在/);
  });

  it('round-trips through the low-level archive helpers', async () => {
    const out = join(dir, 'x.tar.gz');
    const size = await createTarGz(out, dir, ['Worlds/World']);
    expect(size).toBeGreaterThan(0);
    const dest = join(dir, '__low');
    await extractTarGz(out, dest);
    expect(await readFile(join(dest, 'Worlds', 'World', 'Regions', 'r0.region'), 'utf8')).toBe('REGION-BYTES');
    await rm(dest, { recursive: true, force: true });
  });
});
