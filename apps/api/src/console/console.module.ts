import { Module } from '@nestjs/common';
import { AuthModule } from '../auth/auth.module';
import { InstanceModule } from '../instance/instance.module';
import { ProbeModule } from '../probe/probe.module';
import { ConsoleController } from './console.controller';
import { ConsoleGateway } from './console.gateway';
import { ConsoleService } from './console.service';

@Module({
  imports: [AuthModule, InstanceModule, ProbeModule],
  controllers: [ConsoleController],
  providers: [ConsoleService, ConsoleGateway],
  exports: [ConsoleService],
})
export class ConsoleModule {}
