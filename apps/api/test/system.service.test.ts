import { describe, expect, it } from 'vitest';
import { SystemService } from '../src/system/system.service';

describe('SystemService', () => {
  it('snapshot 返回真实主机指标且字段合理', async () => {
    const svc = new SystemService();
    const a = await svc.snapshot();
    expect(a.cpuCount).toBeGreaterThan(0);
    expect(a.memTotalMB).toBeGreaterThan(0);
    expect(a.memUsedMB).toBeGreaterThanOrEqual(0);
    expect(a.memPercent).toBeGreaterThanOrEqual(0);
    expect(a.memPercent).toBeLessThanOrEqual(100);
    expect(a.cpu).toBeGreaterThanOrEqual(0);
    expect(a.cpu).toBeLessThanOrEqual(100);
    expect(typeof a.platform).toBe('string');
    expect(Array.isArray(a.loadAvg)).toBe(true);
    expect(a.ts).toBeGreaterThan(0);
  });

  it('连续采样 ts 递增', async () => {
    const svc = new SystemService();
    const a = await svc.snapshot();
    await new Promise((r) => setTimeout(r, 5));
    const b = await svc.snapshot();
    expect(b.ts).toBeGreaterThan(a.ts);
  });
});
