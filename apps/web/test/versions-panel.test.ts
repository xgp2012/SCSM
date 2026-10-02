import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { mount, flushPromises } from '@vue/test-utils';
import { createPinia, setActivePinia } from 'pinia';
import VersionsPanel from '../app/components/version/VersionsPanel.vue';

const releases = {
  source: 'gitee',
  fallback: false,
  hostPlatform: 'windows',
  fetchedAt: Date.now(),
  cached: false,
  releases: [
    {
      tag: 'x26.06.19',
      name: 'x26.06.19',
      publishedAt: '2026-06-19T12:48:25+08:00',
      assets: [
        {
          name: '[服务端]SCNETx26.06.19z1.zip',
          url: 'https://gitee.com/dl/a.zip',
          size: 23114052,
          isServerPackage: true,
          platform: 'windows',
          isSourceArchive: false,
        },
        {
          name: '[电脑版][Windows]SCNET.zip',
          url: 'https://gitee.com/dl/client.zip',
          size: 1,
          isServerPackage: false,
          platform: 'windows',
          isSourceArchive: false,
        },
      ],
      recommendedAsset: '[服务端]SCNETx26.06.19z1.zip',
    },
  ],
};

const tasks = [
  {
    id: 't1',
    phase: 'downloading',
    tag: 'x26.06.19',
    asset: '[服务端]SCNETx26.06.19z1.zip',
    mode: 'new',
    totalBytes: 23114052,
    receivedBytes: 11557026,
    percent: 50,
    speedBps: 1000000,
    createdAt: new Date().toISOString(),
    updatedAt: new Date().toISOString(),
  },
];

describe('VersionsPanel', () => {
  const calls: Array<{ url: string; method: string; body?: unknown }> = [];

  beforeEach(() => {
    setActivePinia(createPinia());
    calls.length = 0;
    vi.stubGlobal(
      'fetch',
      vi.fn(async (url: string, init?: RequestInit) => {
        calls.push({ url, method: init?.method ?? 'GET', body: init?.body });
        let data: unknown = [];
        if (url.includes('/versions/releases')) data = releases;
        else if (url.includes('/versions/tasks')) data = tasks;
        else if (url.includes('/instances')) data = { instances: [], total: 0, running: 0 };
        return { ok: true, status: 200, text: async () => JSON.stringify({ ok: true, data }) };
      }),
    );
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it('loads releases and renders only server assets', async () => {
    const wrapper = mount(VersionsPanel);
    await flushPromises();
    expect(calls.some((c) => c.url.endsWith('/versions/releases'))).toBe(true);
    expect(wrapper.text()).toContain('x26.06.19');
    expect(wrapper.text()).toContain('[服务端]SCNETx26.06.19z1.zip');
    // 客户端包不应作为可安装项出现
    expect(wrapper.text()).not.toContain('[电脑版][Windows]SCNET.zip');
    expect(wrapper.text()).toContain('推荐');
    wrapper.unmount();
  });

  it('polls task progress', async () => {
    const wrapper = mount(VersionsPanel);
    await flushPromises();
    expect(wrapper.text()).toContain('下载中');
    expect(wrapper.text()).toContain('50%');
    wrapper.unmount();
  });

  it('shows fallback notice when provided', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async (url: string) => {
        const data = url.includes('/versions/releases')
          ? { ...releases, fallback: true, notice: '拉取失败（展示本地缓存）', releases: [] }
          : url.includes('/instances')
            ? { instances: [], total: 0, running: 0 }
            : [];
        return { ok: true, status: 200, text: async () => JSON.stringify({ ok: true, data }) };
      }),
    );
    const wrapper = mount(VersionsPanel);
    await flushPromises();
    expect(wrapper.text()).toContain('展示本地缓存');
    wrapper.unmount();
  });

  it('offers manual upload fallback entry', async () => {
    const wrapper = mount(VersionsPanel);
    await flushPromises();
    expect(wrapper.text()).toContain('手动上传版本包');
    wrapper.unmount();
  });
});
