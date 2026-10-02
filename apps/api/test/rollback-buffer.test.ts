import { describe, expect, it } from 'vitest';
import { RollbackBuffer } from '../src/console/rollback-buffer';

describe('RollbackBuffer', () => {
  it('keeps append order for lines', () => {
    const buf = new RollbackBuffer(10, 1024);
    buf.appendLine('a');
    buf.appendLine('b');
    buf.appendLine('c');
    expect(buf.getLines()).toEqual(['a', 'b', 'c']);
  });

  it('evicts oldest lines beyond maxLines', () => {
    const buf = new RollbackBuffer(3, 1024);
    for (const l of ['1', '2', '3', '4', '5']) buf.appendLine(l);
    expect(buf.getLines()).toEqual(['3', '4', '5']);
    expect(buf.size.lines).toBe(3);
  });

  it('trims ansi beyond maxAnsiBytes keeping the tail', () => {
    const buf = new RollbackBuffer(10, 5);
    buf.appendRaw('abcde');
    buf.appendRaw('fgh');
    const snap = buf.getAnsiSnapshot();
    expect(snap.startsWith('\u001B[0m')).toBe(true);
    expect(snap.endsWith('defgh')).toBe(true);
    expect(buf.size.ansiBytes).toBeLessThanOrEqual(5);
  });

  it('returns a defensive copy of lines', () => {
    const buf = new RollbackBuffer();
    buf.appendLine('x');
    const lines = buf.getLines();
    lines.push('mutated');
    expect(buf.getLines()).toEqual(['x']);
  });

  it('returns empty snapshot without ansi', () => {
    const buf = new RollbackBuffer();
    expect(buf.getAnsiSnapshot()).toBe('');
  });
});
