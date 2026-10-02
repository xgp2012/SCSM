import { Module } from '@nestjs/common';
import { InstanceModule } from '../instance/instance.module';
import { VersionController } from './version.controller';
import { VersionService } from './version.service';
import { DownloadService } from './download.service';

@Module({
  imports: [InstanceModule],
  controllers: [VersionController],
  providers: [VersionService, DownloadService],
  exports: [VersionService, DownloadService],
})
export class VersionModule {}
