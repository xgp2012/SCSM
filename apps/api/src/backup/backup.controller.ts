import {
  Body,
  Controller,
  Delete,
  Get,
  Param,
  Post,
  Req,
  Res,
} from '@nestjs/common';
import { createReadStream } from 'node:fs';
import type { Request, Response } from 'express';
import type { AuthUser, BackupMeta, CreateBackupInput, RestoreBackupInput, WorldInfo } from '@sc-panel/shared';
import { CurrentUser } from '../auth/current-user.decorator';
import { AuditService } from '../audit/audit.service';
import { BackupService } from './backup.service';

@Controller('instances')
export class BackupController {
  constructor(
    private readonly service: BackupService,
    private readonly audit: AuditService,
  ) {}

  /** 列出实例的世界存档目录 */
  @Get(':id/worlds')
  async worlds(@Param('id') id: string): Promise<{ ok: true; data: WorldInfo[] }> {
    return { ok: true, data: await this.service.listWorlds(id) };
  }

  /** 列出实例的备份 */
  @Get(':id/backups')
  async list(@Param('id') id: string): Promise<{ ok: true; data: BackupMeta[] }> {
    return { ok: true, data: await this.service.listBackups(id) };
  }

  /** 创建备份 */
  @Post(':id/backups')
  async create(
    @Param('id') id: string,
    @Body() body: CreateBackupInput,
    @CurrentUser() user: AuthUser,
    @Req() req: Request,
  ): Promise<{ ok: true; data: BackupMeta }> {
    const backup = await this.service.createBackup(id, body ?? {});
    void this.audit.log({
      action: 'backup.create',
      actor: user.username,
      instanceId: id,
      detail: `创建备份 ${backup.file}（${backup.world}）`,
      meta: { backupId: backup.id, world: backup.world, size: backup.size },
      ip: clientIp(req),
    });
    return { ok: true, data: backup };
  }

  /** 恢复备份（停止实例 → 预快照 → 覆盖） */
  @Post(':id/backups/:backupId/restore')
  async restore(
    @Param('id') id: string,
    @Param('backupId') backupId: string,
    @Body() body: RestoreBackupInput,
    @CurrentUser() user: AuthUser,
    @Req() req: Request,
  ): Promise<{ ok: true; data: { restored: BackupMeta; snapshot?: BackupMeta } }> {
    const result = await this.service.restoreBackup(id, backupId, body ?? {});
    void this.audit.log({
      action: 'backup.restore',
      actor: user.username,
      instanceId: id,
      detail: `恢复备份 ${backupId}`,
      meta: { backupId, snapshotId: result.snapshot?.id },
      ip: clientIp(req),
    });
    return { ok: true, data: result };
  }

  /** 下载备份归档（tar.gz） */
  @Get(':id/backups/:backupId/download')
  async download(
    @Param('id') id: string,
    @Param('backupId') backupId: string,
    @CurrentUser() user: AuthUser,
    @Req() req: Request,
    @Res() res: Response,
  ): Promise<void> {
    const archive = await this.service.getArchivePath(id, backupId);
    const backup = await this.service.getBackup(id, backupId);
    void this.audit.log({
      action: 'backup.download',
      actor: user.username,
      instanceId: id,
      detail: `下载备份 ${backup.file}`,
      meta: { backupId },
      ip: clientIp(req),
    });
    res.setHeader('Content-Type', 'application/gzip');
    res.setHeader(
      'Content-Disposition',
      `attachment; filename="${encodeURIComponent(backup.file)}"`,
    );
    createReadStream(archive).pipe(res);
  }

  /** 删除备份 */
  @Delete(':id/backups/:backupId')
  async remove(
    @Param('id') id: string,
    @Param('backupId') backupId: string,
    @CurrentUser() user: AuthUser,
    @Req() req: Request,
  ): Promise<{ ok: true; data: { removed: true } }> {
    await this.service.removeBackup(id, backupId);
    void this.audit.log({
      action: 'backup.remove',
      actor: user.username,
      instanceId: id,
      detail: `删除备份 ${backupId}`,
      meta: { backupId },
      ip: clientIp(req),
    });
    return { ok: true, data: { removed: true } };
  }
}

function clientIp(req: Request): string | undefined {
  const fwd = req.headers['x-forwarded-for'];
  if (typeof fwd === 'string' && fwd) return fwd.split(',')[0].trim();
  return req.ip ?? req.socket?.remoteAddress ?? undefined;
}
