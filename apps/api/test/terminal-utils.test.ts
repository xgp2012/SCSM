import { describe, expect, it } from 'vitest';
import { LineSplitter, stripAnsi } from '../src/adapter/terminal-utils';

describe('stripAnsi', () => {
  it('removes CSI color sequences', () => {
    expect(stripAnsi('\u001b[32mhello\u001b[0m')).toBe('hello');
  });

  it('removes OSC title sequences', () => {
    expect(stripAnsi('\u001b]0;C:\\win\\cmd.exe\u0007text')).toBe('text');
  });
});

describe('LineSplitter', () => {
  it('splits on newlines and keeps partial line across chunks', () => {
    const s = new LineSplitter();
    expect(s.push('开启服务器成')).toEqual([]);
    expect(s.push('功，端口 28887\n')).toEqual(['开启服务器成功，端口 28887']);
  });

  it('drops ANSI and empty lines', () => {
    const s = new LineSplitter();
    const lines = s.push('\u001b[90m23:15:44 INFO: hi\u001b[0m\n\n');
    expect(lines).toEqual(['23:15:44 INFO: hi']);
  });

  it('takes the tail segment after carriage return', () => {
    const s = new LineSplitter();
    const lines = s.push('loading 10%\rloading 90%\rdone\n');
    expect(lines).toEqual(['done']);
  });

  it('flushes trailing buffer', () => {
    const s = new LineSplitter();
    s.push('no-newline');
    expect(s.flush()).toEqual(['no-newline']);
    expect(s.flush()).toEqual([]);
  });
});
