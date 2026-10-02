import { describe, expect, it } from 'vitest';
import {
  INSTANCE_STATUSES,
  type Instance,
  type InstanceStatus,
  type WsServerEvent,
} from '../src/index';

describe('shared types', () => {
  it('exposes the full status machine', () => {
    const expected: InstanceStatus[] = [
      'stopped',
      'starting',
      'running',
      'stopping',
      'crashed',
    ];
    expect([...INSTANCE_STATUSES]).toEqual(expected);
  });

  it('instance shape is usable', () => {
    const inst: Instance = {
      id: 'x',
      name: 'demo',
      source: 'template',
      dir: '/tmp/demo',
      serverPort: 28887,
      version: 'x26.07.01.01',
      launchArgs: ['--sckey-token-local', '--enhanced'],
      worldPath: 'app:/Worlds/World',
      worldName: 'ScWorld',
      maxPlayers: 20,
      autoRestart: true,
      autoStart: false,
      autoRun: true,
      createdAt: new Date(0).toISOString(),
      runtime: { status: 'stopped' },
    };
    expect(inst.runtime.status).toBe('stopped');
    expect(inst.maxPlayers).toBe(20);
  });

  it('ws events are discriminated unions', () => {
    const ev: WsServerEvent = {
      type: 'console.line',
      instanceId: 'x',
      line: '开启服务器成功，端口 28887',
      ts: 0,
    };
    expect(ev.type).toBe('console.line');
  });
});
