import { BadRequestException, Injectable, Logger } from '@nestjs/common';
import { access, constants, readFile, rename, writeFile } from 'node:fs/promises';
import { join } from 'node:path';
import { Builder, parseStringPromise } from 'xml2js';
import {
  SECRET_PLACEHOLDER,
  type BanConfig,
  type ConfigBundle,
  type ConfigSection,
  type ConfigSectionMeta,
  type LevelConfig,
  type LimitConfig,
  type PasswordConfig,
  type ServerSettingConfig,
} from '@sc-panel/shared';

/** Settings.xml 中的 <Setting Name=".." Value=".." /> 条目 */
interface XmlSettingEntry {
  $?: { Name?: string; Value?: string };
}

interface XmlSettingsDoc {
  Settings?: {
    Setting?: XmlSettingEntry[];
    [key: string]: unknown;
  };
}

/** 面板管理的分区元数据（对齐 plants.md §5.5；effective=当前编译版是否真实消费） */
export const CONFIG_SECTIONS: readonly ConfigSectionMeta[] = [
  {
    key: 'settings',
    label: '面板/服务端设置',
    file: 'Settings.xml',
    effective: true,
    writableWhileRunning: true,
    note: '端口等按键写入，保留未知条目与顺序',
  },
  {
    key: 'serverSetting',
    label: '世界设置',
    file: 'ServerSetting.json',
    effective: true,
    writableWhileRunning: false,
    note: '浅合并已知键，保留未知键',
  },
  {
    key: 'level',
    label: '玩家权限',
    file: 'Configs/LevelConfig.json',
    effective: true,
    writableWhileRunning: false,
  },
  {
    key: 'ban',
    label: '封禁列表',
    file: 'Configs/BanConfig.json',
    effective: true,
    writableWhileRunning: false,
  },
  {
    key: 'limit',
    label: '限制插件',
    file: 'Configs/LimitConfig.json',
    effective: false,
    writableWhileRunning: false,
    note: '编译产物中未发现，可能为源码/遗留文件；写入后需服务端重启且插件已启用才生效',
  },
  {
    key: 'password',
    label: '玩家密码',
    file: 'Plugins/Password.json',
    effective: false,
    writableWhileRunning: false,
    note: '源码插件未编译进当前服务端；面板仅管理文件，启用后需重启生效',
  },
];

export const CONFIG_SECTION_KEYS = CONFIG_SECTIONS.map((s) => s.key);

/**
 * 服务端配置文件读写（plants.md §5.5）。
 *
 * 写回原则：**保留未知字段与顺序**，原子写（.tmp → rename）。
 * - `Settings.xml`：仅按 Name 更新已知键，未知节点/条目原样保留。
 * - `ServerSetting.json`：浅合并已知键，未知键保留。
 * - `Configs/{Level,Ban,Limit}Config.json`：深合并已知键，未知键保留。
 * - `Plugins/Password.json`：深合并；敏感字段以 `SECRET_PLACEHOLDER` 脱敏回显。
 */
@Injectable()
export class ServerConfigService {
  private readonly logger = new Logger('ServerConfig');

  // ── Settings.xml ─────────────────────────────────────

  async readSettingsXml(dir: string): Promise<Record<string, string>> {
    const raw = await readFile(join(dir, 'Settings.xml'), 'utf8');
    const doc = (await parseStringPromise(raw, {
      explicitArray: true,
      preserveChildrenOrder: true,
    })) as XmlSettingsDoc;
    const list = doc.Settings?.Setting ?? [];
    const out: Record<string, string> = {};
    for (const entry of list) {
      const name = entry.$?.Name;
      if (name) out[name] = entry.$?.Value ?? '';
    }
    return out;
  }

  /**
   * 若 Settings.xml 不存在则用给定键创建（服务端首次运行前无该文件；
   * 从版本包安装的新实例需要面板播种默认设置，plants.md §7.3）。
   */
  async ensureSettingsXml(dir: string, defaults: Record<string, string>): Promise<void> {
    const path = join(dir, 'Settings.xml');
    if (await this.fileExists(path)) return;
    const doc: XmlSettingsDoc = {
      Settings: {
        Setting: Object.entries(defaults).map(([Name, Value]) => ({ $: { Name, Value } })),
      },
    };
    await this.atomicWrite(path, new Builder().buildObject(doc));
    this.logger.debug(`Settings.xml 已创建: ${Object.keys(defaults).join(', ')}`);
  }

  /** 若 ServerSetting.json 不存在则用给定对象创建 */
  async ensureServerSetting(dir: string, defaults: Record<string, unknown>): Promise<void> {
    const path = join(dir, 'ServerSetting.json');
    if (await this.fileExists(path)) return;
    await this.atomicWrite(path, JSON.stringify(defaults, null, 2));
    this.logger.debug(`ServerSetting.json 已创建: ${Object.keys(defaults).join(', ')}`);
  }

  /** 仅更新给定的键，保留未知条目/顺序 */
  async updateSettingsXml(dir: string, updates: Record<string, string>): Promise<void> {
    const path = join(dir, 'Settings.xml');
    const raw = await readFile(path, 'utf8');
    const doc = (await parseStringPromise(raw, {
      explicitArray: true,
      preserveChildrenOrder: true,
    })) as XmlSettingsDoc;

    if (!doc.Settings) {
      throw new Error('Settings.xml 结构无效：缺少 <Settings>');
    }
    const list = (doc.Settings.Setting ??= []);
    const seen = new Set<string>();
    for (const entry of list) {
      const name = entry.$?.Name;
      if (name && entry.$ && name in updates) {
        entry.$.Value = updates[name];
        seen.add(name);
      }
    }
    for (const [name, value] of Object.entries(updates)) {
      if (!seen.has(name)) {
        list.push({ $: { Name: name, Value: value } });
      }
    }

    await this.atomicWrite(path, new Builder().buildObject(doc));
    this.logger.debug(`Settings.xml 已更新: ${Object.keys(updates).join(', ')}`);
  }

  // ── ServerSetting.json ───────────────────────────────

  async readServerSetting(dir: string): Promise<Record<string, unknown>> {
    const raw = await readFile(join(dir, 'ServerSetting.json'), 'utf8');
    return JSON.parse(raw) as Record<string, unknown>;
  }

  /** 仅合并给定键，未知键保留 */
  async updateServerSetting(dir: string, updates: Record<string, unknown>): Promise<void> {
    const path = join(dir, 'ServerSetting.json');
    const current = await this.readServerSetting(dir);
    // 提交脱敏占位符表示保持原值
    const patch = Object.fromEntries(
      Object.entries(updates).filter(([, v]) => v !== SECRET_PLACEHOLDER),
    );
    const merged = { ...current, ...patch };
    await this.atomicWrite(path, JSON.stringify(merged, null, 2));
    this.logger.debug(`ServerSetting.json 已更新: ${Object.keys(patch).join(', ')}`);
  }

  // ── JSON 分区（Level / Ban / Limit / Password）────────

  async fileExists(path: string): Promise<boolean> {
    try {
      await access(path, constants.F_OK);
      return true;
    } catch {
      return false;
    }
  }

  private jsonPath(dir: string, section: ConfigSection): string {
    switch (section) {
      case 'level':
        return join(dir, 'Configs', 'LevelConfig.json');
      case 'ban':
        return join(dir, 'Configs', 'BanConfig.json');
      case 'limit':
        return join(dir, 'Configs', 'LimitConfig.json');
      case 'password':
        return join(dir, 'Plugins', 'Password.json');
      default:
        throw new BadRequestException(`分区 ${section} 不是 JSON 分区`);
    }
  }

  /** 读取 JSON 分区；文件不存在返回 null */
  async readJsonSection<T>(dir: string, section: ConfigSection): Promise<T | null> {
    const path = this.jsonPath(dir, section);
    if (!(await this.fileExists(path))) return null;
    const raw = await readFile(path, 'utf8');
    const trimmed = raw.trim();
    if (!trimmed) return null;
    try {
      return JSON.parse(trimmed) as T;
    } catch {
      throw new BadRequestException(`${CONFIG_SECTIONS.find((s) => s.key === section)?.file} 不是合法 JSON`);
    }
  }

  async writeJsonSection(dir: string, section: ConfigSection, value: unknown): Promise<void> {
    const path = this.jsonPath(dir, section);
    await this.atomicWrite(path, JSON.stringify(value, null, 2));
    this.logger.debug(`${section} 已写入 ${path}`);
  }

  // ── 类型化读取（脱敏）────────────────────────────────

  async readServerSettingConfig(dir: string): Promise<ServerSettingConfig | null> {
    if (!(await this.fileExists(join(dir, 'ServerSetting.json')))) return null;
    const raw = await this.readServerSetting(dir);
    return this.maskServerSetting(raw);
  }

  async readLevelConfig(dir: string): Promise<LevelConfig> {
    return (await this.readJsonSection<LevelConfig>(dir, 'level')) ?? {};
  }

  async readBanConfig(dir: string): Promise<BanConfig> {
    const cfg = await this.readJsonSection<BanConfig>(dir, 'ban');
    return cfg ?? { BanUserList: [], BanUserIpList: [], BanIpList: [] };
  }

  async readLimitConfig(dir: string): Promise<LimitConfig | null> {
    return this.readJsonSection<LimitConfig>(dir, 'limit');
  }

  async readPasswordConfig(dir: string): Promise<PasswordConfig | null> {
    const cfg = await this.readJsonSection<PasswordConfig>(dir, 'password');
    if (!cfg) return null;
    return this.maskPassword(cfg);
  }

  /** 汇总实例全部配置（供 GET /instances/:id/config） */
  async readBundle(dir: string, instanceId: string, running: boolean): Promise<ConfigBundle> {
    const [settings, serverSetting, level, ban, limit, password] = await Promise.all([
      (await this.fileExists(join(dir, 'Settings.xml')))
        ? this.readSettingsXml(dir)
        : Promise.resolve({} as Record<string, string>),
      this.readServerSettingConfig(dir),
      this.readLevelConfig(dir),
      this.readBanConfig(dir),
      this.readLimitConfig(dir),
      this.readPasswordConfig(dir),
    ]);
    return {
      instanceId,
      running,
      sections: {
        settings,
        serverSetting: serverSetting ?? {},
        level,
        ban,
        limit,
        password,
      },
      meta: [...CONFIG_SECTIONS],
    };
  }

  // ── 校验 ─────────────────────────────────────────────

  /** 校验分区结构，非法输入抛出 BadRequestException（清晰错误信息） */
  validateSection(section: ConfigSection, payload: unknown): void {
    if (payload === null || typeof payload !== 'object' || Array.isArray(payload)) {
      throw new BadRequestException('配置体必须是 JSON 对象');
    }
    const obj = payload as Record<string, unknown>;
    switch (section) {
      case 'level':
        for (const [name, level] of Object.entries(obj)) {
          if (!name) throw new BadRequestException('玩家名不能为空');
          if (!Number.isInteger(level) || (level as number) < 0) {
            throw new BadRequestException(`玩家 ${name} 的等级必须是非负整数`);
          }
        }
        break;
      case 'ban':
        for (const key of ['BanUserList', 'BanUserIpList', 'BanIpList']) {
          const list = obj[key];
          if (list === undefined) continue;
          if (!Array.isArray(list) || !list.every((v) => typeof v === 'string')) {
            throw new BadRequestException(`${key} 必须是字符串数组`);
          }
        }
        break;
      case 'limit': {
        for (const key of ['WaterLength', 'MagmaLength', 'LimitViewLength']) {
          const v = obj[key];
          if (v === undefined) continue;
          if (!Number.isInteger(v) || (v as number) < 0) {
            throw new BadRequestException(`${key} 必须是非负整数`);
          }
        }
        for (const key of ['LimitBlockBreak', 'LimitExplode', 'LimitView']) {
          const v = obj[key];
          if (v === undefined) continue;
          if (typeof v !== 'boolean') {
            throw new BadRequestException(`${key} 必须是布尔值`);
          }
        }
        break;
      }
      case 'password': {
        const isUse = obj.IsUse;
        if (isUse !== undefined && typeof isUse !== 'boolean') {
          throw new BadRequestException('IsUse 必须是布尔值');
        }
        const def = obj.DefaultPassword;
        if (def !== undefined && typeof def !== 'string') {
          throw new BadRequestException('DefaultPassword 必须是字符串');
        }
        const pp = obj.PlayerPassword;
        if (pp !== undefined) {
          if (pp === null || typeof pp !== 'object' || Array.isArray(pp)) {
            throw new BadRequestException('PlayerPassword 必须是 玩家名→密码 的对象');
          }
          for (const [name, pass] of Object.entries(pp as Record<string, unknown>)) {
            if (!name) throw new BadRequestException('PlayerPassword 的玩家名不能为空');
            if (typeof pass !== 'string') {
              throw new BadRequestException(`玩家 ${name} 的密码必须是字符串`);
            }
          }
        }
        break;
      }
      case 'serverSetting':
        break;
      default:
        break;
    }
  }

  // ── 脱敏 ─────────────────────────────────────────────

  private maskServerSetting(raw: Record<string, unknown>): ServerSettingConfig {
    const masked: Record<string, unknown> = { ...raw };
    if (typeof masked.WorldPassword === 'string' && masked.WorldPassword !== '') {
      masked.WorldPassword = SECRET_PLACEHOLDER;
    }
    return masked as ServerSettingConfig;
  }

  private maskPassword(cfg: PasswordConfig): PasswordConfig {
    const playerPassword = cfg.PlayerPassword;
    const maskedPlayers: Record<string, string> = {};
    if (playerPassword && typeof playerPassword === 'object') {
      for (const name of Object.keys(playerPassword)) maskedPlayers[name] = SECRET_PLACEHOLDER;
    }
    return {
      ...cfg,
      DefaultPassword:
        typeof cfg.DefaultPassword === 'string' && cfg.DefaultPassword !== ''
          ? SECRET_PLACEHOLDER
          : cfg.DefaultPassword,
      PlayerPassword: maskedPlayers,
    };
  }

  /**
   * 合并密码分区：提交 `SECRET_PLACEHOLDER` 的项保持原值。
   * 用于写入前还原脱敏。
   */
  mergePasswordSection(
    incoming: PasswordConfig,
    current: PasswordConfig | null,
  ): PasswordConfig {
    const merged: PasswordConfig = { ...(current ?? {}), ...incoming };
    if (incoming.DefaultPassword === SECRET_PLACEHOLDER) {
      merged.DefaultPassword = current?.DefaultPassword ?? '';
    }
    const playerPassword = incoming.PlayerPassword ?? {};
    const restored: Record<string, string> = {};
    for (const [name, pass] of Object.entries(playerPassword)) {
      restored[name] =
        pass === SECRET_PLACEHOLDER ? (current?.PlayerPassword?.[name] ?? '') : pass;
    }
    merged.PlayerPassword = restored;
    return merged;
  }

  private async atomicWrite(path: string, content: string): Promise<void> {
    const tmp = `${path}.tmp`;
    await writeFile(tmp, content, 'utf8');
    await rename(tmp, path);
  }
}
