import { describe, expect, it, beforeEach, afterEach } from 'vitest';
import { mkdtemp, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { AuditService } from '../src/audit/audit.service';
import { parseEnv } from '../src/config/env';

describe('AuditService', () => {
  let dir: string;
  let audit: AuditService;

  beforeEach(async () => {
    dir = await mkdtemp(join(tmpdir(), 'sc-audit-'));
    const env = parseEnv({ PANEL_DATA_DIR: dir });
    audit = new AuditService(env);
  });

  afterEach(async () => {
    await rm(dir, { recursive: true, force: true });
  });

  it('writes and queries records newest-first', async () => {
    await audit.log({ action: 'auth.login', actor: 'admin', detail: '登录成功' });
    await audit.log({ action: 'instance.start', instanceId: 'i1', detail: '启动' });
    const res = await audit.query({});
    expect(res.total).toBe(2);
    expect(res.records[0].action).toBe('instance.start');
    expect(res.records[0].outcome).toBe('ok');
    expect(res.records[0].id).toMatch(/[0-9a-f-]{36}/);
  });

  it('filters by action/instance/outcome and text', async () => {
    await audit.log({ action: 'auth.login.failed', actor: 'admin', outcome: 'failed', detail: '密码错误' });
    await audit.log({ action: 'console.command', instanceId: 'i2', detail: '发送命令 /help' });
    expect((await audit.query({ action: 'console.command' })).total).toBe(1);
    expect((await audit.query({ instanceId: 'i2' })).total).toBe(1);
    expect((await audit.query({ outcome: 'failed' })).total).toBe(1);
    expect((await audit.query({ q: 'help' })).total).toBe(1);
  });

  it('persists one jsonl line per record and lists days', async () => {
    await audit.log({ action: 'auth.logout' });
    const days = await audit.listDays();
    expect(days).toHaveLength(1);
    expect(days[0]).toMatch(/^\d{4}-\d{2}-\d{2}$/);
  });
});
