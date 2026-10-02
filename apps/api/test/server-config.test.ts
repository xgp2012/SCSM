import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import { mkdir, mkdtemp, readFile, rm, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { ServerConfigService } from '../src/server-config/server-config.service';
import { SECRET_PLACEHOLDER } from '@sc-panel/shared';

const SETTINGS_XML = `<?xml version="1.0" encoding="utf-8"?>
<Settings>
  <Setting Name="ResizableWindowSize" Value="1280,720" />
  <Setting Name="ServerPort" Value="28887" />
  <Setting Name="EnableMod" Value="True" />
  <DisableMods />
  <Configs Language="zh-CN" />
</Settings>
`;

const SERVER_SETTING_JSON = JSON.stringify(
  {
    Autorun: false,
    WorldPath: 'app:/Worlds/World',
    WorldName: 'ScWorld',
    WorldMaxPlayers: 20,
    WorldPassword: '',
    Unknown_Field: 'keep-me',
  },
  null,
  2,
);

const LEVEL_JSON = JSON.stringify({ Alice: 100 }, null, 2);
const BAN_JSON = JSON.stringify({
  BanUserList: ['Bob'],
  BanUserIpList: [],
  BanIpList: [],
  Unknown: 1,
});
const LIMIT_JSON = JSON.stringify({
  WaterLength: 7,
  MagmaLength: 4,
  LimitBlockBreak: true,
  LimitExplode: false,
  CustomField: 'keep',
});
const PASSWORD_JSON = JSON.stringify(
  {
    IsUse: false,
    DefaultPassword: '123456',
    PlayerPassword: { Alice: 'secret' },
  },
  null,
  2,
);

describe('ServerConfigService', () => {
  let dir: string;
  let svc: ServerConfigService;

  beforeEach(async () => {
    dir = await mkdtemp(join(tmpdir(), 'sc-config-'));
    svc = new ServerConfigService();
    await mkdir(join(dir, 'Configs'), { recursive: true });
    await mkdir(join(dir, 'Plugins'), { recursive: true });
    await writeFile(join(dir, 'Settings.xml'), SETTINGS_XML, 'utf8');
    await writeFile(join(dir, 'ServerSetting.json'), SERVER_SETTING_JSON, 'utf8');
    await writeFile(join(dir, 'Configs', 'LevelConfig.json'), LEVEL_JSON, 'utf8');
    await writeFile(join(dir, 'Configs', 'BanConfig.json'), BAN_JSON, 'utf8');
    await writeFile(join(dir, 'Configs', 'LimitConfig.json'), LIMIT_JSON, 'utf8');
    await writeFile(join(dir, 'Plugins', 'Password.json'), PASSWORD_JSON, 'utf8');
  });

  afterEach(async () => {
    await rm(dir, { recursive: true, force: true });
  });

  it('reads Settings.xml into a name→value map', async () => {
    const settings = await svc.readSettingsXml(dir);
    expect(settings.ServerPort).toBe('28887');
    expect(settings.EnableMod).toBe('True');
  });

  it('updates a known key and preserves unknown nodes/order', async () => {
    await svc.updateSettingsXml(dir, { ServerPort: '29000' });
    const raw = await readFile(join(dir, 'Settings.xml'), 'utf8');
    expect(raw).toContain('Name="ServerPort" Value="29000"');
    expect(raw).toContain('<DisableMods');
    expect(raw).toContain('<Configs Language="zh-CN"');
    // 顺序：ServerPort 仍在第二个 Setting
    const portIdx = raw.indexOf('ServerPort');
    const modIdx = raw.indexOf('EnableMod');
    expect(portIdx).toBeLessThan(modIdx);
  });

  it('appends a previously unknown key', async () => {
    await svc.updateSettingsXml(dir, { BrandNewKey: '1' });
    const raw = await readFile(join(dir, 'Settings.xml'), 'utf8');
    expect(raw).toContain('Name="BrandNewKey" Value="1"');
  });

  it('merges ServerSetting.json without dropping unknown fields', async () => {
    await svc.updateServerSetting(dir, { WorldMaxPlayers: 50, Autorun: true });
    const parsed = JSON.parse(await readFile(join(dir, 'ServerSetting.json'), 'utf8'));
    expect(parsed.WorldMaxPlayers).toBe(50);
    expect(parsed.Autorun).toBe(true);
    expect(parsed.Unknown_Field).toBe('keep-me');
    expect(parsed.WorldName).toBe('ScWorld');
  });

  it('masks WorldPassword when reading bundle and keeps SECRET placeholder on write', async () => {
    await svc.updateServerSetting(dir, { WorldPassword: 'hunter2' });
    const bundle = await svc.readBundle(dir, 'i-1', false);
    expect(bundle.sections.serverSetting.WorldPassword).toBe(SECRET_PLACEHOLDER);

    // 提交占位符不改变原值
    await svc.updateServerSetting(dir, { WorldPassword: SECRET_PLACEHOLDER, WorldName: 'W2' });
    const raw = JSON.parse(await readFile(join(dir, 'ServerSetting.json'), 'utf8'));
    expect(raw.WorldPassword).toBe('hunter2');
    expect(raw.WorldName).toBe('W2');
  });

  it('reads and writes LevelConfig preserving unknown entries', async () => {
    const level = await svc.readLevelConfig(dir);
    expect(level).toEqual({ Alice: 100 });
    await svc.writeJsonSection(dir, 'level', { ...level, Bob: 50 });
    const parsed = JSON.parse(await readFile(join(dir, 'Configs', 'LevelConfig.json'), 'utf8'));
    expect(parsed).toEqual({ Alice: 100, Bob: 50 });
  });

  it('reads and writes BanConfig preserving unknown keys', async () => {
    const ban = await svc.readBanConfig(dir);
    expect(ban.BanUserList).toEqual(['Bob']);
    await svc.writeJsonSection(dir, 'ban', { ...ban, BanUserList: ['Bob', 'Eve'] });
    const parsed = JSON.parse(await readFile(join(dir, 'Configs', 'BanConfig.json'), 'utf8'));
    expect(parsed.BanUserList).toEqual(['Bob', 'Eve']);
    expect(parsed.Unknown).toBe(1);
  });

  it('reads LimitConfig including legacy/unknown fields', async () => {
    const limit = await svc.readLimitConfig(dir);
    expect(limit).toMatchObject({ WaterLength: 7, LimitBlockBreak: true, CustomField: 'keep' });
  });

  it('returns null for missing JSON sections', async () => {
    await rm(join(dir, 'Configs', 'LimitConfig.json'));
    expect(await svc.readLimitConfig(dir)).toBeNull();
  });

  it('masks password config and restores placeholders on merge', async () => {
    const masked = await svc.readPasswordConfig(dir);
    expect(masked?.DefaultPassword).toBe(SECRET_PLACEHOLDER);
    expect(masked?.PlayerPassword.Alice).toBe(SECRET_PLACEHOLDER);

    const current = await svc.readJsonSection<{
      DefaultPassword: string;
      PlayerPassword: Record<string, string>;
    }>(dir, 'password');
    const merged = svc.mergePasswordSection(
      {
        IsUse: true,
        DefaultPassword: SECRET_PLACEHOLDER,
        PlayerPassword: { Alice: SECRET_PLACEHOLDER, Bob: 'new' },
      },
      current as never,
    );
    expect(merged.IsUse).toBe(true);
    expect(merged.DefaultPassword).toBe('123456');
    expect(merged.PlayerPassword.Alice).toBe('secret');
    expect(merged.PlayerPassword.Bob).toBe('new');
  });

  it('validates invalid section payloads', () => {
    expect(() => svc.validateSection('level', { Alice: -1 })).toThrow();
    expect(() => svc.validateSection('level', { Alice: 'x' })).toThrow();
    expect(() => svc.validateSection('ban', { BanUserList: 'not-array' })).toThrow();
    expect(() => svc.validateSection('limit', { LimitBlockBreak: 'yes' })).toThrow();
    expect(() => svc.validateSection('limit', { WaterLength: -3 })).toThrow();
    expect(() => svc.validateSection('password', { PlayerPassword: [] })).toThrow();
    expect(() => svc.validateSection('level', 'nope')).toThrow();
    expect(() => svc.validateSection('level', { Alice: 1 })).not.toThrow();
  });

  it('returns a full bundle with section metadata', async () => {
    const bundle = await svc.readBundle(dir, 'inst-1', true);
    expect(bundle.instanceId).toBe('inst-1');
    expect(bundle.running).toBe(true);
    expect(bundle.sections.level).toEqual({ Alice: 100 });
    expect(bundle.sections.ban.BanUserList).toEqual(['Bob']);
    expect(bundle.sections.limit?.WaterLength).toBe(7);
    expect(bundle.meta.find((m) => m.key === 'limit')?.effective).toBe(false);
    expect(bundle.meta.find((m) => m.key === 'serverSetting')?.writableWhileRunning).toBe(false);
  });
});
