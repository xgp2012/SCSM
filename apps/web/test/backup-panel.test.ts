import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { mount, flushPromises } from '@vue/test-utils';
import BackupPanel from '../app/components/instance/BackupPanel.vue';

const worlds = [
  { name: 'World', relPath: 'Worlds/World', dir: '/x/Worlds/World', isCurrent: true, size: 2048, files: 3, hasProject: true },
  { name: 'Old', relPath: 'Worlds/Old', dir: '/x/Worlds/Old', isCurrent: false, size: 512, files: 1, hasProject: true },
];

const backups = [
  {
    id: 'b-manual',
    instanceId: 'inst-1',
    file: 'b-manual.tar.gz',
    size: 4096,
    createdAt: new Date('2026-10-01T10:00:00Z').toISOString(),
    type: 'manual',
    world: 'World',
    includes: { world: true, config: true },
  },
  {
    id: 'b-pre',
    instanceId: 'inst-1',
    file: 'b-pre.tar.gz',
    size: 1024,
    createdAt: new Date('2026-10-01T09:00:00Z').toISOString(),
    type: 'pre-restore',
    world: 'World',
    includes: { world: true, config: true },
  },
];

describe('BackupPanel', () => {
  const calls: Array<{ url: string; method: string; body?: unknown }> = [];

  beforeEach(() => {
    calls.length = 0;
    vi.stubGlobal(
      'fetch',
      vi.fn(async (url: string, init?: RequestInit) => {
        calls.push({ url, method: init?.method ?? 'GET', body: init?.body });
        const data = url.endsWith('/worlds')
          ? worlds
          : url.includes('/backups')
            ? backups
            : [];
        return { ok: true, status: 200, text: async () => JSON.stringify({ ok: true, data }) };
      }),
    );
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it('loads worlds and backups on mount', async () => {
    const wrapper = mount(BackupPanel, { props: { instanceId: 'inst-1' } });
    await flushPromises();
    expect(calls.some((c) => c.url.endsWith('/instances/inst-1/worlds'))).toBe(true);
    expect(calls.some((c) => c.url.endsWith('/instances/inst-1/backups'))).toBe(true);
    expect(wrapper.text()).toContain('World');
    expect(wrapper.text()).toContain('当前');
    expect(wrapper.text()).toContain('备份列表（2）');
  });

  it('renders backup type labels and download links', async () => {
    const wrapper = mount(BackupPanel, { props: { instanceId: 'inst-1' } });
    await flushPromises();
    expect(wrapper.text()).toContain('手动');
    expect(wrapper.text()).toContain('恢复前快照');
    const link = wrapper.find('a[href="/api/instances/inst-1/backups/b-manual/download"]');
    expect(link.exists()).toBe(true);
  });

  it('disables backup creation while running', async () => {
    const wrapper = mount(BackupPanel, { props: { instanceId: 'inst-1', running: true } });
    await flushPromises();
    expect(wrapper.text()).toContain('实例运行中不可创建备份');
    const btn = wrapper.findAll('button').find((b) => b.text() === '立即备份');
    expect(btn!.attributes('disabled')).toBeDefined();
  });

  it('posts a manual backup with world + config selection', async () => {
    const wrapper = mount(BackupPanel, { props: { instanceId: 'inst-1' } });
    await flushPromises();
    const btn = wrapper.findAll('button').find((b) => b.text() === '立即备份');
    await btn!.trigger('click');
    await flushPromises();
    const post = calls.find((c) => c.method === 'POST');
    expect(post?.url).toBe('/api/instances/inst-1/backups');
    expect(JSON.parse(post!.body as string)).toMatchObject({ world: 'World', includeConfig: true });
  });
});
