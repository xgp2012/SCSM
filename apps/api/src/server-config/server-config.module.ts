import { Global, Module } from '@nestjs/common';
import { ServerConfigService } from './server-config.service';

@Global()
@Module({
  providers: [ServerConfigService],
  exports: [ServerConfigService],
})
export class ServerConfigModule {}
