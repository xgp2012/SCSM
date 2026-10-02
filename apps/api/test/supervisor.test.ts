import { describe, expect, it, vi } from 'vitest';
import type { InstanceMeta, InstanceStatus } from '@sc-panel/shared';
import type { PanelEnv } from '../src/config/env';
import { parseEnv } from '../src/config/env';
import { InstanceSupervisor } from '../src/instance/instance.supervisor';
import type {
  AdapterEventListener,
  AdapterStartOptions,
  IServerAdapter,
} from '../src/adapter/server-adapter.interface';

class FakeAdapter implements IServerAdapter {
  private listeners = new Set<AdapterEventListener>();
  private alive = false;
  pid = 4242;
  startedArgs: string[] = [];
  commands: string[] = [];
  stopped = false;

  on(listener: AdapterEventListener): () => void {
    this.listeners.add(listener);
    return () => this.listeners.delete(listener);
  }
  private emit(e: Parameters<AdapterEventListener>[0]) {
    for (const l of this.listeners) l(e);
  }
  async start(opts: AdapterStartOptions) {
    this.alive = true;
    this.startedArgs = opts.args;
    return { pid: this.pid };
  }
  async stop() {
    this.stopped = true;
    this.sendCommand('/stop');
    this.alive = false;
    this.emit({ type: 'exit', info: { exitCode: 0 } });
  }
  kill() {
    this.alive = false;
    this.emit({ type: 'exit', info: { exitCode: 1 } });
  }
  sendCommand(cmd: string) {
    this.commands.push(cmd);
  }
  write() {}
  resize() {}
  isAlive() {
    return this.alive;
  }
  getPid() {
    return this.pid;
  }
  // 测试辅助
  line(text: string) {
    this.emit({ type: 'line', line: text, ts: Date.now() });
  }
  crash() {
    this.alive = false;
    this.emit({ type: 'exit', info: { exitCode: 1 } });
  }
}

function makeMeta(over: Partial<InstanceMeta> = {}): InstanceMeta {
  return {
    id: 'inst-1',
    name: 'demo',
    source: 'template',
    dir: '/tmp/demo',
    serverPort: 28887,
    version: 'x',
    launchArgs: ['--enhanced'],
    worldPath: 'app:/Worlds/World',
    worldName: 'ScWorld',
    maxPlayers: 20,
    autoRestart: false,
    autoStart: false,
    autoRun: false,
    createdAt: new Date(0).toISOString(),
    ...over,
  };
}

class TestSupervisor extends InstanceSupervisor {
  adapter = new FakeAdapter();
  protected override createAdapter(): IServerAdapter {
    return this.adapter;
  }
}

function statuses(sup: InstanceSupervisor, id: string): InstanceStatus[] {
  const seen: InstanceStatus[] = [];
  sup.on('status', (e: { instanceId: string; runtime: { status: InstanceStatus } }) => {
    if (e.instanceId === id) seen.push(e.runtime.status);
  });
  return seen;
}

describe('InstanceSupervisor state machine', () => {
  const env: PanelEnv = parseEnv({ STOP_GRACE_MS: '300' });

  it('transitions stopped → starting → running on startup markers', async () => {
    const sup = new TestSupervisor(env);
    const meta = makeMeta();
    sup.register(meta);
    const seen = statuses(sup, meta.id);

    await sup.start(meta.id);
    expect(sup.getRuntime(meta.id)?.status).toBe('starting');
    expect(sup.getRuntime(meta.id)?.pid).toBe(4242);

    sup.adapter.line('23:15:44 INFO: [StartServer]开启服务器成功，端口 28887');
    expect(sup.getRuntime(meta.id)?.serverPort).toBe(28887);
    sup.adapter.line('23:15:46 INFO: Entered screen "Game"');

    expect(sup.getRuntime(meta.id)?.status).toBe('running');
    expect(seen).toEqual(['starting', 'running']);
  });

  it('marks crashed when startup fails', async () => {
    const sup = new TestSupervisor(env);
    const meta = makeMeta();
    sup.register(meta);
    await sup.start(meta.id);
    sup.adapter.line('创建服务器失败，端口已被占用');
    expect(sup.getRuntime(meta.id)?.status).toBe('crashed');
  });

  it('distinguishes intentional stop (stopped) from crash (crashed)', async () => {
    const sup = new TestSupervisor(env);
    const meta = makeMeta();
    sup.register(meta);

    await sup.start(meta.id);
    sup.adapter.line('Entered screen "Game"');
    await sup.stop(meta.id);
    expect(sup.adapter.commands).toContain('/stop');
    expect(sup.getRuntime(meta.id)?.status).toBe('stopped');

    const sup2 = new TestSupervisor(env);
    sup2.register(makeMeta());
    await sup2.start(meta.id);
    sup2.adapter.crash();
    expect(sup2.getRuntime(meta.id)?.status).toBe('crashed');
  });

  it('auto-restarts after crash when enabled', async () => {
    vi.useFakeTimers();
    try {
      const sup = new TestSupervisor(env);
      const meta = makeMeta({ autoRestart: true });
      sup.register(meta);
      await sup.start(meta.id);
      sup.adapter.crash();
      expect(sup.getRuntime(meta.id)?.status).toBe('crashed');

      // 第一次退避 5s
      await vi.advanceTimersByTimeAsync(5_100);
      expect(sup.getRuntime(meta.id)?.status).toBe('starting');
    } finally {
      vi.useRealTimers();
    }
  });

  it('does not auto-restart when disabled', async () => {
    vi.useFakeTimers();
    try {
      const sup = new TestSupervisor(env);
      const meta = makeMeta({ autoRestart: false });
      sup.register(meta);
      await sup.start(meta.id);
      sup.adapter.crash();
      await vi.advanceTimersByTimeAsync(10_000);
      expect(sup.getRuntime(meta.id)?.status).toBe('crashed');
    } finally {
      vi.useRealTimers();
    }
  });
});
