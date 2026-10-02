import {
  BadRequestException,
  Inject,
  Injectable,
  Logger,
} from '@nestjs/common';
import { randomUUID } from 'node:crypto';
import { createWriteStream } from 'node:fs';
import { access, constants, mkdir, rm, stat } from 'node:fs/promises';
import { Readable } from 'node:stream';
import { pipeline } from 'node:stream/promises';
import { join } from 'node:path';
import type {
  CreateDownloadInput,
  DownloadPhase,
  DownloadTask,
  InstallMode,
  UploadInstallInput,
} from '@sc-panel/shared';
import type { PanelEnv } from '../config/env';
import { PANEL_ENV } from '../config/env.module';
import { toAbsolute } from '../common/utils/paths';
import { InstanceService } from '../instance/instance.service';
import { SettingsService } from '../settings/settings.service';
import { AuditService } from '../audit/audit.service';
import { detectArchiveKind, safeFilename } from './archive-utils';
import { VersionService } from './version.service';

const REQUIRED_BINARY = 'Survivalcraft.dll';
/** 下载失败自动重试次数（含 Range 续传尝试） */
const MAX_DOWNLOAD_RETRIES = 3;
/** 单次 IO/网络错误后的退避基数（ms） */
const RETRY_BACKOFF_MS = 1500;

/**
 * 版本包下载/上传 + 安装任务（plants.md §5.8 / §7.3）。
 *
 * - 下载：后端代理流式拉取（跟随 Gitee 302、带 Referer/UA），带进度；
 * - **断点续传/重试**（M6 遗留，M7）：失败时按已下载字节数发 `Range` 续传，最多 3 次；
 * - 安装：解压到实例目录；支持「新建实例」与「覆盖既有实例（保留配置/存档）」；
 * - 任务状态经 REST 轮询（`GET /versions/tasks/:id`），无需 WS；
 * - 手动上传版本包作为兜底（Gitee 不可用时）。
 * - 体积上限/缓存时长来自 `SettingsService`（运行期可调）。
 */
@Injectable()
export class DownloadService {
  private readonly logger = new Logger('Download');
  private readonly tasks = new Map<string, DownloadTask>();
  private readonly tmpRoot: string;
  private readonly aborts = new Map<string, AbortController>();

  constructor(
    @Inject(PANEL_ENV) private readonly env: PanelEnv,
    private readonly versions: VersionService,
    private readonly instances: InstanceService,
    private readonly settings: SettingsService,
    private readonly audit: AuditService,
  ) {
    this.tmpRoot = join(toAbsolute(env.PANEL_DATA_DIR), 'versions', 'downloads');
  }

  private get maxBytes(): number {
    return this.settings.download.maxBytes;
  }

  // ── 查询 ─────────────────────────────────────────────

  listTasks(): DownloadTask[] {
    return [...this.tasks.values()].sort((a, b) => b.createdAt.localeCompare(a.createdAt));
  }

  getTask(id: string): DownloadTask {
    const task = this.tasks.get(id);
    if (!task) throw new BadRequestException(`下载任务不存在: ${id}`);
    return task;
  }

  // ── 下载任务 ─────────────────────────────────────────

  /** 创建下载任务并异步执行，立即返回任务（前端轮询进度） */
  startDownload(input: CreateDownloadInput): DownloadTask {
    this.validateInstallInput(input);
    const task: DownloadTask = {
      id: randomUUID(),
      phase: 'queued',
      tag: input.tag,
      asset: input.asset,
      mode: input.mode,
      receivedBytes: 0,
      createdAt: new Date().toISOString(),
      updatedAt: new Date().toISOString(),
    };
    this.tasks.set(task.id, task);
    void this.runDownload(task, input);
    return task;
  }

  private async runDownload(task: DownloadTask, input: CreateDownloadInput): Promise<void> {
    const archivePath = join(this.tmpRoot, `${task.id}-${safeFilename(input.asset)}`);
    try {
      const { assetUrl, assetSize, source } = await this.versions.resolveAsset(
        input.source,
        input.tag,
        input.asset,
      );
      if (assetSize && assetSize > this.maxBytes) {
        throw new Error(`资源体积 ${assetSize} 超过上限 ${this.maxBytes}`);
      }

      this.setPhase(task, 'downloading', { totalBytes: assetSize });
      await this.downloadToFile(task, assetUrl, archivePath, source.id);

      this.setPhase(task, 'extracting');
      await this.install(task, archivePath, input, detectArchiveKind(input.asset));

      this.setPhase(task, 'done');
      this.logger.log(`[${task.id}] 安装完成 tag=${task.tag} mode=${task.mode}`);
      void this.audit.log({
        action: 'version.install',
        instanceId: task.instanceId,
        instanceName: task.instanceName,
        detail: `安装版本 ${task.tag}/${task.asset} → ${task.instanceName ?? task.instanceId ?? ''}`,
        meta: { tag: task.tag, asset: task.asset, mode: task.mode, taskId: task.id },
      });
    } catch (err) {
      task.phase = 'failed';
      task.error = this.errText(err);
      task.updatedAt = new Date().toISOString();
      this.logger.warn(`[${task.id}] 失败: ${task.error}`);
    } finally {
      this.aborts.delete(task.id);
      await rm(archivePath, { force: true });
      const safe = safeFilename(input.asset);
      if (safe !== input.asset) await rm(join(this.tmpRoot, safe), { force: true });
    }
  }

  /**
   * 流式下载到文件并更新进度（速度/ETA）。
   * 失败时按已落盘字节数发 `Range` 续传，最多 `MAX_DOWNLOAD_RETRIES` 次（M6 遗留，M7）。
   */
  private async downloadToFile(
    task: DownloadTask,
    url: string,
    outPath: string,
    sourceId?: string,
  ): Promise<void> {
    await mkdir(this.tmpRoot, { recursive: true });

    let attempt = 0;
    for (;;) {
      attempt += 1;
      const already = await this.fileSizeOrZero(outPath);
      const controller = new AbortController();
      this.aborts.set(task.id, controller);
      try {
        await this.downloadOnce(task, url, outPath, sourceId, already, controller);
        break;
      } catch (err) {
        if (attempt >= MAX_DOWNLOAD_RETRIES || controller.signal.aborted || !this.isRetryable(err)) {
          this.aborts.delete(task.id);
          throw err;
        }
        const delay = RETRY_BACKOFF_MS * attempt;
        this.logger.warn(
          `[${task.id}] 下载中断（第 ${attempt} 次，已 ${already} 字节），${delay}ms 后续传: ${this.errText(err)}`,
        );
        task.updatedAt = new Date().toISOString();
        await new Promise((r) => setTimeout(r, delay));
      }
    }
    this.aborts.delete(task.id);

    const written = (await stat(outPath)).size;
    if (written <= 0) throw new Error('下载结果为空文件');
    if (task.totalBytes && written !== task.totalBytes) {
      throw new Error(`下载不完整：期望 ${task.totalBytes} 字节，实际 ${written} 字节`);
    }
    task.receivedBytes = written;
    task.percent = 100;
    task.updatedAt = new Date().toISOString();
  }

  /** 单次下载尝试；`resumeFrom` > 0 时发 Range 续传（追加写） */
  private async downloadOnce(
    task: DownloadTask,
    url: string,
    outPath: string,
    sourceId: string | undefined,
    resumeFrom: number,
    controller: AbortController,
  ): Promise<void> {
    const headers = { ...this.versions.headers(sourceId) };
    if (resumeFrom > 0) headers.Range = `bytes=${resumeFrom}-`;

    const res = await fetch(url, {
      redirect: 'follow',
      headers,
      signal: controller.signal,
    });

    // 服务端不支持续传（返回 200）时从头写；支持（206）时追加
    const resuming = resumeFrom > 0 && res.status === 206;
    if (!res.ok || !res.body) {
      // 4xx 为确定性错误（如 404），不重试；5xx/网络错误可重试
      const err = new Error(`下载失败：HTTP ${res.status} ${res.statusText}`) as Error & {
        retryable?: boolean;
      };
      err.retryable = res.status >= 500;
      throw err;
    }

    if (resuming) {
      const cr = res.headers.get('content-range');
      const total = cr ? Number(cr.split('/')[1]) : undefined;
      if (Number.isFinite(total) && total) task.totalBytes = total;
    } else {
      const len = Number(res.headers.get('content-length'));
      if (Number.isFinite(len) && len > 0) task.totalBytes = len;
      resumeFrom = 0;
      task.receivedBytes = 0;
    }

    if (task.totalBytes && task.totalBytes > this.maxBytes) {
      throw new Error(`资源体积超过上限 ${this.maxBytes}`);
    }

    let received = resumeFrom;
    const startedAt = Date.now();
    const nodeStream = Readable.fromWeb(res.body as Parameters<typeof Readable.fromWeb>[0]);
    nodeStream.on('data', (chunk: Buffer) => {
      received += chunk.length;
      task.receivedBytes = received;
      const elapsed = (Date.now() - startedAt) / 1000;
      if (elapsed > 0) {
        task.speedBps = (received - resumeFrom) / elapsed;
        const remain = (task.totalBytes ?? 0) - received;
        task.etaMs =
          task.speedBps > 0 && task.totalBytes ? (remain / task.speedBps) * 1000 : undefined;
      }
      if (task.totalBytes) {
        task.percent = Math.min(100, Math.round((received / task.totalBytes) * 100));
      }
      task.updatedAt = new Date().toISOString();
    });

    await pipeline(nodeStream, createWriteStream(outPath, { flags: resuming ? 'a' : 'w' }));
  }

  // ── 上传兜底 ─────────────────────────────────────────

  /** 手动上传版本包（zip/tar.gz）安装到新实例或覆盖既有实例 */
  async installFromUploadBuffer(
    buffer: Buffer,
    input: UploadInstallInput,
    filename: string,
  ): Promise<{ task: DownloadTask; install: { instanceId: string; instanceName: string } }> {
    this.validateInstallInput({ mode: input.mode, instanceId: input.instanceId, asset: filename });
    const kind = detectArchiveKind(filename);
    if (kind === 'unknown') {
      throw new BadRequestException('仅支持 zip 或 tar.gz 格式的版本包');
    }
    if (buffer.length > this.maxBytes) {
      throw new BadRequestException(`上传体积超过上限 ${this.maxBytes}`);
    }

    await mkdir(this.tmpRoot, { recursive: true });
    const archivePath = join(this.tmpRoot, `${randomUUID()}-${safeFilename(filename)}`);
    const { writeFile } = await import('node:fs/promises');
    await writeFile(archivePath, buffer);

    const task: DownloadTask = {
      id: randomUUID(),
      phase: 'extracting',
      tag: 'upload',
      asset: filename,
      mode: input.mode,
      totalBytes: buffer.length,
      receivedBytes: buffer.length,
      percent: 100,
      createdAt: new Date().toISOString(),
      updatedAt: new Date().toISOString(),
    };
    this.tasks.set(task.id, task);

    try {
      const install = await this.install(task, archivePath, { ...input, asset: filename }, kind);
      this.setPhase(task, 'done');
      return { task, install };
    } catch (err) {
      task.phase = 'failed';
      task.error = this.errText(err);
      task.updatedAt = new Date().toISOString();
      throw err;
    } finally {
      await rm(archivePath, { force: true });
    }
  }

  // ── 安装 ─────────────────────────────────────────────

  private async install(
    task: DownloadTask,
    archivePath: string,
    input: { mode: InstallMode; name?: string; instanceId?: string; preserve?: boolean; asset: string },
    kind: 'zip' | 'tar.gz' | 'unknown',
  ): Promise<{ instanceId: string; instanceName: string }> {
    const version = this.tagToVersion(task.tag);

    if (input.mode === 'overwrite') {
      if (!input.instanceId) throw new BadRequestException('覆盖安装需要指定 instanceId');
      this.setPhase(task, 'installing');
      const inst = await this.instances.overwriteInstall(input.instanceId, archivePath, {
        preserve: input.preserve ?? true,
        kind: kind === 'unknown' ? undefined : kind,
        version,
      });
      task.instanceId = inst.id;
      task.instanceName = inst.name;
      return { instanceId: inst.id, instanceName: inst.name };
    }

    // 新建实例：解压到临时目录后注册实例（端口/配置由 InstanceService 处理）
    const id = randomUUID();
    const dir = join(toAbsolute(this.env.PANEL_DATA_DIR), 'instances', id);
    this.setPhase(task, 'extracting', { instanceId: id });
    await mkdir(dir, { recursive: true });
    try {
      await this.extract(archivePath, dir, kind);
    } catch (err) {
      await rm(dir, { recursive: true, force: true });
      throw err;
    }
    if (!(await this.pathExists(join(dir, REQUIRED_BINARY)))) {
      // 单层目录包裹时上移（与 InstanceService.normalizeExtracted 同义）
      await this.flattenSingleRoot(dir);
    }
    if (!(await this.pathExists(join(dir, REQUIRED_BINARY)))) {
      await rm(dir, { recursive: true, force: true });
      throw new Error(`版本包缺少 ${REQUIRED_BINARY}`);
    }

    this.setPhase(task, 'installing', { instanceId: id });
    const inst = await this.instances.createFromVersion({
      name: input.name?.trim() || version || task.tag,
      dir,
      version,
    });
    task.instanceId = inst.id;
    task.instanceName = inst.name;
    return { instanceId: inst.id, instanceName: inst.name };
  }

  private async extract(
    archivePath: string,
    destDir: string,
    kind: 'zip' | 'tar.gz' | 'unknown',
  ): Promise<void> {
    const actual = kind === 'unknown' ? detectArchiveKind(archivePath) : kind;
    if (actual === 'tar.gz') {
      const { extract } = await import('tar');
      await extract({ file: archivePath, cwd: destDir, portable: true });
    } else {
      const AdmZip = (await import('adm-zip')).default;
      new AdmZip(archivePath).extractAllTo(destDir, true);
    }
  }

  private async flattenSingleRoot(dir: string): Promise<void> {
    const { readdir, cp, rm } = await import('node:fs/promises');
    const entries = await readdir(dir, { withFileTypes: true });
    if (entries.length !== 1 || !entries[0].isDirectory()) return;
    const nested = join(dir, entries[0].name);
    if (!(await this.pathExists(join(nested, REQUIRED_BINARY)))) return;
    for (const name of await readdir(nested)) {
      await cp(join(nested, name), join(dir, name), { recursive: true });
    }
    await rm(nested, { recursive: true, force: true });
  }

  // ── 内部 ─────────────────────────────────────────────

  private validateInstallInput(input: {
    mode: InstallMode;
    instanceId?: string;
    asset: string;
    preserve?: boolean;
  }): void {
    if (input.mode === 'overwrite' && !input.instanceId) {
      throw new BadRequestException('覆盖安装需要指定 instanceId');
    }
    if (!input.asset) throw new BadRequestException('缺少资源名 asset');
  }

  private setPhase(task: DownloadTask, phase: DownloadPhase, extra: Partial<DownloadTask> = {}): void {
    task.phase = phase;
    Object.assign(task, extra);
    task.updatedAt = new Date().toISOString();
  }

  /** 从 tag 解析版本号（x26.06.19 → x26.06.19；upload 时为空） */
  private tagToVersion(tag: string): string {
    return tag && tag !== 'upload' ? tag : 'unknown';
  }

  private errText(err: unknown): string {
    return err instanceof Error ? err.message : String(err);
  }

  /** 是否可重试：显式标记 retryable，或网络/流错误（无 HTTP 确定性状态） */
  private isRetryable(err: unknown): boolean {
    if (err && typeof err === 'object' && 'retryable' in err) {
      return (err as { retryable?: boolean }).retryable === true;
    }
    return true;
  }

  private async pathExists(path: string): Promise<boolean> {
    try {
      await access(path, constants.F_OK);
      return true;
    } catch {
      return false;
    }
  }

  /** 已存在文件的字节数（不存在则为 0），用于续传起点 */
  private async fileSizeOrZero(path: string): Promise<number> {
    try {
      const s = await stat(path);
      return s.size;
    } catch {
      return 0;
    }
  }

  /** 供门禁/调试：确认暂存目录可用 */
  async ensureTmp(): Promise<void> {
    await mkdir(this.tmpRoot, { recursive: true });
  }
}
