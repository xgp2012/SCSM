import { Module } from '@nestjs/common';
import { EnvModule } from '../config/env.module';
import { InstanceModule } from '../instance/instance.module';
import { SystemModule } from '../system/system.module';
import { MetricsService } from './metrics.service';
import { ProbeController } from './probe.controller';

@Module({
  imports: [EnvModule, InstanceModule, SystemModule],
  controllers: [ProbeController],
  providers: [MetricsService],
  exports: [MetricsService],
})
export class ProbeModule {}
