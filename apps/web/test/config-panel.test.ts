import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { mount, flushPromises } from '@vue/test-utils';
import ConfigPanel from '../app/components/instance/ConfigPanel.vue';
import { SECRET_PLACEHOLDER } from '@sc-panel/shared';

const bundle = {
  instanceId: 'inst-1',
  running: false,
  sections: {
    settings: { ServerPort: '28887', EnableMod: 'True', UnknownKey: 'x' },
    serverSetting: { WorldName: 'ScWorld', WorldMaxPlayers: 20, GameMode: 2, WorldPassword: SECRET_PLACEHOLDER },
    level: { Alice: 100 },
    ban: { BanUserList: ['Bob'], BanUserIpList: [], BanIpList: [], Unknown: 7 },
    limit: { WaterLength: 7, MagmaLength: 4, LimitBlockBreak: true, LimitExplode: false },
    password: { IsUse: false, DefaultPassword: SECRET_PLACEHOLDER, PlayerPassword: { Alice: SECRET_PLACEHOLDER } },
  },
  meta: [
    { key: 'settings', label: '面板/服务端设置', file: 'Settings.xml', effective: true, writableWhileRunning: true },
    { key: 'serverSetting', label: '世界设置', file: 'ServerSetting.json', effective: true, writableWhileRunning: false },
    { key: 'level', label: '玩家权限', file: 'Configs/LevelConfig.json', effective: true, writableWhileRunning: false },
    { key: 'ban', label: '封禁列表', file: 'Configs/BanConfig.json', effective: true, writableWhileRunning: false },
    { key: 'limit', label: '限制插件', file: 'Configs/LimitConfig.json', effective: false, writableWhileRunning: false, note: '编译产物中未发现' },
    { key: 'password', label: '玩家密码', file: 'Plugins/Password.json', effective: false, writableWhileRunning: false, note: '源码插件未编译' },
  ],
};

describe('ConfigPanel', () => {
  const calls: Array<{ url: string; method: string; body?: unknown }> = [];

  beforeEach(() => {
    calls.length = 0;
    vi.stubGlobal(
      'fetch',
      vi.fn(async (url: string, init?: RequestInit) => {
        calls.push({ url, method: init?.method ?? 'GET', body: init?.body });
        return {
          ok: true,
          status: 200,
          text: async () => JSON.stringify({ ok: true, data: bundle }),
        };
      }),
    );
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it('loads config bundle and renders all section tabs', async () => {
    const wrapper = mount(ConfigPanel, { props: { instanceId: 'inst-1' } });
    await flushPromises();
    expect(calls[0].url).toContain('/instances/inst-1/config');
    const text = wrapper.text();
    expect(text).toContain('面板/服务端设置');
    expect(text).toContain('玩家权限');
    expect(text).toContain('封禁列表');
    expect(text).toContain('玩家密码');
    expect(text).toContain('限制插件');
  });

  it('shows the settings port and preserves unknown-entry count', async () => {
    const wrapper = mount(ConfigPanel, { props: { instanceId: 'inst-1' } });
    await flushPromises();
    const input = wrapper.find('input[type="number"]');
    expect((input.element as HTMLInputElement).value).toBe('28887');
    expect(wrapper.text()).toContain('1 个未管理条目');
  });

  it('labels legacy/source-only sections when selected', async () => {
    const wrapper = mount(ConfigPanel, { props: { instanceId: 'inst-1' } });
    await flushPromises();
    const limitTab = wrapper.findAll('button').find((b) => b.text() === '限制插件');
    await limitTab!.trigger('click');
    expect(wrapper.text()).toContain('源码/遗留');
  });

  it('saves settings through PUT with string values', async () => {
    const wrapper = mount(ConfigPanel, { props: { instanceId: 'inst-1' } });
    await flushPromises();
    const save = wrapper.findAll('button').find((b) => b.text() === '保存');
    await save!.trigger('click');
    await flushPromises();
    const put = calls.find((c) => c.method === 'PUT');
    expect(put?.url).toBe('/api/instances/inst-1/config/settings');
    expect(JSON.parse(put!.body as string).ServerPort).toBe('28887');
  });
});
