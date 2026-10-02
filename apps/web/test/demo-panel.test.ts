import { describe, expect, it } from 'vitest';
import { mount } from '@vue/test-utils';
import DemoPanel from '../app/components/demo/DemoPanel.vue';

describe('DemoPanel', () => {
  it('renders the project title and ptY mention', () => {
    const wrapper = mount(DemoPanel);
    expect(wrapper.text()).toContain('SurvivalcraftNet 开服面板');
    expect(wrapper.text()).toContain('node-pty');
  });

  it('renders fuxsto-design Button components', () => {
    const wrapper = mount(DemoPanel);
    const buttons = wrapper.findAll('button');
    expect(buttons.length).toBeGreaterThanOrEqual(3);
    const texts = buttons.map((b) => b.text());
    expect(texts).toContain('启动实例');
    expect(texts).toContain('打开对话框');
  });

  it('increments click count when primary button is clicked', async () => {
    const wrapper = mount(DemoPanel);
    const primary = wrapper
      .findAll('button')
      .find((b) => b.text() === '启动实例');
    expect(primary).toBeTruthy();
    await primary!.trigger('click');
    expect(wrapper.find('[data-test="click-count"]').text()).toContain('1');
  });
});
