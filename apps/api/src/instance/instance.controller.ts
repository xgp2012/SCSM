import {
  BadRequestException,
  Body,
  Controller,
  Delete,
  Get,
  Param,
  Patch,
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
  AuthUser,
  Instance,
  InstanceAction,
  OverviewResponse,
  TemplateInfo,
} from '@sc-panel/shared';
import { CurrentUser } from '../auth/current-user.decorator';
import { AuditService } from '../audit/audit.service';
import { InstanceService } from './instance.service';
import { InstanceSupervisor } from './instance.supervisor';
import { CreateInstanceDto, UpdateInstanceDto } from './dto/instance.dto';

@Controller('instances')
export class InstanceController {
  constructor(
    private readonly service: InstanceService,
    private readonly supervisor: InstanceSupervisor,
    private readonly audit: AuditService,
  ) {}

  @Get()
  async list(): Promise<{ ok: true; data: OverviewResponse }> {
    return { ok: true, data: await this.service.overview() };
  }

  @Get('template')
  async template(): Promise<{ ok: true; data: TemplateInfo }> {
    return { ok: true, data: await this.service.getTemplateInfo() };
  }

  @Get(':id')
  async get(@Param('id') id: string): Promise<{ ok: true; data: Instance }> {
    return { ok: true, data: await this.service.get(id) };
  }

  @Post()
  async create(
    @Body() dto: CreateInstanceDto,
    @CurrentUser() user: AuthUser,
    @Req() req: Request,
  ): Promise<{ ok: true; data: Instance }> {
    const inst = await this.service.create(dto);
    void this.audit.log({
      action: 'instance.create',
      actor: user.username,
      instanceId: inst.id,
      instanceName: inst.name,
      detail: `创建实例 ${inst.name}（source=${inst.source} port=${inst.serverPort}）`,
      meta: { source: inst.source, serverPort: inst.serverPort },
      ip: clientIp(req),
    });
    return { ok: true, data: inst };
  }

  @Patch(':id')
  async update(
    @Param('id') id: string,
    @Body() dto: UpdateInstanceDto,
    @CurrentUser() user: AuthUser,
    @Req() req: Request,
  ): Promise<{ ok: true; data: Instance }> {
    const inst = await this.service.update(id, dto);
    void this.audit.log({
      action: 'instance.update',
      actor: user.username,
      instanceId: id,
      instanceName: inst.name,
      detail: `更新实例设置`,
      meta: dto as Record<string, unknown>,
      ip: clientIp(req),
    });
    return { ok: true, data: inst };
  }

  @Delete(':id')
  async remove(
    @Param('id') id: string,
    @Query('deleteFiles') deleteFiles: string | undefined,
    @CurrentUser() user: AuthUser,
    @Req() req: Request,
  ): Promise<{ ok: true; data: { removed: true } }> {
    const before = await this.service.get(id).catch(() => null);
    await this.service.remove(id, deleteFiles === 'true');
    void this.audit.log({
      action: 'instance.remove',
      actor: user.username,
      instanceId: id,
      instanceName: before?.name,
      detail: `删除实例（deleteFiles=${deleteFiles === 'true'}）`,
      ip: clientIp(req),
    });
    return { ok: true, data: { removed: true } };
  }

  /** 上传基础包（zip）解压到 upload 类型实例目录 */
  @Post(':id/upload')
  @UseInterceptors(FileInterceptor('file'))
  async upload(
    @Param('id') id: string,
    @UploadedFile() file?: Express.Multer.File,
  ): Promise<{ ok: true; data: { installed: true } }> {
    if (!file) throw new BadRequestException('缺少上传文件字段 file');
    await this.service.installFromUpload(id, file.buffer);
    return { ok: true, data: { installed: true } };
  }

  /** 生命周期操作：start / stop / restart / kill（限流：每分 30 次） */
  @Throttle({ default: { limit: 30, ttl: 60_000 } })
  @Post(':id/:action')
  async action(
    @Param('id') id: string,
    @Param('action') action: string,
    @CurrentUser() user: AuthUser,
    @Req() req: Request,
  ): Promise<{ ok: true; data: Instance }> {
    const allowed: InstanceAction[] = ['start', 'stop', 'restart', 'kill'];
    if (!(allowed as string[]).includes(action)) {
      throw new BadRequestException(`不支持的操作: ${action}`);
    }
    switch (action as InstanceAction) {
      case 'start':
        await this.supervisor.start(id);
        break;
      case 'stop':
        await this.supervisor.stop(id);
        break;
      case 'restart':
        await this.supervisor.restart(id);
        break;
      case 'kill':
        this.supervisor.kill(id);
        break;
    }
    const inst = await this.service.get(id);
    void this.audit.log({
      action: `instance.${action}` as `instance.${InstanceAction}`,
      actor: user.username,
      instanceId: id,
      instanceName: inst.name,
      detail: `实例操作 ${action}`,
      ip: clientIp(req),
    });
    return { ok: true, data: inst };
  }
}

function clientIp(req: Request): string | undefined {
  const fwd = req.headers['x-forwarded-for'];
  if (typeof fwd === 'string' && fwd) return fwd.split(',')[0].trim();
  return req.ip ?? req.socket?.remoteAddress ?? undefined;
}
