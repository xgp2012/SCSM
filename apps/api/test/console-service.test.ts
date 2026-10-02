import { describe, expect, it } from 'vitest';
import { EventEmitter } from 'node:events';
import type { InstanceRuntime } from '@sc-panel/shared';
import { ConsoleService } from '../src/console/console.service';
import type { InstanceSupervisor } from '../src/instance/instance.supervisor';

/** 最小 Supervisor 替身：仅需 EventEmitter + getRuntime/listRuntimes */
class FakeSupervisor extends EventEmitter {
  runtime: InstanceRuntime = { status: 'running', pid: 1234 };
  getRuntime(): InstanceRuntime {
    return this.runtime;
  }
  listRuntimes(): Record<string, InstanceRuntime> {
    return { 'i-1': this.runtime };
  }
}

function makeService() {
  const sup = new FakeSupervisor();
  const svc = new ConsoleService(sup as unknown as InstanceSupervisor);
  return { sup, svc };
}

describe('ConsoleService', () => {
  it('accumulates raw + line into rollback buffer', () => {
    const { sup, svc } = makeService();
    sup.emit('raw', { instanceId: 'i-1', data: '\u001B[32mhello\u001B[0m' });
    sup.emit('line', { instanceId: 'i-1', line: 'hello', ts: 1 });
    const buf = svc.getBuffer('i-1');
    expect(buf.getLines()).toEqual(['hello']);
    expect(buf.getAnsiSnapshot()).toContain('hello');
  });

  it('fans events out to all subscribers of an instance', () => {
    const { sup, svc } = makeService();
    const a: unknown[] = [];
    const b: unknown[] = [];
    svc.subscribe('i-1', (e) => a.push(e));
    svc.subscribe('i-1', (e) => b.push(e));

    sup.emit('line', { instanceId: 'i-1', line: 'x', ts: 2 });

    expect(a).toHaveLength(1);
    expect(b).toHaveLength(1);
    expect(a[0]).toMatchObject({ type: 'console.line', line: 'x' });
  });

  it('unsubscribe stops delivery', () => {
    const { sup, svc } = makeService();
    const seen: unknown[] = [];
    const off = svc.subscribe('i-1', (e) => seen.push(e));
    sup.emit('line', { instanceId: 'i-1', line: 'a', ts: 1 });
    off();
    sup.emit('line', { instanceId: 'i-1', line: 'b', ts: 2 });
    expect(seen).toHaveLength(1);
  });

  it('maps supervisor status into instance.status events', () => {
    const { sup, svc } = makeService();
    const seen: Array<Record<string, unknown>> = [];
    svc.subscribe('i-1', (e) => seen.push(e as Record<string, unknown>));
    sup.emit('status', { instanceId: 'i-1', runtime: { status: 'running', pid: 99, serverPort: 28899 } });
    expect(seen[0]).toMatchObject({ type: 'instance.status', status: 'running', pid: 99, serverPort: 28899 });
  });

  it('isolates subscriber exceptions', () => {
    const { sup, svc } = makeService();
    const seen: unknown[] = [];
    svc.subscribe('i-1', () => {
      throw new Error('boom');
    });
    svc.subscribe('i-1', (e) => seen.push(e));
    expect(() => sup.emit('line', { instanceId: 'i-1', line: 'x', ts: 1 })).not.toThrow();
    expect(seen).toHaveLength(1);
  });
});
