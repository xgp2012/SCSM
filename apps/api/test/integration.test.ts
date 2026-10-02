import { afterAll, beforeAll, describe, expect, it } from 'vitest';
import { Test } from '@nestjs/testing';
import { INestApplication, ValidationPipe } from '@nestjs/common';
import { APP_GUARD } from '@nestjs/core';
import cookieParser from 'cookie-parser';
import request from 'supertest';
import { mkdir, mkdtemp, rm, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { EnvModule } from '../src/config/env.module';
import { ServerConfigModule } from '../src/server-config/server-config.module';
import { AuditModule } from '../src/audit/audit.module';
import { SettingsModule } from '../src/settings/settings.module';
import { AuthModule } from '../src/auth/auth.module';
import { JwtAuthGuard } from '../src/auth/jwt-auth.guard';
import { InstanceModule } from '../src/instance/instance.module';
import { HealthModule } from '../src/health/health.module';

/**
 * 集成测试：auth 守卫 + 登录 + 实例 CRUD（plants.md §9.5.1）。
 * 使用临时模板目录（仅放一个伪造的 Survivalcraft.dll）避免依赖真实服务端包。
 */
describe('API integration (auth + instances)', () => {
  let app: INestApplication;
  let dataDir: string;
  let templateDir: string;

  beforeAll(async () => {
    dataDir = await mkdtemp(join(tmpdir(), 'sc-panel-data-'));
    templateDir = await mkdtemp(join(tmpdir(), 'sc-panel-tpl-'));
    await writeFile(join(templateDir, 'Survivalcraft.dll'), 'fake');
    await writeFile(join(templateDir, 'Settings.xml'), '<Settings><Setting Name="ServerPort" Value="1" /></Settings>');
    await writeFile(
      join(templateDir, 'ServerSetting.json'),
      JSON.stringify({ WorldName: 'W', WorldMaxPlayers: 20, Autorun: false, WorldPassword: '' }),
    );
    await mkdir(join(templateDir, 'Configs'), { recursive: true });
    await writeFile(join(templateDir, 'Configs', 'LevelConfig.json'), '{}');
    await writeFile(
      join(templateDir, 'Configs', 'BanConfig.json'),
      JSON.stringify({ BanUserList: [], BanUserIpList: [], BanIpList: [], Unknown: 7 }),
    );
    await writeFile(
      join(templateDir, 'Configs', 'LimitConfig.json'),
      JSON.stringify({ WaterLength: 7, MagmaLength: 4, LimitBlockBreak: true, LimitExplode: false }),
    );
    await mkdir(join(templateDir, 'Plugins'), { recursive: true });
    await writeFile(
      join(templateDir, 'Plugins', 'Password.json'),
      JSON.stringify({ IsUse: false, DefaultPassword: '123456', PlayerPassword: {} }),
    );

    process.env.PANEL_USER = 'admin';
    process.env.PANEL_PASSWORD = 'secret';
    process.env.JWT_SECRET = 'test-secret';
    process.env.PANEL_DATA_DIR = dataDir;
    process.env.SERVER_TEMPLATE_DIR = templateDir;
    process.env.STOP_GRACE_MS = '500';

    const moduleRef = await Test.createTestingModule({
      imports: [EnvModule, ServerConfigModule, AuditModule, SettingsModule, AuthModule, InstanceModule, HealthModule],
      providers: [{ provide: APP_GUARD, useClass: JwtAuthGuard }],
    }).compile();

    app = moduleRef.createNestApplication();
    app.setGlobalPrefix('api');
    app.use(cookieParser());
    app.useGlobalPipes(new ValidationPipe({ whitelist: true, transform: true }));
    await app.init();
  });

  afterAll(async () => {
    await app?.close();
    await rm(dataDir, { recursive: true, force: true });
    await rm(templateDir, { recursive: true, force: true });
  });

  it('rejects unauthenticated access to protected routes (401)', async () => {
    await request(app.getHttpServer()).get('/api/instances').expect(401);
    await request(app.getHttpServer()).get('/api/auth/me').expect(401);
  });

  it('allows public health check', async () => {
    await request(app.getHttpServer()).get('/api/health').expect(200);
  });

  it('rejects wrong password', async () => {
    await request(app.getHttpServer())
      .post('/api/auth/login')
      .send({ username: 'admin', password: 'nope' })
      .expect(401);
  });

  let cookie = '';

  it('logs in with correct password and issues cookie', async () => {
    const res = await request(app.getHttpServer())
      .post('/api/auth/login')
      .send({ username: 'admin', password: 'secret' })
      .expect(201);
    expect(res.body.data.user.username).toBe('admin');
    const setCookie = res.headers['set-cookie'] as unknown as string[];
    expect(setCookie).toBeTruthy();
    cookie = setCookie[0].split(';')[0];
    expect(cookie).toContain('sc_panel_token=');
  });

  it('returns current user when cookie present', async () => {
    const res = await request(app.getHttpServer())
      .get('/api/auth/me')
      .set('Cookie', cookie)
      .expect(200);
    expect(res.body.data.user.role).toBe('admin');
  });

  let createdId = '';
  let createdDir = '';

  it('creates an instance from template and writes port to Settings.xml', async () => {
    const res = await request(app.getHttpServer())
      .post('/api/instances')
      .set('Cookie', cookie)
      .send({ name: '测试实例', serverPort: 29111, maxPlayers: 30 })
      .expect(201);
    createdId = res.body.data.id;
    createdDir = res.body.data.dir;
    expect(res.body.data.serverPort).toBe(29111);
    expect(res.body.data.runtime.status).toBe('stopped');

    const { readFile } = await import('node:fs/promises');
    const xml = await readFile(join(res.body.data.dir, 'Settings.xml'), 'utf8');
    expect(xml).toContain('Name="ServerPort" Value="29111"');
    const ss = JSON.parse(await readFile(join(res.body.data.dir, 'ServerSetting.json'), 'utf8'));
    expect(ss.WorldMaxPlayers).toBe(30);
  });
  it('lists instances', async () => {
    const res = await request(app.getHttpServer())
      .get('/api/instances')
      .set('Cookie', cookie)
      .expect(200);
    expect(res.body.data.total).toBe(1);
    expect(res.body.data.instances[0].id).toBe(createdId);
  });

  it('updates instance', async () => {
    const res = await request(app.getHttpServer())
      .patch(`/api/instances/${createdId}`)
      .set('Cookie', cookie)
      .send({ name: '改名', maxPlayers: 40 })
      .expect(200);
    expect(res.body.data.name).toBe('改名');
    expect(res.body.data.maxPlayers).toBe(40);
  });

  it('preserves unspecified fields on partial update', async () => {
    const before = await request(app.getHttpServer())
      .get(`/api/instances/${createdId}`)
      .set('Cookie', cookie)
      .expect(200);
    expect(before.body.data.launchArgs.length).toBeGreaterThan(0);
    expect(before.body.data.version).toBeTruthy();

    const res = await request(app.getHttpServer())
      .patch(`/api/instances/${createdId}`)
      .set('Cookie', cookie)
      .send({ serverPort: 29123 })
      .expect(200);
    // 仅端口变化，其它字段不得被 undefined 覆盖
    expect(res.body.data.name).toBe('改名');
    expect(res.body.data.version).toBe(before.body.data.version);
    expect(res.body.data.launchArgs).toEqual(before.body.data.launchArgs);
    expect(res.body.data.worldName).toBe(before.body.data.worldName);
  });

  it('rejects invalid body (validation)', async () => {
    await request(app.getHttpServer())
      .post('/api/instances')
      .set('Cookie', cookie)
      .send({ name: '', serverPort: 999999 })
      .expect(400);
  });

  it('rejects unknown lifecycle action', async () => {
    await request(app.getHttpServer())
      .post(`/api/instances/${createdId}/explode`)
      .set('Cookie', cookie)
      .expect(400);
  });

  it('reads full config bundle with masked password fields', async () => {
    const res = await request(app.getHttpServer())
      .get(`/api/instances/${createdId}/config`)
      .set('Cookie', cookie)
      .expect(200);
    const bundle = res.body.data;
    expect(bundle.instanceId).toBe(createdId);
    expect(bundle.sections.settings.ServerPort).toBe('29123');
    expect(bundle.sections.level).toEqual({});
    expect(bundle.sections.serverSetting.WorldName).toBeTruthy();
    expect(bundle.meta.find((m: { key: string }) => m.key === 'level')?.effective).toBe(true);
  });

  it('updates Settings.xml via config section and preserves unknown', async () => {
    const res = await request(app.getHttpServer())
      .put(`/api/instances/${createdId}/config/settings`)
      .set('Cookie', cookie)
      .send({ ServerPort: '29199', EnableStatLogging: 'False' })
      .expect(200);
    expect(res.body.data.sections.settings.ServerPort).toBe('29199');
  });

  it('adds level + ban entries and preserves unknown fields', async () => {
    await request(app.getHttpServer())
      .put(`/api/instances/${createdId}/config/level`)
      .set('Cookie', cookie)
      .send({ Alice: 100 })
      .expect(200);
    const ban = await request(app.getHttpServer())
      .put(`/api/instances/${createdId}/config/ban`)
      .set('Cookie', cookie)
      .send({ BanUserList: ['Bob'] })
      .expect(200);
    expect(ban.body.data.sections.ban.BanUserList).toEqual(['Bob']);
    expect(ban.body.data.sections.ban.Unknown).toBe(7);
  });

  it('updates world password with mask round-trip', async () => {
    await request(app.getHttpServer())
      .patch(`/api/instances/${createdId}`)
      .set('Cookie', cookie)
      .send({ worldPassword: 'hunter2', gameMode: 2 })
      .expect(200);
    const res = await request(app.getHttpServer())
      .get(`/api/instances/${createdId}/config`)
      .set('Cookie', cookie)
      .expect(200);
    expect(res.body.data.sections.serverSetting.WorldPassword).toBe('********');
    expect(res.body.data.sections.serverSetting.GameMode).toBe(2);

    const { readFile } = await import('node:fs/promises');
    const raw = JSON.parse(await readFile(join(createdDir, 'ServerSetting.json'), 'utf8'));
    expect(raw.WorldPassword).toBe('hunter2');
  });

  it('rejects invalid config payloads with clear errors', async () => {
    await request(app.getHttpServer())
      .put(`/api/instances/${createdId}/config/level`)
      .set('Cookie', cookie)
      .send({ Alice: -5 })
      .expect(400);
    await request(app.getHttpServer())
      .put(`/api/instances/${createdId}/config/ban`)
      .set('Cookie', cookie)
      .send({ BanUserList: 'oops' })
      .expect(400);
    await request(app.getHttpServer())
      .put(`/api/instances/${createdId}/config/nope`)
      .set('Cookie', cookie)
      .send({ a: 1 })
      .expect(400);
  });

  it('deletes instance', async () => {
    await request(app.getHttpServer())
      .delete(`/api/instances/${createdId}`)
      .set('Cookie', cookie)
      .expect(200);
    await request(app.getHttpServer())
      .get(`/api/instances/${createdId}`)
      .set('Cookie', cookie)
      .expect(404);
  });

  it('logout clears cookie', async () => {
    await request(app.getHttpServer()).post('/api/auth/logout').set('Cookie', cookie).expect(201);
  });
});
