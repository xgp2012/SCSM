import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { defineComponent } from 'vue';
import { mount } from '@vue/test-utils';
import { useConsoleSocket } from '../app/composables/useConsoleSocket';

class MockWebSocket {
  static instances: MockWebSocket[] = [];
  static OPEN = 1;
  readyState = MockWebSocket.OPEN;
  binaryType = 'blob';
  sent: string[] = [];
  onopen: (() => void) | null = null;
  onclose: ((ev: { code: number; reason: string }) => void) | null = null;
  onerror: (() => void) | null = null;
  onmessage: ((ev: { data: string }) => void) | null = null;
  url: string;

  constructor(url: string) {
    this.url = url;
    MockWebSocket.instances.push(this);
    queueMicrotask(() => this.onopen?.());
  }
  send(data: string) {
    this.sent.push(data);
  }
  close(code = 1000, reason = '') {
    this.readyState = 3;
    this.onclose?.({ code, reason });
  }
  emit(payload: unknown) {
    this.onmessage?.({ data: JSON.stringify(payload) });
  }
}

interface Exposed {
  connect: () => void;
  command: (c: string) => void;
  resize: (c: number, r: number) => void;
}

/** 挂载一个能拿到 composable 方法的宿主组件（不 stub window） */
function mountHost(onRaw: (d: string) => void) {
  let exposed!: Exposed;
  const Comp = defineComponent({
    setup() {
      exposed = useConsoleSocket('inst-1', { onRaw });
      return () => null;
    },
  });
  mount(Comp);
  return exposed;
}

describe('useConsoleSocket', () => {
  beforeEach(() => {
    MockWebSocket.instances = [];
    vi.stubGlobal('WebSocket', MockWebSocket as unknown as typeof WebSocket);
  });
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it('connects with instanceId in ws url', async () => {
    const socket = mountHost(() => {});
    socket.connect();
    await Promise.resolve();
    expect(MockWebSocket.instances[0].url).toContain('/ws/console?instanceId=inst-1');
  });

  it('dispatches console.raw to onRaw', async () => {
    const chunks: string[] = [];
    const socket = mountHost((d) => chunks.push(d));
    socket.connect();
    await Promise.resolve();
    MockWebSocket.instances[0].emit({ type: 'console.raw', instanceId: 'inst-1', data: 'hello' });
    expect(chunks).toEqual(['hello']);
  });

  it('encodes command and resize frames', async () => {
    const socket = mountHost(() => {});
    socket.connect();
    await Promise.resolve();
    const ws = MockWebSocket.instances[0];
    socket.command('help');
    socket.resize(80, 24);
    await Promise.resolve();
    expect(JSON.parse(ws.sent[0])).toMatchObject({ type: 'console.command', command: 'help' });
    expect(JSON.parse(ws.sent[1])).toMatchObject({ type: 'terminal.resize', cols: 80, rows: 24 });
  });

  it('dispatches instance.stats / player.list / system.stats (M3)', async () => {
    const stats: unknown[] = [];
    const players: unknown[] = [];
    const system: unknown[] = [];
    let exposed!: Exposed;
    const Comp = defineComponent({
      setup() {
        exposed = useConsoleSocket('inst-1', {
          onStats: (s) => stats.push(s),
          onPlayerList: (p) => players.push(p),
          onSystemStats: (s) => system.push(s),
        });
        return () => null;
      },
    });
    mount(Comp);
    exposed.connect();
    await Promise.resolve();
    const ws = MockWebSocket.instances[0];
    ws.emit({ type: 'instance.stats', instanceId: 'inst-1', cpu: 12.5, memMB: 300, ts: 1 });
    ws.emit({
      type: 'player.list',
      instanceId: 'inst-1',
      online: 3,
      maxOnline: 20,
      gameMode: 'Survival',
      hasPassword: false,
      probeOnline: true,
      version: 'x26.06.19',
    });
    ws.emit({ type: 'system.stats', cpu: 20, memPercent: 40, ts: 2 });
    expect(stats[0]).toMatchObject({ cpu: 12.5, memMB: 300 });
    expect(players[0]).toMatchObject({ online: 3, probeOnline: true, version: 'x26.06.19' });
    expect(system[0]).toMatchObject({ cpu: 20, memPercent: 40 });
  });
});
