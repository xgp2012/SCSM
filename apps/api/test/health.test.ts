import { describe, expect, it } from 'vitest';
import { Test } from '@nestjs/testing';
import { HealthController } from '../src/health/health.controller';

describe('HealthController', () => {
  it('returns ok payload', async () => {
    const moduleRef = await Test.createTestingModule({
      controllers: [HealthController],
    }).compile();

    const controller = moduleRef.get(HealthController);
    const res = controller.get();

    expect(res.status).toBe('ok');
    expect(res.name).toBe('sc-panel-api');
    expect(typeof res.uptimeSec).toBe('number');
    expect(new Date(res.timestamp).toString()).not.toBe('Invalid Date');
  });
});
