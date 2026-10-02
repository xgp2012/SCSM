import { afterEach, describe, expect, it, vi } from 'vitest';
import { usePanelSettings, useAuditLog } from '../app/composables/useSettings';

const settings = {
  backup: { intervalMs: 20000, keep: 5 },
  download: { giteeMirror: '', maxBytes: 2 * 1024 * 1024 * 1024, cacheMs: 300000 },
  ui: { theme: 'dark' },
  runtime: {
    version: '0.0.0',
    node: 'v24',
    platform: 'win32-x64',
    dataDir: 'C:/data',
    uptimeSec: 12,
  },
};

const audit = {
  records: [
    {
      id: 'a1',
      ts: '2026-10-01T00:00:00.000Z',
      action: 'auth.login',
      outcome: 'ok',
      actor: 'admin',
    },
  ],
  total: 1,
  days: ['2026-10-01'],
};

describe('usePanelSettings', () => {
  afterEach(() => vi.unstubAllGlobals());

  it('loads settings', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => ({ ok: true, status: 200, text: async () => JSON.stringify({ ok: true, data: settings }) })),
    );
    const { settings: s, load } = usePanelSettings();
    await load();
    expect(s.value?.backup.keep).toBe(5);
  });

  it('saves settings via PUT', async () => {
    const calls: Array<{ method: string; url: string }> = [];
    vi.stubGlobal(
      'fetch',
      vi.fn(async (url: string, init?: RequestInit) => {
        calls.push({ method: init?.method ?? 'GET', url });
        return { ok: true, status: 200, text: async () => JSON.stringify({ ok: true, data: settings }) };
      }),
    );
    const { save } = usePanelSettings();
    const out = await save({ backup: { keep: 3 } });
    expect(out?.backup.keep).toBe(5);
    expect(calls[0].method).toBe('PUT');
  });
});

describe('useAuditLog', () => {
  afterEach(() => vi.unstubAllGlobals());

  it('queries with filters and returns records', async () => {
    let captured = '';
    vi.stubGlobal(
      'fetch',
      vi.fn(async (url: string) => {
        captured = url;
        return { ok: true, status: 200, text: async () => JSON.stringify({ ok: true, data: audit }) };
      }),
    );
    const { records, result, query } = useAuditLog();
    await query({ action: 'auth.login', limit: 100 });
    expect(captured).toContain('action=auth.login');
    expect(records.value).toHaveLength(1);
    expect(result.value?.total).toBe(1);
  });
});
