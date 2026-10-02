# 架构设计

## 1. 总览

SC-Panel 是单管理员、单节点多实例的 SurvivalcraftNet 服务端管理面板。

```
┌──────────────────────────────────────────────────────────────┐
│  浏览器 (Nuxt 4 SPA, ssr:false)                                 │
│  fuxsto-design + Tailwind v4 + Pinia + xterm.js + WS 客户端      │
└───────────────▲───────────────────────────┬──────────────────┘
                │ REST (JWT HttpOnly Cookie) │ WebSocket (/ws/console)
                │                            ▼
┌───────────────┴──────────────────────────────────────────────┐
│  NestJS 后端 (apps/api)                                        │
│  ├─ auth          : 单管理员 JWT / Cookie，全局 JwtAuthGuard     │
│  ├─ instance      : CRUD / 建实例 / 端口分配 / 配置播种           │
│  │  └─ supervisor : 状态机 / 崩溃识别 / 自动重启 / FileTailer     │
│  ├─ adapter       : IServerAdapter + PtyAdapter(node-pty)       │
│  ├─ console       : WS 网关 + 回滚缓冲 + 历史接口                │
│  ├─ server-config : Settings.xml / ServerSetting.json 读写       │
│  ├─ probe         : UDP ServerInfo 探测 + 指标调度               │
│  ├─ system        : 系统 + 进程指标采样                          │
│  ├─ backup        : 存档 / tar.gz 备份 / 恢复 / 定时 / 保留       │
│  ├─ version       : Gitee releases / 流式下载 / 解压安装          │
│  ├─ audit         : 审计（JSONL 追加 / 按天轮转 / 查询）          │
│  ├─ settings      : 面板设置（备份策略/镜像）+ 修改密码           │
│  └─ common        : 日志 / 异常过滤器 / 工具                     │
└───────────────┬──────────────────────────────────────────────┘
                │ node-pty (伪终端 / ConPTY)
                ▼
┌──────────────────────────────────────────────────────────────┐
│  实例 N: 工作目录/ (Survivalcraft.dll, Settings.xml,            │
│          ServerSetting.json, Configs/, Worlds/, Bugs/Game.log) │
└──────────────────────────────────────────────────────────────┘
```

## 2. 仓库结构

```
scsm/
├─ apps/
│  ├─ web/                       # Nuxt 4 SPA 前端
│  │  └─ app/
│  │     ├─ components/console/  # TerminalView（xterm.js）
│  │     ├─ components/instance/ # 卡片 / 状态灯 / 创建对话框 / 配置 / 备份面板
│  │     ├─ components/version/  # VersionsPanel（版本下载/安装）
│  │     ├─ components/ui/       # AppDialog（替代 fuxsto Dialog）
│  │     ├─ composables/         # useApi / useConsoleSocket / useSystemStats / useSettings
│  │     ├─ pages/               # login / index / instances/[id] / versions / audit / settings
│  │     ├─ stores/              # Pinia（auth / instances）
│  │     └─ middleware/auth.global.ts
│  └─ api/                       # NestJS 后端
│     ├─ src/<module>/           # 见上架构图
│     ├─ scripts/mN-gate.cjs     # M2–M7 真实门禁脚本
│     └─ test/                   # Vitest（单元 + 集成）
├─ packages/
│  └─ shared/                    # 前后端共享类型（ESM + CJS 双产物）
├─ docs/                         # 架构 / 接口 / 开发 / 部署 / 测试报告
├─ THIRD_PARTY_LICENSES/         # 第三方许可文本
├─ data/                         # 运行期数据（实例元数据 / 日志 / 审计 / 设置）
├─ plants.md                     # 开发计划与测试门禁
└─ pnpm-workspace.yaml
```

## 3. 后端模块

| 模块 | 职责 | 关键文件 |
|------|------|----------|
| `auth` | 登录/登出/me、JWT 签发与校验、全局守卫、运行期改密 | `auth.service.ts`、`jwt-auth.guard.ts` |
| `instance` | 实例 CRUD、模板拷贝/zip 上传/目录导入/版本安装、端口分配、配置播种、生命周期 | `instance.service.ts`、`instance.controller.ts` |
| `instance.supervisor` | 进程状态机、崩溃识别、指数退避自动重启、PTY 读写/resize | `instance.supervisor.ts` |
| `instance.config` | 配置快照读取与分区写入 | `config.controller.ts` |
| `adapter` | `IServerAdapter` 接口 + `PtyAdapter`（node-pty）、终端工具 | `pty.adapter.ts`、`terminal-utils.ts` |
| `console` | WS 网关、订阅扇出、回滚缓冲、历史接口、命令限速 | `console.gateway.ts`、`rollback-buffer.ts` |
| `server-config` | `Settings.xml` / `ServerSetting.json` / `Configs/*.json` 读写（保留未知字段/原子写） | `server-config.service.ts` |
| `probe` | UDP ServerInfo 编解码、单播探测、周期指标调度 | `server-info.protocol.ts`、`udp-probe.ts`、`metrics.service.ts` |
| `system` | 主机指标（os/statfs）+ 进程指标（wmic/PowerShell、/proc） | `system.service.ts`、`process-sampler.ts` |
| `backup` | 列存档、tar.gz 备份、恢复+预快照、删除、保留淘汰、定时任务 | `backup.service.ts`、`archive.ts` |
| `version` | Gitee releases 拉取/缓存/降级、流式下载、解压安装、上传兜底 | `version.service.ts`、`download.service.ts` |
| `audit` | JSONL 追加、按天轮转、过滤查询 | `audit.service.ts` |
| `settings` | 面板设置运行期可调 + 改密 | `settings.service.ts` |
| `common` | pino 日志、统一异常过滤器、路径/可执行文件工具 | `filters/`、`logger/`、`utils/` |

### 3.1 模块注册顺序（重要）

`app.module.ts` 中模块顺序影响路由匹配：

```
EnvModule → ThrottlerModule → ServerConfigModule → AuditModule → SettingsModule
→ AuthModule → BackupModule → VersionModule → InstanceModule → ConsoleModule
→ ProbeModule → HealthModule
```

`BackupModule` / `VersionModule` 必须**先于** `InstanceModule` 注册，否则
`InstanceController` 的 `@Post(':id/:action')` 会吞掉 `/instances/:id/backups` 等子路由。

## 4. 数据模型

### 4.1 Instance（实例）

实例 = 一个独立工作目录 + 一份元数据（JSON）+ 独占端口。

```ts
interface Instance {
  id: string;                 // uuid
  name: string;               // 显示名
  source: 'template' | 'upload' | 'import' | 'version';
  dir: string;                // 工作目录（含 Survivalcraft.dll / Settings.xml / ServerSetting.json）
  serverPort: number;         // Settings.xml 的 ServerPort
  broadcastPort?: number;     // 广播端口（编译产物中不存在，保留字段）
  version: string;            // 如 2.4.0.0-ljx26.06.19
  launchArgs: string[];       // e.g. ["--sckey-token-local", "--enhanced"]
  worldPath: string;          // ServerSetting.json.WorldPath（app:/Worlds/World）
  worldName: string;
  maxPlayers: number;
  autoRestart: boolean;
  autoStart: boolean;
  autoRun: boolean;           // ServerSetting.json.Autorun（无人值守开服）
  createdAt: string;
  // 运行时
  status: 'stopped' | 'starting' | 'running' | 'stopping' | 'crashed';
  pid?: number;
  online?: number; maxOnline?: number; gameMode?: string; hasPassword?: boolean;
  cpu?: number; memMB?: number;
  startedAt?: string; uptimeMs?: number;
}
```

### 4.2 运行期数据目录

```
data/                              # PANEL_DATA_DIR
├─ instances/<id>/meta.json        # 实例元数据（原子写）
├─ versions/releases-cache-*.json  # releases 缓存
├─ audit/YYYY-MM-DD.jsonl          # 审计日志（按天轮转）
├─ admin-password.json             # 运行期修改的管理员密码哈希
└─ panel-settings.json             # 面板设置
```

实例工作目录结构（服务端运行时生成）：

```
＜实例工作目录＞/
├─ Survivalcraft.dll
├─ Settings.xml            # 全局设置（含 ServerPort）
├─ ServerSetting.json      # 世界参数
├─ Configs/{Level,Ban,Limit}Config.json
├─ Plugins/Password.json
├─ Worlds/<name>/          # 存档
├─ Bugs/Game.log           # 运行日志（FileTailer 增量消费）
└─ backups/                # 面板侧备份归档 <uuid>.tar.gz + <uuid>.json
```

## 5. 关键设计决策

### 5.1 PTY 硬性要求
游戏主进程**必须**经 `node-pty`（Windows ConPTY / Linux pty）运行，保留真实 TTY：ANSI
转义、增强控制台热键/表格、行缓冲、`Ctrl+C`。**禁止**用 `child_process.spawn` 管道替代。

> Windows 上 `node-pty.spawn` 不接受裸命令名，需先经 `resolveExecutable` 解析绝对路径
> （如 `dotnet.EXE`）。

### 5.2 raw / line 双通道
- `console.raw`：原样 ANSI 字节流，供 xterm.js 渲染。
- `console.line`：清洗后纯文本行，供日志/检索/事件匹配。
- PTY `stdout` + `tail Bugs/Game.log`（`FileTailer` 增量字节偏移）双路消费，覆盖不同 `LogMode`。

### 5.3 无人值守开服
面板托管实例创建时 `ServerSetting.json.Autorun=true`（`autoRun` 默认 `true`）。否则服务端
停留在 `Entered screen "Play"`，不打印 `开启服务器成功，端口 ...`，状态机无法进入 `running`。

### 5.4 端口与探测协议
- 端口在 `Settings.xml` 的 `ServerPort`（编译产物中**无** `BroadcastPort`）。
- UDP 探测向实例 `ServerPort` **单播** `[0x09][rawDeflate(0x88,0x00,0x01)]`，解析回执
  `version/人数/上限/游戏模式/密码/世界时间`。`0x09` 是 LiteNetLib `UnconnectedMessage` property 字节。
- 探测回执 `maxCount` 来自世界数据 `WorldSettings.MaxOnlinePlayerCount`，
  `ServerSetting.WorldMaxPlayers` 仅在**新建世界**时播种。

### 5.5 配置写回
- `Settings.xml`：按 `Name`/`Value` 读写，保留未知条目与顺序，原子写。
- `ServerSetting.json`：浅合并世界参数，保留未知键。
- 敏感字段（`WorldPassword`/`DefaultPassword`/`PlayerPassword`）以 `********` 脱敏回显。
- 运行中写保护：世界/权限/封禁/密码/限制分区返回 `409`；`Settings.xml` 允许热改。

### 5.6 编译产物文件口径（实测）
服务端 `Survivalcraft.dll` 中**真实生效**：`Settings.xml`、`ServerSetting.json`、
`Configs/{Level,Ban}Config.json`。
`Configs/LimitConfig.json` 与 `Plugins/Password.json` **未编译进服务端**，面板仍管理并标注
「源码/遗留」（`effective=false`）。

### 5.7 备份与恢复
- 归档格式 **tar.gz 流式**，范围 = **世界目录 + 配置**（`Worlds/`、`Settings.xml`、
  `ServerSetting.json`、`Configs/`、`Plugins/`）。
- 恢复流程：**停止实例 → 生成 `pre-restore` 安全快照 → 解压覆盖**。
- 运行中禁止备份（一致性，返回 `409`）。
- 保留策略 `BACKUP_KEEP` 按时间从旧到新淘汰，`pre-restore` 优先保留。

### 5.8 版本安装
- Gitee releases 拉取（缓存 + 失败降级到本地缓存）。
- 服务端包识别：排除 `[电脑版]/[安卓版]/.apk/源码归档`，按 `serverPackageScore` 优先托管式
  `Survivalcraft.dll` 包（避免 `PocketSurvival`/`PSTerminal.exe` 变体）。
- 流式下载跟随 Gitee 302、带 Referer/UA、进度/速度/ETA、体积上限、**Range 断点续传 + 重试**。
- 安装播种：`ServerPort` + `Autorun=true`；世界目录不存在时置 `AutoGenerateWorld=true`
  （否则服务端因「自动运行存档不存在」退出）。
- `mode=overwrite` 覆盖既有实例并保留 `Configs/Worlds/Plugins/备份`。

### 5.9 安全
- 单管理员 JWT + HttpOnly/SameSite=Lax Cookie，全局 `JwtAuthGuard`（`@Public()` 放行登录/健康检查）。
- 限流：全局 300/分；登录 5/分；下载/上传 10/分；实例操作 30/分；WS 命令按连接 10s/40 条。
- 路径遍历防护：所有文件操作基于 `instance.dir` 做 `path.resolve` 前缀校验。
- 命令仅经 PTY 写入（不经 shell）。
- 审计：登录/启停/命令/配置/备份/下载/安装/设置，JSONL 按天轮转。

### 5.10 前端要点
- `ssr:false` SPA；`/api` 与 `/ws` 经 Nitro `routeRules.proxy` 代理后端（dev）。
- 终端优先**直连后端 origin**（WS 升级不被 Nitro 处理，同源回退）。
- `fuxsto-design@1.0.4` 的 `Dialog` 正文不渲染，已用自研 `AppDialog`/`dialog.ts` 替代（API 对齐）。
- xterm.js 容器 ResizeObserver → fit → `terminal.resize`。

## 6. 进程状态机

```
stopped ──start──▶ starting ──成功特征行──▶ running
                       │                      │
                       │ 超时/失败            │ stop
                       ▼                      ▼
                    crashed ◀──非预期退出   stopping ──▶ stopped
                       │
                       └──autoRestart（指数退避 5s→60s）──▶ starting
```

- 正常停止 → `stopped` 与崩溃 → `crashed` 通过 `intentionalStop` 区分。
- 停止优先经 PTY 发 `/stop`，超时（`STOP_GRACE_MS`，默认 15s）后强杀。

## 7. 共享类型包

`packages/shared` 输出 **ESM + CJS 双产物**（CJS 供 NestJS 运行时 `require`），按域拆分：
`instance` / `ws` / `api` / `auth` / `probe` / `config` / `backup` / `version` / `audit` / `settings`。
前后端共用同一份类型定义，避免接口漂移。
