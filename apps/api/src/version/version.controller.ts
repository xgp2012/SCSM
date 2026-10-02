import {
  BadRequestException,
  Body,
  Controller,
  Get,
  Param,
  Post,
  Query,
  Req,
  UploadedFile,
  UseInterceptors,
} from '@nestjs/common';
import { Throttle } from '@nestjs/throttler';
import { FileInterceptor } from '@nestjs/platform-express';
import type { Request } from 'express';
import type {
  CreateDownloadInput,
  DownloadTask,
  ReleaseListResponse,
  UploadInstallInput,
  VersionSourceInfo,
  VersionSourceId,
} from '@sc-panel/shared';
import { AuditService } from '../audit/audit.service';
import { DownloadService } from './download.service';
import { VersionService } from './version.service';

@Controller('versions')
export class VersionController {
  constructor(
    private readonly versions: VersionService,
    private readonly downloads: DownloadService,
    private readonly audit: AuditService,
  ) {}

  /** 版本来源列表（默认 gitee，预留第三方源） */
  @Get('sources')
  sources(): { ok: true; data: VersionSourceInfo[] } {
    const info = this.versions.sourceInfo();
    return {
      ok: true,
      data: [
        {
          id: info.id,
          label: info.label,
          repo: info.repo,
          isDefault: info.isDefault,
          available: info.available,
        },
      ],
    };
  }

  /** Gitee releases 列表（缓存 + 降级） */
  @Get('releases')
  async releases(
    @Query('source') source?: string,
    @Query('refresh') refresh?: string,
  ): Promise<{ ok: true; data: ReleaseListResponse }> {
    const useCache = refresh !== 'true';
    return {
      ok: true,
      data: await this.versions.listReleases(source as VersionSourceId | undefined, useCache),
    };
  }

  /** 强制刷新缓存 */
  @Post('releases/refresh')
  async refresh(): Promise<{ ok: true; data: ReleaseListResponse }> {
    await this.versions.clearCache();
    return { ok: true, data: await this.versions.listReleases(undefined, false) };
  }

  /** 触发下载+安装任务，立即返回任务 ID（前端轮询进度）；限流防滥用 */
  @Throttle({ default: { limit: 10, ttl: 60_000 } })
  @Post('download')
  async download(
    @Body() body: CreateDownloadInput,
    @Req() req: Request,
  ): Promise<{ ok: true; data: DownloadTask }> {
    if (!body?.tag || !body?.asset || !body?.mode) {
      throw new BadRequestException('缺少 tag / asset / mode');
    }
    const task = this.downloads.startDownload(body);
    void this.audit.log({
      action: 'version.download',
      detail: `下载版本包 ${body.tag}/${body.asset}`,
      meta: { tag: body.tag, asset: body.asset, mode: body.mode, taskId: task.id },
      ip: clientIp(req),
    });
    return { ok: true, data: task };
  }

  /** 列出全部下载任务 */
  @Get('tasks')
  tasks(): { ok: true; data: DownloadTask[] } {
    return { ok: true, data: this.downloads.listTasks() };
  }

  /** 查询单个任务进度 */
  @Get('tasks/:id')
  task(@Param('id') id: string): { ok: true; data: DownloadTask } {
    return { ok: true, data: this.downloads.getTask(id) };
  }

  /** 手动上传版本包兜底（zip/tar.gz）；限流 */
  @Throttle({ default: { limit: 10, ttl: 60_000 } })
  @Post('upload')
  @UseInterceptors(
    FileInterceptor('file', {
      limits: { fileSize: 2 * 1024 * 1024 * 1024 },
    }),
  )
  async upload(
    @UploadedFile() file: Express.Multer.File | undefined,
    @Body() body: UploadInstallInput,
    @Req() req: Request,
  ): Promise<{ ok: true; data: { task: DownloadTask; instanceId: string; instanceName: string } }> {
    if (!file) throw new BadRequestException('缺少上传文件字段 file');
    const result = await this.downloads.installFromUploadBuffer(
      file.buffer,
      body ?? { mode: 'new' },
      file.originalname || 'version.zip',
    );
    void this.audit.log({
      action: 'version.upload',
      detail: `上传版本包 ${file.originalname} (${file.size} bytes)`,
      instanceId: result.install.instanceId,
      instanceName: result.install.instanceName,
      meta: { mode: body?.mode ?? 'new', filename: file.originalname, bytes: file.size },
      ip: clientIp(req),
    });
    return {
      ok: true,
      data: {
        task: result.task,
        instanceId: result.install.instanceId,
        instanceName: result.install.instanceName,
      },
    };
  }
}

function clientIp(req: Request): string | undefined {
  const fwd = req.headers['x-forwarded-for'];
  if (typeof fwd === 'string' && fwd) return fwd.split(',')[0].trim();
  return req.ip ?? req.socket?.remoteAddress ?? undefined;
}
