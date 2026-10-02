import { Controller, Get } from '@nestjs/common';
import type { HealthResponse } from '@sc-panel/shared';
import { Public } from '../auth/public.decorator';

@Controller('health')
export class HealthController {
  @Public()
  @Get()
  get(): HealthResponse {
    return {
      status: 'ok',
      name: 'sc-panel-api',
      version: process.env.npm_package_version ?? '0.0.0',
      uptimeSec: Math.round(process.uptime()),
      timestamp: new Date().toISOString(),
    };
  }
}
