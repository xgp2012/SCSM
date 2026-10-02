import { describe, expect, it } from 'vitest';
import { parseEnv } from '../src/config/env';

describe('env config', () => {
  it('applies defaults', () => {
    const env = parseEnv({});
    expect(env.PANEL_PORT).toBe(3001);
    expect(env.PANEL_HOST).toBe('127.0.0.1');
    expect(env.PANEL_CORS_ORIGIN).toBe('http://localhost:3000');
  });

  it('coerces string port', () => {
    const env = parseEnv({ PANEL_PORT: '8080' });
    expect(env.PANEL_PORT).toBe(8080);
  });

  it('parses LOG_PRETTY flag', () => {
    expect(parseEnv({ LOG_PRETTY: 'false' }).LOG_PRETTY).toBe(false);
    expect(parseEnv({ LOG_PRETTY: 'true' }).LOG_PRETTY).toBe(true);
  });

  it('rejects invalid port', () => {
    expect(() => parseEnv({ PANEL_PORT: 'abc' })).toThrow(/环境变量校验失败/);
  });
});
