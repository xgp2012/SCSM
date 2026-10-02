# SC-Panel · SurvivalcraftNet 开服面板

参考 MCSM 形态，为 [**SurvivalcraftNet 服务端**](https://gitee.com/SC-SPM/SurvivalcraftNet)
制作的全栈 Node.js 开服面板。

- **前端**：Nuxt 4（SPA，`ssr:false`）+ fuxsto-design + Tailwind CSS v4 + Pinia + xterm.js
- **后端**：NestJS + **node-pty**（伪终端 PTY，硬性要求）+ WebSocket 实时控制台
- **能力**：单管理员 JWT 认证、单节点多实例托管、实时终端与状态推送、存档/配置/备份管理、
  Gitee 版本下载与安装

完整开发计划与门禁要求见 [`plants.md`](./plants.md)；各阶段测试证据见
[`docs/test-reports/`](./docs/test-reports/)。

---

## 里程碑状态

| 阶段 | 内容 | 状态 | 证据 |
|------|------|------|------|
| M0 | 脚手架（workspace / 前后端跑通 / 测试接入） | ✅ 2026-09-30 | [M0.md](./docs/test-reports/M0.md) |
| M1 | 认证 + 实例 CRUD + PTY 启停 | ✅ 2026-10-01 | [M1.md](./docs/test-reports/M1.md) |
| M2 | WebSocket 终端 + 状态推送 | ✅ 2026-10-01 | [M2.md](./docs/test-reports/M2.md) |
| M3 | UDP 探测 + 指标 | ✅ 2026-10-01 | [M3.md](./docs/test-reports/M3.md) |
| M4 | 配置管理 | ✅ 2026-10-01 | [M4.md](./docs/test-reports/M4.md) |
| M5 | 存档与备份 | ✅ 2026-10-01 | [M5.md](./docs/test-reports/M5.md) |
| M6 | 版本下载与安装 | ✅ 2026-10-01 | [M6.md](./docs/test-reports/M6.md) |
| M7 | 打磨与安全 | ✅ 2026-10-01 | [M7.md](./docs/test-reports/M7.md) |

---

## 架构

```
┌──────────────────────────────────────────────────────────────┐
│  浏览器 (Nuxt4 SPA, ssr:false)                                  │
│  fuxsto-design + Tailwind4 + Pinia + xterm.js + WS 客户端       │
└───────────────▲───────────────────────────┬──────────────────┘
                │ REST (JWT HttpOnly Cookie) │ WebSocket (/ws/console)
                │                            ▼
┌───────────────┴──────────────────────────────────────────────┐
│  NestJS 后端 (apps/api)                                        │
│  ├─ auth          : 单管理员 JWT / Cookie，全局 JwtAuthGuard     │
│  ├─ instance      : CRUD / 模板·上传·导入建实例 / 端口分配        │
│  │  └─ supervisor : 状态机 / 崩溃识别 / 自动重启 / FileTailer    │
│  ├─ adapter       : IServerAdapter + PtyAdapter(node-pty)       │
│  ├─ console       : WS 网关 + 回滚缓冲 + 历史接口                │
│  ├─ server-config : Settings.xml / ServerSetting.json 读写       │
│  └─ (预留) probe / config / backup / version / system           │
└───────────────┬──────────────────────────────────────────────┘
                │ node-pty (伪终端 / ConPTY)
                ▼
┌──────────────────────────────────────────────────────────────┐
│  实例 N: 工作目录/ (Survivalcraft.dll, Settings.xml,            │
│          ServerSetting.json, Configs/, Worlds/, Bugs/Game.log) │
└──────────────────────────────────────────────────────────────┘
```

### 目录结构

```
scsm/
├─ apps/
│  ├─ web/                       # Nuxt4 SPA 前端
│  │  └─ app/
│  │     ├─ components/console/  # TerminalView（xterm.js）
│  │     ├─ components/instance/ # 卡片 / 状态灯 / 创建对话框
│  │     ├─ components/version/  # VersionsPanel（版本下载/安装）
│  │     ├─ composables/         # useApi / useConsoleSocket / useSystemStats
│  │     ├─ pages/               # login / index / instances/[id] / versions
│  │     ├─ stores/              # Pinia（auth / instances）
│  │     └─ middleware/auth.global.ts
│  └─ api/                       # NestJS 后端
│     ├─ src/
│     │  ├─ auth/                # JWT / Cookie / 全局守卫
│     │  ├─ instance/            # CRUD / Supervisor / Store / FileTailer / config.controller
│     │  ├─ adapter/             # IServerAdapter / PtyAdapter / terminal-utils
│     │  ├─ console/             # WS 网关 / 服务 / 回滚缓冲 / 历史接口
│     │  ├─ probe/               # UDP ServerInfo 探测 / 指标调度 / REST 端点
│     │  ├─ system/              # 系统 + 进程指标采样
│     │  ├─ backup/              # 存档列举 / tar.gz 备份 / 恢复+预快照 / 定时 / 保留
│     │  ├─ version/             # Gitee releases / 流式下载 / 解压安装 / 上传兜底 / 断点续传
│     │  ├─ audit/               # 审计（JSONL 追加 / 按天轮转 / 查询）
│     │  ├─ settings/            # 面板设置（备份策略/镜像/主题）+ 修改密码
│     │  ├─ server-config/       # Settings.xml / ServerSetting.json / Configs / Password
│     │  └─ common/              # 日志 / 异常过滤器 / 工具
│     ├─ scripts/m2-gate.cjs     # M2 真实门禁脚本
│     ├─ scripts/m3-gate.cjs     # M3 真实门禁脚本（UDP 探测 + 指标）
│     ├─ scripts/m4-gate.cjs     # M4 真实门禁脚本（配置读写 + 重启核实）
│     ├─ scripts/m5-gate.cjs     # M5 真实门禁脚本（备份 + 恢复 + 真实启服）
│     ├─ scripts/m6-gate.cjs     # M6 真实门禁脚本（Gitee 下载 + 解压安装 + 真实启服）
│     ├─ scripts/m7-gate.cjs     # M7 真实门禁脚本（限流 / 审计 / 设置）
│     └─ test/                   # Vitest（单元 + 集成）
├─ packages/
│  └─ shared/                    # 前后端共享类型（ESM + CJS 双产物）
├─ docs/test-reports/            # M0–M7 门禁证据
├─ THIRD_PARTY_LICENSES/         # 第三方许可文本
├─ data/                         # 运行期数据（实例元数据 / 日志 / 审计 / 设置）
├─ plants.md                     # 开发计划与测试门禁
└─ pnpm-workspace.yaml
```

---

## 已实现功能

### 认证（M1）
- 单管理员登录：`POST /api/auth/login` 校验 bcrypt 密码，签发 JWT 并以
  **HttpOnly + SameSite=Lax Cookie**（`sc_panel_token`）下发。
- 全局 `JwtAuthGuard` 保护除 `@Public()`（`/auth/login`、`/health`）外全部 REST 与 WS 握手。
- 首启：从 `.env` 读 `PANEL_USER` / `PANEL_PASSWORD`（或 `PANEL_PASSWORD_HASH`）；
  未配置则生成随机密码并仅打印一次。

### 实例管理（M1）
- **独立工作目录**模式：每个实例一份完整服务端拷贝，`Settings.xml`/`ServerSetting.json`/
  `Worlds/` 各自独立，可并行多开、端口隔离。
- 四种建实例来源：`template`（模板拷贝）/ `upload`（上传 zip 解压）/ `import`（导入既有目录）/
  `version`（M6 版本包下载安装）。
- **端口自动分配**：读取已用端口并探测空闲，写回 `Settings.xml.ServerPort`；
  配置写回**保留未知条目与顺序**（原子写）。
- 生命周期 `start / stop / restart / kill`：状态机
  `stopped → starting → running → stopping → stopped`，崩溃 → `crashed`；
  `autoRestart` 指数退避重启（5s 起，上限 60s）。
- 面板托管实例创建时 **`Autorun=true`**（无人值守开服，服务端启动后直接进世界）。

### 实时终端（M2）
- **PTY 硬性要求**：游戏主进程经 `node-pty`（Windows ConPTY）运行，保留真实 TTY
  （ANSI、增强控制台热键/表格、行缓冲、`Ctrl+C`）。
- WS 端点 `/ws/console?instanceId=<id>`：握手读取 HttpOnly Cookie 校验 JWT，
  未登录以 `4401` 关闭。
- **raw / line 双通道**：`console.raw` 原样 ANSI 字节流供 xterm.js 渲染；
  `console.line` 清洗后纯文本行供日志/检索/事件匹配。
- **回滚缓冲与回放**：每实例 5000 行纯文本 + 64KB ANSI 快照；连接即下发
  `console.history` + `console.hello`，另有 `GET /api/instances/:id/history`。
- **命令与 resize**：`console.command`（自动补 `/` 前缀）与 `console.write`（原始输入，
  含 `Ctrl+C`），写入经每连接队列 + 16ms 合帧；`terminal.resize` → `pty.resize`。
- **多观察者**：同一实例允许多个 WS 同时观察并写入，订阅扇出，单连接异常隔离。
- 前端 `TerminalView.vue` 用 `@xterm/xterm` + `@xterm/addon-fit`，容器 ResizeObserver
  自动 fit 并触发 resize。

### 状态探测与指标（M3）
- **UDP ServerInfo 探测**：向实例 `ServerPort` **单播** 未连接消息
  `[0x09][rawDeflate(0x88,0x00,0x01)]`，解析回执 `version/人数/上限/游戏模式/密码/世界时间`；
  不依赖 `BroadcastPort`（编译产物中不存在）。周期探测（默认 5s）+ REST 即时探测
  `GET /api/instances/:id/probe`。
- **系统指标**：`GET /api/system/stats`（`node:os` + `fs.statfs`，零外部依赖）返回主机
  CPU/内存/磁盘。**实例进程指标**：Windows `wmic`（回退 PowerShell）、Linux `/proc`，
  周期采样（默认 3s）。
- **WS 推送**：`instance.stats`（CPU/内存）、`player.list`（探测玩家/模式/版本）、
  `system.stats`（主机级，广播全部连接）。
- **前端**：概览页主机指标条 + 卡片显示在线人数/CPU/内存/模式与探测在线点；详情页实时
  StatTile 与「玩家」标签即时探测面板。

### 配置读写（M1 基础 · M4 完善）- `Settings.xml`：按 `Name`/`Value` 读写（如 `ServerPort`），保留未知条目与顺序。
- `ServerSetting.json`：浅合并世界参数（`WorldMaxPlayers`/`WorldName`/`Autorun` 等），
  不丢弃未知键。
- `Configs/{Level,Ban,Limit}Config.json`、`Plugins/Password.json`：分区化表单读写
  （M4，见上「配置管理」）。

### 配置管理（M4）
- **配置快照**：`GET /api/instances/:id/config` 返回 6 个分区
  （`settings` / `serverSetting` / `level` / `ban` / `limit` / `password`）及分区元数据；
  敏感字段（`WorldPassword`、`DefaultPassword`、`PlayerPassword`）以 `********` 脱敏回显。
- **分区写入**：`PUT /api/instances/:id/config/:section`，全部**保留未知字段/条目、原子写**：
  - `Settings.xml`：按键 `Name`/`Value` 更新（`ServerPort` 等可热改）；运行中允许。
  - `ServerSetting.json`：浅合并世界参数（`WorldName`/`WorldMaxPlayers`/`Autorun`/
    `GameMode`/`WorldPassword` 等）；运行中禁止。
  - `Configs/LevelConfig.json`：玩家权限 `{玩家:等级}`。
  - `Configs/BanConfig.json`：`BanUserList`/`BanUserIpList`/`BanIpList`。
  - `Configs/LimitConfig.json`、`Plugins/Password.json`：管理文件并标注「源码/遗留」
    （编译产物未含，见下）。
- **运行中写保护**：世界/权限/封禁/密码/限制分区在实例运行时返回 `409`。
- **前端**：实例详情页「配置」标签 = `ConfigPanel.vue`（分区切换 + 拖入删除 + 遗留徽标）。

> **实测口径**：编译产物 `Survivalcraft.dll` 真实读写 `Settings.xml`、`ServerSetting.json`、
> `Configs/{Level,Ban}Config.json`；`Configs/LimitConfig.json` 与 `Plugins/Password.json`
> **未编译进服务端**（面板仍管理并显式标注）。探测回执 `maxCount` 来自世界数据
> `WorldSettings.MaxOnlinePlayerCount`，`ServerSetting.WorldMaxPlayers` 仅在新建世界时播种。

### 存档与备份（M5）
- **列存档**：`GET /api/instances/:id/worlds` 解析 `Worlds/<name>/`，返回大小/文件数/
  最近修改/是否当前世界（对齐 `ServerSetting.json.WorldPath`）/是否含 `Project.json`。
- **创建备份**：`POST /api/instances/:id/backups { world, includeConfig }`，把**世界目录 +
  配置**（`Settings.xml`/`ServerSetting.json`/`Configs`/`Plugins`）打包为
  `backups/<uuid>.tar.gz`（**流式 tar.gz**，低内存），元数据 `backups/<uuid>.json`。
  运行中返回 `409`（保证快照一致）。
- **恢复**：`POST /api/instances/:id/backups/:backupId/restore { snapshot }`，流程为
  **停止实例 → 生成 `pre-restore` 安全快照 → 解压覆盖**；恢复后世界数据逐字节还原。
- **下载/删除**：`GET .../backups/:backupId/download`（tar.gz）、`DELETE .../:backupId`。
- **定时备份**：`BACKUP_INTERVAL_MS`（默认 0=禁用）周期对 stopped 实例执行；
  `BACKUP_KEEP`（默认 10）按时间淘汰旧备份，`pre-restore` 快照优先保留。
- **前端**：实例详情页「存档与备份」标签 = `BackupPanel.vue`（世界表 + 创建/列表/恢复/下载）。

### 版本下载与安装（M6）
- **releases 列表**：`GET /api/versions/releases` 拉取 Gitee Releases（`GITEE_REPO`），
  缓存（`VERSION_CACHE_MS`，默认 5 min）；拉取失败**降级到本地缓存**并返回可读 `notice`，
  无缓存则 `fallback=true` 提示改用手动上传。`POST /api/versions/releases/refresh` 强制刷新。
- **服务端包识别**：自动排除 `[电脑版]/[安卓版]/.apk/源码归档`；按 `serverPackageScore`
  优先托管式 `Survivalcraft.dll` 包（避免误装 `PocketSurvival`/`PSTerminal` 变体），
  并预选面板主机平台对应包（`recommendedAsset`）。
- **下载安装**：`POST /api/versions/download { tag, asset, mode, name?, instanceId? }`
  立即返回 `taskId`；后端**流式代理下载**（跟随 Gitee 302、带 Referer/UA、进度/速度/ETA、
  体积上限 `VERSION_MAX_BYTES`）→ 解压到实例目录 → 播种默认配置
  （`ServerPort`/`Autorun=true`/无世界时 `AutoGenerateWorld=true`）→ 注册实例。
  `mode=overwrite` 覆盖既有实例并**保留 `Configs/Worlds/Plugins/备份`**（安装前整目录快照）。
- **任务进度（REST 轮询）**：`GET /api/versions/tasks`、`GET /api/versions/tasks/:id`；
  阶段 `queued → downloading → extracting → installing → done|failed`，失败带可读 `error`。
- **手动上传兜底**：`POST /api/versions/upload`（multipart `file` + `mode/name/instanceId/preserve`），
  支持 zip/tar.gz，新建或覆盖。
- **前端**：`/versions` 页面 = `VersionsPanel.vue`（release/asset 列表、安装对话框、
  下载进度条、手动上传、兜底提示）。

### 日志
- **双通道消费**：PTY stdout + `tail Bugs/Game.log`（增量字节偏移），覆盖不同 `LogMode`。
- pino 结构化日志 + 统一异常过滤器（API 错误包络）。

### 打磨与安全（M7）
- **限流**：全局 `@nestjs/throttler`（每 IP 每分 300 次）；登录 **5/分**、版本下载/上传 **10/分**、
  实例操作 **30/分**；WS 命令/输入按连接 10s/40 条限速，超限回 `error: rate_limited`。
- **审计**：`<PANEL_DATA_DIR>/audit/YYYY-MM-DD.jsonl`（JSONL 追加、按天轮转），覆盖登录/启停/
  命令/配置/备份/下载/安装/设置；查询 `GET /api/audit`（from/to/action/instanceId/outcome/q/limit）。
  前端 `/audit` 页可视化过滤。
- **面板设置 `/settings`**：备份策略（定时间隔/保留数）、下载镜像/体积上限/缓存、主题偏好；
  **修改管理员密码**（校验当前密码，持久化于数据目录，优先于 `.env`）。均可运行期热更新。
- **修复**：`fuxsto-design@1.0.4` 的 `Dialog` 不渲染正文，改用自研 `AppDialog`/`dialog.ts`（API 对齐）。
- M6 遗留收尾：下载支持 **Range 断点续传 + 3 次重试**（4xx 不重试）；覆盖安装后自动清理旧
  `pre-install` 快照（保留最近 3 份）。

### 日志
- **双通道消费**：PTY stdout + `tail Bugs/Game.log`（增量字节偏移），覆盖不同 `LogMode`。
- pino 结构化日志 + 统一异常过滤器（API 错误包络）。

---

## 快速开始

### 前置

- Node.js **≥ 24**
- pnpm **11**（`packageManager` 已声明）
- .NET SDK **10**（运行服务端 `dotnet Survivalcraft.dll`）
- 一份 SurvivalcraftNet 服务端本体（含 `Survivalcraft.dll` / `Content.scpak`）

### 安装与运行

```bash
pnpm install
cp .env.example .env      # 按需修改（Windows: copy .env.example .env）
pnpm dev                  # 并行启动 api(:3001) 与 web(:3000)
```

打开 http://localhost:3000 ，使用 `.env` 中的管理员账号登录。

> **模板目录**：`.env` 的 `SERVER_TEMPLATE_DIR` 指向服务端本体目录
> （相对后端进程工作目录 `apps/api`，默认 `../../../SurvivalcraftNet/服务端本体`）。
> 若不便使用模板，可在创建实例时改用"上传 zip"。

### 其它脚本

```bash
pnpm build       # 构建 shared → api → web
pnpm typecheck   # 类型检查（三包）
pnpm lint        # ESLint（三包）
pnpm test        # Vitest（api / web / shared）
pnpm test:e2e    # Playwright 真实全链路（需先 build，自动拉起前后端）
```

---

## 环境变量

见 [`.env.example`](./.env.example)。要点：

| 变量 | 说明 | 默认 |
|------|------|------|
| `PANEL_HOST` / `PANEL_PORT` | 后端监听 | `127.0.0.1` / `3001` |
| `PANEL_USER` | 管理员用户名 | `admin` |
| `PANEL_PASSWORD` | 管理员密码（首启生成哈希；亦可直接给 `PANEL_PASSWORD_HASH`） | — |
| `JWT_SECRET` / `JWT_EXPIRES_IN` | JWT 密钥与有效期 | 开发默认 / `12h` |
| `PANEL_CORS_ORIGIN` | 允许的前端来源 | `http://localhost:3000` |
| `PANEL_DATA_DIR` | 面板数据目录（实例元数据 / 日志） | `./data` |
| `SERVER_TEMPLATE_DIR` | 服务端本体模板目录 | `../../../SurvivalcraftNet/服务端本体` |
| `STOP_GRACE_MS` | 优雅停止等待（ms），超时后强杀 | `15000` |
| `LOG_LEVEL` / `LOG_PRETTY` | 日志级别与美化 | `info` / `true` |
| `GITEE_REPO` / `GITEE_MIRROR` | 版本下载仓库与镜像 | `SC-SPM/SurvivalcraftNet` / — |
| `VERSION_SOURCE` | 版本来源 id（预留第三方源） | `gitee` |
| `VERSION_CACHE_MS` | releases 列表缓存时长（ms），0 不缓存 | `300000` |
| `VERSION_MAX_BYTES` | 单个版本包下载大小上限（字节） | `2147483648`（2G） |
| `PROBE_INTERVAL_MS` | UDP 探测周期（ms），0 禁用 | `5000` |
| `METRICS_INTERVAL_MS` | 进程/系统指标采样周期（ms），0 禁用 | `3000` |
| `BACKUP_INTERVAL_MS` | 定时自动备份周期（ms），0 禁用 | `0` |
| `BACKUP_KEEP` | 每实例保留的最大备份数，0 不限制 | `10` |

---

## 测试与门禁

项目遵循 [`plants.md` §9.5](./plants.md) 的**测试门禁**：每个里程碑必须通过可复现的
端到端验证并留存 `docs/test-reports/M<n>.md`。

- **单元 / 集成**：Vitest（三包）。集成测试用 Nest `Test` + Supertest，SWC 转换器
  产出 `emitDecoratorMetadata` 以支持 DI。
- **真实门禁脚本**：`apps/api/scripts/m2-gate.cjs` —— 对运行中的 API 发真实 HTTP/WS，
  真实拉起服务端并验证终端输出、`/help` 回执、多观察者、resize、历史回放。
  `apps/api/scripts/m3-gate.cjs` —— 独立发真实 UDP 探测并校验 REST/WS 指标推送。
  `apps/api/scripts/m4-gate.cjs` —— 真实读写各配置分区并重启核实、校验未知字段保留与非法输入拒绝。
  `apps/api/scripts/m5-gate.cjs` —— 真实列存档、备份落盘、篡改→恢复→数据还原、恢复后真实启服、运行中写保护。
  `apps/api/scripts/m6-gate.cjs` —— 真实拉 Gitee releases、真实联网下载服务端包、解压安装到新实例并真实启服、上传兜底、覆盖安装保留配置。

```bash
pnpm test                                   # 全部单测（当前 134 passed）
node apps/api/scripts/m2-gate.cjs           # M2 门禁（需先启动后端）
node apps/api/scripts/m3-gate.cjs           # M3 门禁（需先启动后端）
node apps/api/scripts/m4-gate.cjs           # M4 门禁（需先启动后端）
node apps/api/scripts/m5-gate.cjs           # M5 门禁（需先启动后端）
node apps/api/scripts/m6-gate.cjs           # M6 门禁（需先启动后端 + 可访问 Gitee）
node apps/api/scripts/m7-gate.cjs           # M7 门禁（限流/审计/设置）
pnpm test:e2e                               # M7 Playwright 真实全链路（自动拉起前后端）
```

| 阶段 | 单测/集成 | 真实门禁 |
|------|-----------|----------|
| M1 | 38 passed | 真实启服 / 双实例并行 / 崩溃自动重启 |
| M2 | 52 passed | 15/15 PASS（真实 PTY 终端 + `/help` 回执） |
| M3 | 62 passed | 12/12 PASS（真实 UDP 探测 + 进程/系统指标） |
| M4 | 80 passed | 14/14 PASS（真实配置落盘 + 未知字段保留 + 写保护 + 非法输入拒绝） |
| M5 | 94 passed | 10/10 PASS（真实备份落盘 + 恢复+预快照 + 恢复后启服 + 定时备份） |
| M6 | 124 passed | 13/13 PASS（Gitee 下载 + 解压安装 + 真实启服 + 上传兜底 + 覆盖保留配置） |
| M7 | 134 passed | 12/12 PASS（限流/审计/设置）+ Playwright E2E 2/2 PASS（真实全链路） |

---

## 工程备注

- **Windows ConPTY**：`node-pty.spawn` 不接受裸命令名，需先经 `resolveExecutable`
  解析为绝对路径（如 `dotnet.EXE`）。
- **无人值守开服**：面板托管实例默认 `ServerSetting.json.Autorun=true`；否则服务端停留
  在 `Entered screen "Play"`，不打印 `开启服务器成功，端口 ...`，状态机无法进入 `running`。
- **依赖构建脚本**：`pnpm-workspace.yaml` 需显式允许 `esbuild` / `node-pty` /
  `@tailwindcss/oxide` / `unrs-resolver` 等构建脚本。
- **共享包产物**：`@sc-panel/shared` 输出 **ESM + CJS 双产物**（CJS 供 NestJS 运行时 `require`）。
- **Nuxt 代理**：开发期经 Nitro `routeRules.proxy` 将 `/api` 与 `/ws` 指向后端。
- **UDP 探测协议**：向实例 `ServerPort` 单播 `[0x09][rawDeflate(0x88,0x00,0x01)]`；
  `0x09` 为 LiteNetLib `UnconnectedMessage` property 字节，缺失会被 `PeerNotFound` 拒绝。
  `BroadcastPort` 在编译产物中不存在，故不做 LAN 广播发现。
- **版本包安装播种（M6）**：从 Gitee 下载的版本包不含 `Settings.xml`/`ServerSetting.json`/
  世界存档；面板安装时须播种 `ServerPort` + `Autorun=true`，且世界目录不存在时置
  `AutoGenerateWorld=true`，否则服务端因「自动运行存档不存在」直接退出。
- **服务端包变体（M6）**：同一 release 存在多种「服务端」资产；面板按 `serverPackageScore`
  优先托管式 `net10.0/Survivalcraft.dll` 包，避免误装 `PocketSurvival`（C++/`PSTerminal.exe`）。
- **运行期设置（M7）**：`BACKUP_*` / `GITEE_MIRROR` / `VERSION_CACHE_MS` / `VERSION_MAX_BYTES`
  等可由 `/settings` 覆盖（持久化 `data/panel-settings.json`），**无需重启**即时生效。
- **WS 部署（M7）**：前端终端优先**直连后端 origin**（`NUXT_PUBLIC_API_BASE`，Cookie 按 host 作用域
  跨端口仍携带）；若经反向代理，需确保 `/ws` 支持 WebSocket 升级。
- **E2E（M7）**：`pnpm test:e2e` 由 Playwright 自动拉起后端与 `nuxt preview`，使用真实服务端模板，
  不 mock；需先 `pnpm build` 且 `SERVER_TEMPLATE_DIR` 就绪。
- **对话框（M7）**：`fuxsto-design@1.0.4` 的 `Dialog` 正文不渲染，已用自研 `AppDialog` 替代（API 对齐）。

---

## 第三方许可

本项目允许参考/移植 [MCSManager](https://github.com/MCSManager/MCSManager)（MIT）的
小颗粒工具函数，凡移植处均在文件顶部**标注来源与贡献**。

> 截至 M7，**仍未移植任何 MCSM 代码**——终端解析（`LineSplitter`/`stripAnsi`）、
> `FileTailer`、端口分配、优雅停止、审计、限流、对话框等均为本项目独立实现。许可框架（
> `THIRD_PARTY_LICENSES/` + 本段落）已就位，后续如发生移植将同步补标注。

许可文本见 [`THIRD_PARTY_LICENSES/`](./THIRD_PARTY_LICENSES/)。
