import { Module } from '@nestjs/common';
import { EnvModule } from '../config/env.module';
import { InstanceController } from './instance.controller';
import { ConfigController } from './config.controller';
import { InstanceService } from './instance.service';
import { InstanceStore } from './instance.store';
import { InstanceSupervisor } from './instance.supervisor';

@Module({
  imports: [EnvModule],
  controllers: [InstanceController, ConfigController],
  providers: [InstanceService, InstanceStore, InstanceSupervisor],
  exports: [InstanceService, InstanceSupervisor, InstanceStore],
})
export class InstanceModule {}
