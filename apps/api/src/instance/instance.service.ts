import {
  BadRequestException,
  ConflictException,
  Inject,
  Injectable,
  Logger,
  NotFoundException,
  OnModuleInit,
} from '@nestjs/common';
import { createServer } from 'node:net';
import { randomUUID } from 'node:crypto';
import { access, cp, mkdir, readdir, rm } from 'node:fs/promises';
import { constants } from 'node:fs';
import { join } from 'node:path';
import AdmZip from 'adm-zip';
import { extract as tarExtract } from 'tar';
import type {
  ConfigSection,
  CreateInstanceInput,
  Instance,
  InstanceMeta,
  PasswordConfig,
  TemplateInfo,
  UpdateInstanceInput,
} from '@sc-panel/shared';
import type { PanelEnv } from '../config/env';
import { PANEL_ENV } from '../config/env.module';
import { toAbsolute } from '../common/utils/paths';
import {
  CONFIG_SECTIONS,
  ServerConfigService,
} from '../server-config/server-config.service';
import { InstanceStore } from './instance.store';
import { InstanceSupervisor } from './instance.supervisor';

const DEFAULT_PORT_START = 28887;
const PORT_SCAN_RANGE = 2000;
const REQUIRED_BINARY = 'Survivalcraft.dll';
/** 覆盖安装保留的 pre-install 快照上限（M6 遗留清理，M7） */
const PRE_INSTALL_KEEP = 3;

@Injectable()
export class InstanceService implements OnModuleInit {
  private readonly logger = new Logger('InstanceService');
  private readonly dataRoot: string;
  private readonly instancesRoot: string;
  private readonly templateDir: string;

  constructor(
    @Inject(PANEL_ENV) env: PanelEnv,
    private readonly store: InstanceStore,
    private readonly supervisor: InstanceSupervisor,
    private readonly serverConfig: ServerConfigService,
  ) {
    this.dataRoot = toAbsolute(env.PANEL_DATA_DIR);
    this.instancesRoot = join(this.dataRoot, 'instances');
    this.templateDir = toAbsolute(env.SERVER_TEMPLATE_DIR);
  }

  async onModuleInit(): Promise<void> {
    await this.store.init();
    const metas = await this.store.list();
    for (const meta of metas) this.supervisor.register(meta);
    this.logger.log(`已加载 ${metas.length} 个实例（均为 stopped）`);
  }

  async getTemplateInfo(): Promise<TemplateInfo> {
    const exists = await this.pathExists(this.templateDir);
    const hasBinary = exists && (await this.pathExists(join(this.templateDir, REQUIRED_BINARY)));
    let version: string | undefined;
    if (hasBinary) {
      try {
        const settings = await this.serverConfig.readSettingsXml(this.templateDir);
        version = settings.LastLaunchedVersion;
      } catch {
        version = undefined;
      }
    }
    return { dir: this.templateDir, exists, hasServerBinary: hasBinary, version };
  }

  async list(): Promise<Instance[]> {
    const metas = await this.store.list();
    return metas.map((meta) => this.toInstance(meta));
  }

  async overview() {
    const instances = await this.list();
    return {
      instances,
      total: instances.length,
      running: instances.filter((i) => i.runtime.status === 'running').length,
    };
  }

  async get(id: string): Promise<Instance> {
    const meta = await this.store.read(id);
    if (!meta) throw new NotFoundException(`实例不存在: ${id}`);
    return this.toInstance(meta);
  }

  private toInstance(meta: InstanceMeta): Instance {
    return { ...meta, runtime: this.supervisor.getRuntime(meta.id) ?? { status: 'stopped' } };
  }

  /**
   * 创建独立实例目录（plants.md §5.2 / §7.3）。
   * - template：从模板目录递归拷贝服务端本体；
   * - upload：留空目录，由 uploadBasePackage 解压；
   * - import：直接引用已有目录（要求含 Survivalcraft.dll）。
   */
  async create(input: CreateInstanceInput): Promise<Instance> {
    const id = randomUUID();
    const source = input.source ?? 'template';
    const dir = input.dir ? toAbsolute(input.dir) : join(this.instancesRoot, id);

    if (await this.store.read(id)) throw new ConflictException('实例 ID 冲突，请重试');
    if (input.dir) {
      const existing = await this.store.list();
      if (existing.some((x) => toAbsolute(x.dir) === dir)) {
        throw new ConflictException(`目录已被其它实例占用: ${dir}`);
      }
    }

    await mkdir(dir, { recursive: true });

    if (source === 'template') {
      const template = input.templateDir ? toAbsolute(input.templateDir) : this.templateDir;
      await this.copyTemplate(template, dir);
    } else if (source === 'import') {
      await this.assertServerBinary(dir);
    }

    // 读取模板的服务端版本（若存在）
    let version = input.version;
    if (!version) {
      const info = await this.getTemplateInfo();
      version = source === 'template' ? (info.version ?? 'unknown') : 'unknown';
    }

    // 端口分配：尊重显式指定，否则选空闲端口
    const usedPorts = new Set((await this.store.list()).map((m) => m.serverPort));
    const serverPort = input.serverPort ?? (await this.allocatePort(usedPorts));

    const meta: InstanceMeta = {
      id,
      name: input.name,
      source,
      dir,
      serverPort,
      broadcastPort: input.broadcastPort,
      version: version ?? 'unknown',
      launchArgs: input.launchArgs ?? ['--sckey-token-local', '--enhanced'],
      worldPath: 'app:/Worlds/World',
      worldName: input.worldName ?? 'ScWorld',
      maxPlayers: input.maxPlayers ?? 20,
      autoRestart: input.autoRestart ?? false,
      autoStart: input.autoStart ?? false,
      // 面板托管实例默认无人值守开服（plants.md §0.2.1 / §11-Q4）：
      // 置 Autorun=true 让服务端启动后直接进世界，而非停留在 Play 界面等待输入。
      autoRun: input.autoRun ?? true,
      createdAt: new Date().toISOString(),
    };

    // 写回端口/世界参数到实际配置文件
    await this.applyMetaToConfig(meta);

    await this.store.write(meta);
    this.supervisor.register(meta);
    this.logger.log(`实例已创建 id=${id} name=${meta.name} port=${serverPort} dir=${dir}`);
    return this.toInstance(meta);
  }

  /** 上传基础包并解压到实例目录（source='upload'） */
  async installFromUpload(id: string, zipBuffer: Buffer): Promise<void> {
    const meta = await this.store.read(id);
    if (!meta) throw new NotFoundException(`实例不存在: ${id}`);
    if (meta.source !== 'upload') {
      throw new BadRequestException('仅 source=upload 的实例可上传基础包');
    }
    await mkdir(meta.dir, { recursive: true });
    const zip = new AdmZip(zipBuffer);
    zip.extractAllTo(meta.dir, /* overwrite */ true);
    await this.normalizeExtracted(meta.dir);
    await this.assertServerBinary(meta.dir);
    await this.applyMetaToConfig(meta);
    this.logger.log(`[${id}] 基础包已解压到 ${meta.dir}`);
  }

  /**
   * 版本安装后创建/注册实例（plants.md §7.3）。
   * 归档已由 VersionService 解压到 `dir`，这里只负责元数据、端口与配置写回。
   */
  async createFromVersion(input: {
    name: string;
    dir: string;
    version: string;
    source?: 'version';
    serverPort?: number;
    maxPlayers?: number;
    autoRun?: boolean;
    autoRestart?: boolean;
  }): Promise<Instance> {
    return this.create({
      name: input.name,
      dir: input.dir,
      source: 'version',
      version: input.version,
      serverPort: input.serverPort,
      maxPlayers: input.maxPlayers,
      autoRun: input.autoRun,
      autoRestart: input.autoRestart,
    });
  }

  /**
   * 把已解压/已下载的版本包安装到既有实例目录（覆盖安装）。
   * 默认保留既有运行期数据：Configs/Worlds/Plugins/Backups 与运行日志。
   * 先整目录快照到 `<实例>/backups/pre-install-<ts>/`，失败时可回滚。
   */
  async overwriteInstall(
    id: string,
    archivePath: string,
    opts: { preserve?: boolean; kind?: 'zip' | 'tar.gz'; version?: string } = {},
  ): Promise<Instance> {
    const meta = await this.store.read(id);
    if (!meta) throw new NotFoundException(`实例不存在: ${id}`);
    if (this.supervisor.isAlive(id)) {
      throw new ConflictException('实例正在运行，请先停止后再覆盖安装');
    }

    const preserve = opts.preserve ?? true;
    const stageDir = join(meta.dir, `.install-stage-${Date.now()}`);
    const snapshotDir = join(meta.dir, 'backups', `pre-install-${Date.now()}`);
    await mkdir(stageDir, { recursive: true });
    await mkdir(snapshotDir, { recursive: true });

    const protectedEntries = preserve
      ? ['Configs', 'Worlds', 'Plugins', 'backups', 'Bugs']
      : ['backups'];

    try {
      // 1) 整目录快照（回滚用）
      for (const name of await readdir(meta.dir, { withFileTypes: true })) {
        if (name.name === stageDir.split(/[\\/]/).pop()) continue;
        if (name.name === 'backups') continue;
        await cp(join(meta.dir, name.name), join(snapshotDir, name.name), { recursive: true });
      }

      // 2) 解压到暂存区
      await this.extractArchive(archivePath, stageDir, opts.kind);
      await this.normalizeExtracted(stageDir);
      await this.assertServerBinary(stageDir);

      // 3) 覆盖：删除包内条目对应的旧文件，但跳过受保护目录
      const incoming = await readdir(stageDir, { withFileTypes: true });
      for (const entry of incoming) {
        if (protectedEntries.includes(entry.name)) {
          await this.mergeInto(join(stageDir, entry.name), join(meta.dir, entry.name));
        } else {
          await rm(join(meta.dir, entry.name), { recursive: true, force: true });
          await cp(join(stageDir, entry.name), join(meta.dir, entry.name), { recursive: true });
        }
      }

      if (opts.version) meta.version = opts.version;
      await this.applyMetaToConfig(meta);
      await this.store.write(meta);
      this.supervisor.updateMeta(meta);
      // 覆盖安装成功后清理旧 pre-install 快照，仅保留最近 N 份（M6 遗留项，M7）
      await this.prunePreInstallSnapshots(meta.dir).catch((err) =>
        this.logger.warn(`[${id}] 清理 pre-install 快照失败: ${String(err)}`),
      );
      this.logger.log(`[${id}] 覆盖安装完成 version=${meta.version} preserve=${preserve}`);
      return this.toInstance(meta);
    } finally {
      await rm(stageDir, { recursive: true, force: true });
    }
  }

  /**
   * 覆盖安装快照清理：`<实例>/backups/pre-install-<ts>/` 仅保留最近 `PRE_INSTALL_KEEP` 份，
   * 避免反复覆盖安装导致磁盘膨胀（M6 报告 §7 遗留项）。
   */
  private async prunePreInstallSnapshots(dir: string): Promise<void> {
    const backupsRoot = join(dir, 'backups');
    if (!(await this.pathExists(backupsRoot))) return;
    const entries = await readdir(backupsRoot, { withFileTypes: true });
    const snaps = entries
      .filter((e) => e.isDirectory() && e.name.startsWith('pre-install-'))
      .map((e) => e.name)
      .sort();
    const overflow = snaps.length - PRE_INSTALL_KEEP;
    for (let i = 0; i < overflow; i++) {
      await rm(join(backupsRoot, snaps[i]), { recursive: true, force: true });
      this.logger.log(`[prune] 已删除旧覆盖安装快照 ${snaps[i]}`);
    }
  }

  /** 把 source 目录内容合并进 target（同名覆盖），用于保留配置/存档目录 */
  private async mergeInto(source: string, target: string): Promise<void> {
    if (!(await this.pathExists(source))) return;
    await mkdir(target, { recursive: true });
    for (const entry of await readdir(source, { withFileTypes: true })) {
      const from = join(source, entry.name);
      const to = join(target, entry.name);
      if (entry.isDirectory()) {
        await this.mergeInto(from, to);
      } else {
        await cp(from, to, { recursive: true });
      }
    }
  }

  /** 解压 zip 或 tar.gz 到目标目录 */
  private async extractArchive(
    archivePath: string,
    destDir: string,
    kind?: 'zip' | 'tar.gz',
  ): Promise<void> {
    if (kind === 'tar.gz' || (!kind && archivePath.toLowerCase().endsWith('.tar.gz'))) {
      await tarExtract({ file: archivePath, cwd: destDir, portable: true });
    } else {
      new AdmZip(archivePath).extractAllTo(destDir, true);
    }
  }

  async update(id: string, input: UpdateInstanceInput): Promise<Instance> {
    const meta = await this.store.read(id);
    if (!meta) throw new NotFoundException(`实例不存在: ${id}`);

    // 只应用显式提供的字段（DTO 未提供的键为 undefined，不能覆盖已有值）
    const patch = Object.fromEntries(
      Object.entries(input).filter(([, v]) => v !== undefined),
    ) as UpdateInstanceInput;

    // 从 patch 中拆出仅写入 ServerSetting.json 的世界参数（不落 meta）
    const worldOnly = patch as Record<string, unknown>;
    const {
      autoGenerateWorld,
      worldSeed,
      gameMode,
      pvpEnabled,
      seasonChanging,
      worldPassword,
      ...metaPatch
    } = worldOnly;

    const serverSettingExtra: Record<string, unknown> = {};
    if (autoGenerateWorld !== undefined) serverSettingExtra.AutoGenerateWorld = autoGenerateWorld;
    if (worldSeed !== undefined) serverSettingExtra.WorldSeed = worldSeed;
    if (gameMode !== undefined) serverSettingExtra.GameMode = gameMode;
    if (pvpEnabled !== undefined) serverSettingExtra.PVPEnabled = pvpEnabled;
    if (seasonChanging !== undefined) serverSettingExtra.SeasonChanging = seasonChanging;
    if (worldPassword !== undefined) serverSettingExtra.WorldPassword = worldPassword;

    const merged: InstanceMeta = { ...meta, ...(metaPatch as UpdateInstanceInput) };
    await this.applyMetaToConfig(merged, serverSettingExtra);
    await this.store.write(merged);
    this.supervisor.updateMeta(merged);
    return this.toInstance(merged);
  }

  async remove(id: string, deleteFiles = false): Promise<void> {
    const meta = await this.store.read(id);
    if (!meta) throw new NotFoundException(`实例不存在: ${id}`);
    if (this.supervisor.isAlive(id)) {
      throw new ConflictException('实例正在运行，请先停止');
    }
    await this.supervisor.stop(id);
    this.supervisor.unregister(id);
    await this.store.remove(id);
    if (deleteFiles && meta.dir.startsWith(this.instancesRoot)) {
      await rm(meta.dir, { recursive: true, force: true });
    }
  }

  /** 读取实例的完整配置快照（脱敏，供配置页，plants.md §5.5） */
  async readConfig(id: string) {
    const meta = await this.store.read(id);
    if (!meta) throw new NotFoundException(`实例不存在: ${id}`);
    const running = this.supervisor.getRuntime(id)?.status === 'running';
    return this.serverConfig.readBundle(meta.dir, id, running);
  }

  /** 写入某个配置分区（保留未知字段/条目，原子写） */
  async writeSection(id: string, section: ConfigSection, payload: unknown): Promise<void> {
    const meta = await this.store.read(id);
    if (!meta) throw new NotFoundException(`实例不存在: ${id}`);
    const sectionMeta = CONFIG_SECTIONS.find((s) => s.key === section);
    if (!sectionMeta) throw new BadRequestException(`不支持的分区: ${section}`);

    const running = this.supervisor.getRuntime(id)?.status === 'running';
    if (running && !sectionMeta.writableWhileRunning) {
      throw new ConflictException(
        `${sectionMeta.label}（${sectionMeta.file}）在实例运行中不可修改，请先停止实例`,
      );
    }

    this.serverConfig.validateSection(section, payload);

    switch (section) {
      case 'settings': {
        const updates = this.pickStringEntries(payload as Record<string, unknown>);
        await this.serverConfig.updateSettingsXml(meta.dir, updates);
        break;
      }
      case 'serverSetting':
        await this.serverConfig.updateServerSetting(meta.dir, payload as Record<string, unknown>);
        this.syncMetaFromServerSetting(meta, payload as Record<string, unknown>);
        await this.applyMetaToConfig(meta);
        await this.store.write(meta);
        this.supervisor.updateMeta(meta);
        break;
      case 'level': {
        const current = await this.serverConfig.readLevelConfig(meta.dir);
        await this.serverConfig.writeJsonSection(meta.dir, 'level', {
          ...current,
          ...(payload as Record<string, unknown>),
        });
        break;
      }
      case 'ban': {
        const current = await this.serverConfig.readBanConfig(meta.dir);
        await this.serverConfig.writeJsonSection(meta.dir, 'ban', {
          ...current,
          ...(payload as Record<string, unknown>),
        });
        break;
      }
      case 'limit': {
        const current = await this.serverConfig.readLimitConfig(meta.dir);
        await this.serverConfig.writeJsonSection(meta.dir, 'limit', {
          ...(current ?? {}),
          ...(payload as Record<string, unknown>),
        });
        break;
      }
      case 'password': {
        const current = await this.serverConfig.readJsonSection<PasswordConfig>(
          meta.dir,
          'password',
        );
        const merged = this.serverConfig.mergePasswordSection(
          payload as PasswordConfig,
          current,
        );
        await this.serverConfig.writeJsonSection(meta.dir, 'password', merged);
        break;
      }
      default:
        throw new BadRequestException(`不支持的分区: ${section}`);
    }
    this.logger.log(`[${id}] 配置分区 ${section} 已写入`);
  }

  private pickStringEntries(obj: Record<string, unknown>): Record<string, string> {
    const out: Record<string, string> = {};
    for (const [k, v] of Object.entries(obj)) {
      if (v === undefined || v === null) continue;
      out[k] = String(v);
    }
    return out;
  }

  private syncMetaFromServerSetting(meta: InstanceMeta, payload: Record<string, unknown>): void {
    if (typeof payload.WorldName === 'string') meta.worldName = payload.WorldName;
    if (typeof payload.WorldMaxPlayers === 'number') meta.maxPlayers = payload.WorldMaxPlayers;
    if (typeof payload.Autorun === 'boolean') meta.autoRun = payload.Autorun;
    if (typeof payload.WorldPath === 'string') meta.worldPath = payload.WorldPath;
  }

  // ── 内部 ─────────────────────────────────────────────

  private async applyMetaToConfig(
    meta: InstanceMeta,
    extraServerSetting: Record<string, unknown> = {},
  ): Promise<void> {
    const settingsUpdates: Record<string, string> = { ServerPort: String(meta.serverPort) };
    const serverSettingUpdates: Record<string, unknown> = {
      WorldMaxPlayers: meta.maxPlayers,
      WorldName: meta.worldName,
      Autorun: meta.autoRun,
      WorldPath: meta.worldPath,
      GameMode: 1,
      PVPEnabled: true,
      SeasonChanging: true,
      ...extraServerSetting,
    };

    // 从版本包/上传包安装的实例首次运行前可能没有这两个文件：先播种默认值。
    // 关键：全新包没有世界存档（Worlds/），需 AutoGenerateWorld=true 让服务端首启建图，
    // 否则 Autorun=true 会因“自动运行存档不存在”直接退出（实测）。
    const worldDir = join(meta.dir, 'Worlds', this.worldNameFromPath(meta.worldPath));
    const hasWorld = await this.pathExists(worldDir);
    await this.serverConfig.ensureSettingsXml(meta.dir, { ServerPort: '28887' });
    await this.serverConfig.ensureServerSetting(meta.dir, {
      AutoGenerateWorld: !hasWorld,
      ...serverSettingUpdates,
    });

    await this.serverConfig.updateSettingsXml(meta.dir, settingsUpdates);
    if (await this.pathExists(join(meta.dir, 'ServerSetting.json'))) {
      await this.serverConfig.updateServerSetting(meta.dir, serverSettingUpdates);
    }
  }

  /** 从 WorldPath（app:/Worlds/World）解析世界名 */
  private worldNameFromPath(worldPath: string): string {
    const normalized = (worldPath ?? '').replace(/^app:\/?/i, '');
    const parts = normalized.split(/[\\/]/).filter(Boolean);
    const idx = parts.findIndex((p) => p.toLowerCase() === 'worlds');
    if (idx >= 0 && parts[idx + 1]) return parts[idx + 1];
    return 'World';
  }

  private async copyTemplate(template: string, dir: string): Promise<void> {
    if (!(await this.pathExists(join(template, REQUIRED_BINARY)))) {
      throw new BadRequestException(
        `模板目录缺少 ${REQUIRED_BINARY}: ${template}（可用 SERVER_TEMPLATE_DIR 指定或改用上传）`,
      );
    }
    await cp(template, dir, { recursive: true });

    // 清理运行期产物，避免新实例继承旧日志/临时文件
    await rm(join(dir, 'Bugs', 'Game.log'), { force: true });
  }

  /** 解压后若压缩包是单层目录包裹，则把内容上移一层 */
  private async normalizeExtracted(dir: string): Promise<void> {
    if (await this.pathExists(join(dir, REQUIRED_BINARY))) return;
    const entries = await readdir(dir, { withFileTypes: true });
    if (entries.length !== 1 || !entries[0].isDirectory()) return;
    const nested = join(dir, entries[0].name);
    if (!(await this.pathExists(join(nested, REQUIRED_BINARY)))) return;
    const inner = await readdir(nested);
    for (const name of inner) {
      await cp(join(nested, name), join(dir, name), { recursive: true });
    }
    await rm(nested, { recursive: true, force: true });
  }

  private async assertServerBinary(dir: string): Promise<void> {
    if (!(await this.pathExists(join(dir, REQUIRED_BINARY)))) {
      throw new BadRequestException(`实例目录缺少 ${REQUIRED_BINARY}: ${dir}`);
    }
  }

  private async pathExists(path: string): Promise<boolean> {
    try {
      await access(path, constants.F_OK);
      return true;
    } catch {
      return false;
    }
  }

  private async allocatePort(used: Set<number>): Promise<number> {
    for (let port = DEFAULT_PORT_START; port < DEFAULT_PORT_START + PORT_SCAN_RANGE; port++) {
      if (used.has(port)) continue;
      if (await this.isPortFree(port)) return port;
    }
    throw new ConflictException('无可用端口');
  }

  private isPortFree(port: number): Promise<boolean> {
    return new Promise((resolve) => {
      const server = createServer();
      server.once('error', () => resolve(false));
      server.once('listening', () => server.close(() => resolve(true)));
      server.listen(port, '0.0.0.0');
    });
  }
}
