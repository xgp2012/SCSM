import { describe, expect, it, beforeEach, afterEach } from 'vitest';
import { mkdtemp, rm, readFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { SettingsService } from '../src/settings/settings.service';
import { parseEnv } from '../src/config/env';

describe('SettingsService', () => {
  let dir: string;
  let svc: SettingsService;

  beforeEach(async () => {
    dir = await mkdtemp(join(tmpdir(), 'sc-settings-'));
    svc = new SettingsService(
      parseEnv({ PANEL_DATA_DIR: dir, BACKUP_INTERVAL_MS: '20000', BACKUP_KEEP: '7', GITEE_MIRROR: 'mirror.local' }),
    );
    await svc.onModuleInit();
  });

  afterEach(async () => {
    await rm(dir, { recursive: true, force: true });
  });

  it('seeds defaults from env', () => {
    const s = svc.get();
    expect(s.backup.intervalMs).toBe(20000);
    expect(s.backup.keep).toBe(7);
    expect(s.download.giteeMirror).toBe('mirror.local');
    expect(s.ui.theme).toBe('dark');
  });

  it('updates, persists to disk and notifies listeners', async () => {
    let notified = 0;
    svc.onChange(() => notified++);
    const next = await svc.update({ backup: { intervalMs: 0, keep: 3 }, ui: { theme: 'system' } });
    expect(next.backup.intervalMs).toBe(0);
    expect(next.backup.keep).toBe(3);
    expect(next.ui.theme).toBe('system');
    expect(notified).toBe(1);

    const raw = JSON.parse(await readFile(join(dir, 'panel-settings.json'), 'utf8'));
    expect(raw.backup.keep).toBe(3);
  });

  it('reloads persisted settings on init', async () => {
    await svc.update({ download: { giteeMirror: 'cdn.example.com' } });
    const svc2 = new SettingsService(parseEnv({ PANEL_DATA_DIR: dir }));
    await svc2.onModuleInit();
    expect(svc2.get().download.giteeMirror).toBe('cdn.example.com');
  });

  it('ignores invalid values', async () => {
    const next = await svc.update({ backup: { keep: -5 }, ui: { theme: 'neon' as never } });
    expect(next.backup.keep).toBe(7);
    expect(next.ui.theme).toBe('dark');
  });
});
