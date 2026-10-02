# SurvivalcraftNet 面板 —— 开发计划（plants.md）

> 目标：参考 MCSM 的形态，为 **SurvivalcraftNet 服务端**（Gitee: SC-SPM/SurvivalcraftNet）
> 制作一个**全栈 Node.js 开服面板**。前端 **Nuxt 4 + fuxsto-design**，启用 **SPA 模式**优化性能；
> 后端 Node.js，支持**单节点 · 多实例托管**、**WebSocket 实时控制台与状态推送**、
> **单管理员 JWT 认证**、**存档/配置/备份管理**、**服务端版本下载**。
> 允许参考并部分移植 MCSM 代码，凡移植处须**标注贡献**。
>
> **两条硬性要求（贯穿全程）：**
> 1. **必须引入 PTY**（伪终端）运行服务端，保证仿真终端行为（ANSI、行编辑、热键、增强控制台
>    正常显示），而非简单管道 `pipe`。
> 2. **阶段完成必须真正测试其功能**：每个里程碑（M0–M7）结束时都必须按 §14 的**测试门禁**
>    完成可复现的端到端验证（含真实启服、真实 WS 控制台、真实命令回执、真实文件读写），
>    **禁止"看起来简单就跳过/只做单元级冒烟就算完成"**。未通过门禁不得进入下一阶段。

---

## 0.0 实施进度速览（据实际交付标注）

> **当前进度：M0 / M1 / M2 / M3 / M4 / M5 / M6 / M7 已完成并通过门禁，全部里程碑达成。**
> 每阶段证据：`docs/test-reports/M0.md` … `M7.md`。

| 里程碑 | 状态 | 门禁证据 | 备注 |
|--------|------|----------|------|
| M0 脚手架 | ✅ 2026-09-30 | `M0.md` | workspace(Nuxt4 SPA + NestJS + shared) / 测试接入 |
| M1 认证+实例+PTY 启停 | ✅ 2026-10-01 | `M1.md` | JWT Cookie / 模板·上传·导入建实例 / 真实启服 / 崩溃重启 |
| M2 WS 终端+状态推送 | ✅ 2026-10-01 | `M2.md` | `/ws/console` / xterm.js / raw·line 双通道 / 回放 / resize |
| M3 UDP 探测+指标 | ✅ 2026-10-01 | `M3.md` | 单播 ServerInfo 探测 / 进程·系统指标 / WS `instance.stats`·`player.list` |
| M4 配置管理 | ✅ 2026-10-01 | `M4.md` | 6 分区表单读写 / 未知字段保留 / 密码脱敏 / 运行中写保护 |
| M5 存档与备份 | ✅ 2026-10-01 | `M5.md` | 列存档 / tar.gz 备份 / 恢复+预快照 / 定时 / 保留淘汰 |
| M6 版本下载与安装 | ✅ 2026-10-01 | `M6.md` | Gitee releases / 流式下载安装 / 上传兜底 / 覆盖保留配置 |
| M7 打磨与安全 | ✅ 2026-10-01 | `M7.md` | 限流·审计 · 面板设置·改密 · Playwright 真实 E2E · 断点续传 · 快照清理 |

**实施中确认/偏离计划的关键点（已并入下列各节正文）：**

1. **后端框架＝NestJS 已落地**（§2.1）：模块拆分为 `auth` / `instance` / `server-config` /
   `adapter` / `console`（+ 预留 probe/config/backup/version/system）。
2. **PTY 硬性要求已满足**：`PtyAdapter`（`node-pty`，Windows ConPTY）承载游戏主进程，
   raw/line 双通道；**绝无**用 `child_process.spawn` 管道替代（§2.2 / §7.5 已实测）。
3. **面板托管实例默认无人值守开服**：创建实例时 `ServerSetting.json.Autorun=true`
   （`autoRun` 默认 `true`）。否则服务端停留在 `Entered screen "Play"`，不打印
   `开启服务器成功，端口 ...`，状态机无法进入 `running`（已实测，见 §11-Q4）。
4. **Windows ConPTY 需解析绝对路径**：`resolveExecutable('dotnet', ...)` 返回
   `dotnet.EXE` 全路径后 `node-pty.spawn` 方可启动（§2.2 工程备注）。
5. **日志双通道**：同时消费 PTY stdout 与 `tail Bugs/Game.log`（`FileTailer`），
   覆盖不同 `LogMode`（§0.2 / §5.9 已落地）。
6. **WS 鉴权复用 HttpOnly Cookie**：握手读取 `sc_panel_token`（亦兼容 `Bearer`），
   与 REST 同源，无需前端额外拼接 token（§5.3）。
7. **终端回滚缓冲**：5000 行纯文本 + 64KB ANSI 快照（`CONSOLE_HISTORY_*` 常量，§2.2）。
8. **测试框架**：Vitest（三包），集成测试用 Nest `Test` + Supertest + SWC
   （`emitDecoratorMetadata` 以支持 DI）；Playwright E2E 按计划在 M7 接入（§9.5.1）。
9. **工程环境**：Node 24 / pnpm 11 / dotnet 10；`pnpm-workspace.yaml` 需显式允许
   `esbuild`/`node-pty` 等构建脚本（§10 工程备注）。
10. **里程碑边界**：M2 只做终端+状态推送；UDP 探测与系统指标归 M3（**已完成**）。

11. **UDP 探测协议（M3 实测确定）**：面板向实例 `ServerPort` **单播** 未连接消息
    `[0x09][rawDeflate(0x88,0x00,0x01)]`，回执 `[0x09][rawDeflate(...)]`；不依赖
    `BroadcastPort`（§11-Q1）。探测周期 5s、进程/系统采样 3s（`PROBE_INTERVAL_MS`/
    `METRICS_INTERVAL_MS`），WS 推送 `instance.stats`/`player.list`/`system.stats`（§9.5.2 M3）。

---

## 0. 调研结论（来自本仓库与官方发布页）

### 0.1 服务端启动与控制方式
- 入口：`服务端本体/Survivalcraft.dll`（`net10.0`），启动 `dotnet Survivalcraft.dll`；
  Linux 亦可用 `start.sh`。
- 交互式控制台：进程读取 stdin（`Console.ReadLineAsync`），命令以 `/` 开头，
  以 `isTerminal = true`（管理员权限 100）执行，回执走 stdout。
- **增强控制台**（编译产物中存在，源码仅为参考）：
  - 命令：`/help` `/ls`(列存档) `/save` `/backup`(备份) `/stop`(关服退出) `/exit`
    `/say <msg>`(全服广播) `/give` `/time` `/rpl`(踢玩家) `/bpl`(黑名单) `/abp`(加黑名单)
    `/paw`(设进服密码) `/rmg`(设/取消管理员) `/anmp` `/cpl` `/rpl` `/clr`(清玩家缓存) 等。
  - 密钥快捷：`[help] [ls] [save] [backup] [stop] [say] [exit] [time] [rpl] [bpl] [abp] [paw] [rmg] [clr] [mem]`
  - 启动参数：`--basic`(基础终端) / `--enhanced`(增强终端) / `--force-ansi` /
    **`--sckey-token-local`（面板开服必须加，见 x26.06.19 更新日志）**。
  - 相关文案：`服务器将在3秒后重启`、`秒后重启服务器`、`存档备份成功/失败`、
    `例行维护] 9s后关闭服务器并全服弹窗通知例行维护`、`清空并退出`。
  - 结论：**重启**可通过 `/stop` 后由面板重新拉起实现；**备份**有原生 `/backup`，
    面板亦可做自己的文件级备份。

### 0.2 运行时实测布局（实测：`dotnet 服务端本体/Survivalcraft.dll` 首次运行后生成）

工作目录 = 可执行文件所在目录（`服务端本体/`）。运行后新增：

```
服务端本体/
├─ Settings.xml          # 全局设置（含 ServerPort 等）
├─ ServerSetting.json    # 服务端/世界配置（端口外的世界参数）
├─ ModSettings.xml
├─ CommunityContentCache.xml
├─ Bugs/Game.log         # 运行日志（按行，含时间戳/级别/中文）
├─ Configs/
│  ├─ LevelConfig.json   # 权限等级  实测内容: {}
│  ├─ BanConfig.json     # 封禁列表  实测: {"BanUserList":[],"BanUserIpList":[],"BanIpList":[]}
│  └─ LimitConfig.json   # 限制插件  实测: {"WaterLength":7,"MagmaLength":4,"LimitBlockBreak":true,"LimitExplode":false}
├─ Plugins/              # 初始为空（密码等配置在启用后生成）
├─ Worlds/
│  └─ World/             # 存档（默认世界）
│     ├─ Project.json    # 世界数据（MessagePack/JSON，含 GameInfo）
│     ├─ Project.json.bak
│     └─ Regions/
├─ NetMods/              # Mod（.scmod）
├─ CharacterSkins/  FurniturePacks/  TexturePacks/
```

**重要修正（实测覆盖此前推断）：**
- **端口**在 `Settings.xml`：`<Setting Name="ServerPort" Value="28887" />`。
  **未发现 `BroadcastPort` 条目** —— 广播端口疑为固定值或另有来源，需在面板中按
  `ServerPort` 管理，广播探测另行确认（见 §11-Q3）。
- **限制配置文件名是 `Configs/LimitConfig.json`**（不是 `Plugins/Limit.json`），
  字段实测仅 `WaterLength/MagmaLength/LimitBlockBreak/LimitExplode`（无 View 字段）。
- **权限文件初始为 `{}`**；**封禁文件结构**与源码一致。
- **世界配置**在 `ServerSetting.json`（`WorldPath: app:/Worlds/World`、`WorldName`、
  `WorldMaxPlayers`、`GameMode`、`WorldPassword`、`WorldSeed`、`Autorun`、
  `AutoGenerateWorld`、`CheckLogin`、`ScKeyServerId`、`PvpEnabled` 等）——**面板创建实例时
  应写入此处**。
- **日志**在 `Bugs/Game.log`（非 stdout 专属）；面板务必**同时**消费 PTY 输出与
  tail `Bugs/Game.log`，以覆盖不同 `LogMode` 行为。
- 启动特征行（可供状态机匹配）：`开启服务器成功，端口 28887`、`Entered screen "Game"`、
  `服务器 启动于 ... 版本=2.4.0.0-ljx26.06.19`。
- 关服特征行：`[断开] 停止网络服务`、`[服务器] 主动关闭，正在通知所有客户端`。

### 0.2.1 `ServerSetting.json`（实测字段）
```json
{
  "CheckLogin": false,
  "ScKeyServerId": "",
  "ScKeyServerName": "",
  "Autorun": false,
  "AutoGenerateWorld": false,
  "WorldPath": "app:/Worlds/World",
  "WorldName": "ScWorld",
  "WorldSeed": "",
  "WorldPassword": "",
  "WorldKeywordBlocking": "",
  "WorldMaxPlayers": 20,
  "WorldDaySpeed": 1,
  "WorldRecoverySpeed": 1,
  "WorldDisableBlocks": "",
  "RandomSpawnPosition": false,
  "GameMode": 1,
  "SeasonChanging": true,
  "PVPEnabled": true
}
```
- `WorldPath` 用 `app:/` 前缀指向工作目录下 `Worlds/<name>`。
- `Autorun` + `AutoGenerateWorld` 决定启动是否自动进世界（面板可置 `Autorun=true`
  让服务端无人值守直接开服；`AutoGenerateWorld` 用于首启建图）。
- `CheckLogin` / `ScKeyServerId` 与 SCKey 联机号相关，对应 `--sckey-token-local`。

### 0.2.2 全局设置 `Settings.xml`（关键项）
- `ServerPort`（实测 `28887`）= 游戏 UDP 端口。
- `AllowLanConnection`、`MultithreadedTerrainUpdate`、`EnableMod`、
  `Server_ChunkSendPeriod`、`Server_ChunkCountSendPer`、`LiteNetLibLogLevel`、
  `LogMode`、`EnableStatLogging` 等。
- 面板写回需**保留未知条目与顺序**（XML 属性 `Name`/`Value`）。

### 0.3 状态探测（UDP）
- 局域网发现：客户端向 `BroadcastPort` 发 UDP 广播；包格式 `0x88` + `ServerInfo` 包 ID
  + `bool true`（请求）。服务端单播回复：`version(string)`、`clientCount(u16)`、
  `maxPlayerCount(u16)`、`gameMode(enum)`、`needLogin(bool)`、`needPasswd(bool)`、
  `timeOfDay(float)`。见 `NetWork/Package/ServerInfoPackage.cs`、`Screen/NetPlayScreen.cs`。
- 采集到实测世界信息：GameMode=Survival、WorldName=Glassmallow、MaxPlayers=20、
  Password 为空、TimeOfDay 可变；可作探测解析的对照样例。
- 面板可据此实现**独立于进程的在线探测/Ping**（无需改服务端），用于实例状态与人数展示。
- 待确认：`BroadcastPort` 具体值（`Settings.xml` 中未出现），见 §11-Q3。

### 0.4 版本下载
- Gitee Releases：`https://gitee.com/SC-SPM/SurvivalcraftNet/releases`，
  已知版本 `x26.07.01.01`（最新）、`x26.06.19`。
- 直链形如：`https://gitee.com/SC-SPM/SurvivalcraftNet/releases/download/<tag>/<file>`。
- 服务端相关产物示例：`服务端X26.07.01.01.zip`、`X26.07.01.01【Linuxx64】.zip`、
  `[linux-arm64][26.07.01]服务端.zip`。
- 面板需能：**列出 release（含 tag/说明/资源）→ 下载服务端包 → 解压到实例目录 → 校验**。

### 0.5 技术栈基线（fuxsto-design）
- `fuxsto-design@1.0.4`：Vue 3 组件库，**Tailwind CSS v4**，78 组件，暗色模式内置。
- Peer 依赖：`vue ^3.5`、`tailwindcss ^4`、`lucide-vue-next ^0.577`。
- 引入两步：`@import "tailwindcss"; @import "fuxsto-design/styles";`
- 支持按需子路径导入（`fuxsto-design/button` 等），利于 tree-shaking。
- Node 24 / npm 11 已就绪。

---

## 1. 总体架构

```
┌──────────────────────────────────────────────────────────────┐
│  浏览器 (Nuxt4 SPA, ssr:false)                                  │
│  fuxsto-design + Tailwind4 + Pinia + WS 客户端                  │
└───────────────▲───────────────────────────┬──────────────────┘
                │ REST (JWT Cookie)          │ WebSocket (实时流)
                │                            ▼
┌───────────────┴──────────────────────────────────────────────┐
│  Node.js 后端 (NestJS 或 Express+ws，见 §2)                     │
│  ├─ Auth     : 单管理员 JWT/Cookie                             │
│  ├─ REST API : 实例/存档/配置/备份/版本/系统                     │
│  ├─ WS Hub   : 终端流(xterm)/状态推送/日志（多路复用）           │
│  ├─ Instance Supervisor : 多实例进程生命周期管理                  │
│  ├─ Adapters : PtyAdapter(node-pty) + PluginAdapter(预留)       │
│  ├─ Probe    : UDP ServerInfo 探测 + 系统指标(os/pidusage)       │
│  ├─ Downloader: Gitee Releases 拉取/下载/解压服务端包             │
│  └─ Store    : 实例元数据(lowdb/sqlite) + 日志文件轮转           │
└───────────────┬──────────────────────────────────────────────┘
                │ node-pty (伪终端 / PTY)
                ▼
┌──────────────────────────────────────────────────────────────┐
│  实例 N: 工作目录/ (Survivalcraft.dll, Configs/, Plugins/, 存档) │
└──────────────────────────────────────────────────────────────┘
```

### 1.1 关键设计原则
- **PTY 优先**：所有实例**必须**通过 PTY 运行（`node-pty`），以获得真实 TTY：
  ANSI 转义、增强控制台热键/表格/进度条、行缓冲、`Ctrl+C` 语义等。禁止用
  `child_process.spawn` 的普通管道替代交互式控制台（仅允许作为非交互辅助命令，
  且不得用于游戏主进程）。
  > **已落地（M1/M2）**：`PtyAdapter` 实现此原则，游戏主进程只经 `node-pty` 运行。
- **适配器模式**：`IServerAdapter` 统一定义 `start/stop/restart/sendCommand/on('log'|'status')`，
  首期实现 `PtyAdapter`（node-pty + UDP 探测），预留 `PluginAdapter`（未来 C# 插件 RPC）。
  > **已落地（M1）**：`IServerAdapter` 事件为 `raw`/`line`/`exit`/`status`；
  > `PluginAdapter` 尚未创建（预留接口，见 §5.4）。
- **进程隔离**：每个实例一个 PTY 子进程；面板主进程不阻塞；实例崩溃自动识别 + 可选自动重启。
  > **已落地（M1）**：`InstanceSupervisor` 实现状态机 + 崩溃识别 + 指数退避自动重启。
- **工作目录即实例边界**：实例 = 一个工作目录 + 一份元数据（json）+ 独占端口。
  > **已落地（M1）**：元数据 JSON（`InstanceStore` 原子写）位于 `data/instances/<id>/`。
- **多实例端口分配**：记录 `ServerPort`/`BroadcastPort`，创建实例时自动分配未占用端口。
  > **部分落地（M1）**：`ServerPort` 自动分配（`Settings.xml`）；`broadcastPort` 字段保留，
  > 其来源/分配待 §11-Q3 确认（M3 探测时处理）。
- **不修改服务端本体**：首期通过 PTY(stdin/stdout) 与 UDP 探测实现全部能力。
  > **已落地（M1/M2）**：仅读写配置与经 PTY 交互，未改动服务端二进制。

---

## 2. 技术选型

| 层 | 选型 | 说明 |
|----|------|------|
| 前端框架 | **Nuxt 4**，`ssr: false`（SPA） | 满足"SPA 优化性能"要求 |
| UI | **fuxsto-design** + Tailwind v4 | 指定组件库 |
| 状态 | Pinia | 实例/用户/WS 状态 |
| 请求 | `$fetch`/ofetch + 统一拦截器 | JWT |
| 实时 | 原生 WebSocket（`ws` 服务端） + **xterm.js** 客户端渲染 | 仿真终端 |
| 终端 | **`node-pty`（伪终端 PTY，硬性要求）** | 真实 TTY/ANSI/热键 |
| 后端 | **NestJS（已确认）**，模块化契合多实例 + `@nestjs/websockets` | 见 §2.1 |
| 校验 | zod / class-validator | DTO 校验 |
| 存储 | SQLite(better-sqlite3) 或 lowdb | 实例元数据、审计日志 |
| 进程/系统 | Node `os`、`pidusage`/`systeminformation` | CPU/内存 |
| 解压 | `adm-zip` / `unzipper` / `tar` | 版本包解压 |
| 日志 | pino + 文件轮转 | 实例日志 |

### 2.2 PTY 终端方案（硬性要求）
- 使用 **`node-pty`** 为每个实例创建伪终端，`spawn` 游戏进程；客户端用 **xterm.js**
  渲染原始字节流，实现"仿真终端"。
- **为什么必须 PTY**：服务端增强控制台依赖 TTY（ANSI 转义、表格/进度条、热键
  `[keyhelp]`、行缓冲、`--enhanced` 模式探测）。普通 pipe 会导致：
  控制台模式降级/乱码、热键失效、无颜色、`isTerminal` 行为差异。
- 数据链路：`PTY → 后端按 UTF-8/ANSI 原样透传（可选清洗控制字符）→ WS 二进制/文本帧
  → xterm.js write()`；输入反向 `xterm.onData → WS → pty.write()`。
- 关键点：
  - 记录并复用 `TERM`（默认 `xterm-256color`）与 `COLUMNS/LINES`，支持窗口 resize
    （`pty.resize`），Windows 上通过 winpty/conpty（`node-pty` 自带）工作。
  - 后端保留**纯文本行缓冲**（解析后）用于日志落盘、检索、事件匹配（如"启动成功"、
    "秒后重启"），与原始 ANSI 流分离，避免污染日志。
  - 终端输出**限流/背压**：大流量时批量合并帧（~16ms），防止 WS 洪泛。
  - 安全：单管理员场景允许多个 WS 观察同一 PTY，但写入命令需队列化，避免交叉输入。
- 进程控制语义：`Ctrl+C` = 发送 `\x03`；优雅停止优先发送 `/stop` 命令，
  超时后再 `SIGTERM`→`SIGKILL`（见 §7.2）。

### 2.1 后端框架（已确认：NestJS）
- 采用 **NestJS**：模块（auth/instance/adapter/console/probe/config/backup/version/system）
  + `@nestjs/websockets`+`@nestjs/platform-ws`（Gateway）天然分层，契合多实例与依赖注入。
- 组件：`@nestjs/common` `@nestjs/jwt` `@nestjs/passport`（或自写 JWT Guard）
  `@nestjs/schedule`（定时备份）`@nestjs/config`（.env）`class-validator`/`class-transformer`（DTO）。

---

## 3. 目录结构（规划）

```
sc-panel/
├─ apps/
│  ├─ web/                          # Nuxt4 SPA 前端
│  │  ├─ nuxt.config.ts             # ssr:false, css 引入, 代理
│  │  ├─ app/
│  │  │  ├─ assets/css/main.css     # @import tailwindcss + fuxsto-design/styles
│  │  │  ├─ components/
│  │  │  │  ├─ instance/            # 实例卡片/状态灯/操作条
│  │  │  │  ├─ console/             # 终端(虚拟滚动)
│  │  │  │  ├─ player/  backup/  version/
│  │  │  ├─ composables/
│  │  │  │  ├─ useApi.ts            # REST 封装
│  │  │  │  ├─ useConsoleSocket.ts  # WS + xterm.js 终端
│  │  │  │  └─ useInstanceStore.ts  # Pinia
│  │  │  ├─ pages/
│  │  │  │  ├─ login.vue
│  │  │  │  ├─ index.vue             # 实例列表/概览
│  │  │  │  ├─ instances/[id].vue    # 详情: 终端/配置/存档/玩家
│  │  │  │  ├─ versions.vue          # 版本下载与安装
│  │  │  │  └─ settings.vue          # 面板设置
│  │  │  └─ app.vue / layouts/
│  │  └─ package.json
│  └─ api/                          # NestJS 后端
│     ├─ src/
│     │  ├─ main.ts
│     │  ├─ auth/  (JWT, guard)
│     │  ├─ instance/ (supervisor, dto, controller)
│     │  ├─ adapter/ (IServerAdapter, PtyAdapter, PluginAdapter.stub)
│     │  ├─ console/ (WS gateway + PTY 桥接)
│     │  ├─ probe/   (UDP ServerInfo)
│     │  ├─ config/  (读写 Settings.xml / ServerSetting.json / Configs/*.json)
│     │  ├─ backup/  (存档/备份)
│     │  ├─ version/ (Gitee releases)
│     │  ├─ system/  (cpu/mem/disk)
│     │  └─ common/  (log, errors, utils)
│     └─ package.json
├─ packages/
│  └─ shared/                       # 前后端共享类型 (Instance, Status, WsEvent...)
├─ data/                            # 面板自身数据
│  ├─ panel.db (或 panel.json)
│  ├─ instances/<id>/meta.json
│  └─ logs/
├─ .env.example
├─ pnpm-workspace.yaml
└─ README.md
```

> 单仓多包（pnpm workspace）便于共享 TS 类型；若偏好简单，可退化为单后端 + 独立前端两目录。

---

## 4. 数据模型

### 4.1 Instance（实例）——字段对齐实测
```ts
interface Instance {
  id: string;                 // uuid
  name: string;               // 显示名
  source: 'template'|'upload'|'import'; // 基础包来源（模板拷贝/上传zip/导入）
  dir: string;                // 工作目录（含 Survivalcraft.dll, Settings.xml, ServerSetting.json）
  serverPort: number;         // Settings.xml 的 ServerPort，实测 28887
  broadcastPort?: number;     // 广播端口（待确认来源，见 §11-Q3）
  version: string;            // 如 2.4.0.0-ljx26.06.19 / x26.07.01.01
  launchArgs: string[];       // e.g. ["--sckey-token-local","--enhanced"]
  worldPath: string;          // ServerSetting.json.WorldPath，如 app:/Worlds/World
  worldName: string;          // ServerSetting.json.WorldName
  maxPlayers: number;         // ServerSetting.json.WorldMaxPlayers
  autoRestart: boolean;
  autoStart: boolean;
  autoRun: boolean;           // ServerSetting.json.Autorun（无人值守开服）
  createdAt: string;
  // 运行时(内存)
  status: 'stopped'|'starting'|'running'|'stopping'|'crashed';
  pid?: number;
  serverPort?: number;        // 运行时从启动日志解析到的实际监听端口（可选）
  online?: number; maxOnline?: number; gameMode?: string; hasPassword?: boolean;
  cpu?: number; memMB?: number;
  startedAt?: string; uptimeMs?: number;
}
```

### 4.2 其它
- `User`：单管理员（`username` + bcrypt hash，来自 `.env` 或首次初始化）。
- `BackupMeta`：`{id, instanceId, file, size, createdAt, type: 'manual'|'auto'}`。
- `AuditLog`：`{ts, action, instanceId?, detail}`。

---

## 5. 后端功能模块

### 5.1 认证 `auth`
- `POST /api/auth/login` → 校验管理员密码（bcrypt）→ 签发 JWT（HttpOnly Cookie）。
- `POST /api/auth/logout`、`GET /api/auth/me`。
- Guard 保护除 login 外全部 REST 与 WS 握手。
- 首启从 `.env` 读 `PANEL_USER` / `PANEL_PASSWORD_HASH`；未设置则生成随机密码并打印一次。

### 5.2 实例管理 `instance`
- CRUD 实例元数据；创建时可"**导入现有目录**"或"**从版本包新建**"。
- **共享原版 vs 每个实例独立目录**（两种模式，建议默认独立）：
  - *共享原版*：所有实例指向同一 `服务端本体/`（仅端口/世界不同）。简单，但
    `Settings.xml`／`ServerSetting.json` 全局共享，**无法同时以不同端口运行**，仅适合单实例。
  - *独立目录*（推荐）：每个实例一份完整拷贝（含 DLL/Content.scpak），各自
    `Settings.xml`/`ServerSetting.json`/`Worlds/`，可并行多开、端口独立。
    面板提供"复制基础包→初始化实例目录"的模板化流程。
- 生命周期：
  - `start` → **经 `node-pty` 创建伪终端** spawn `dotnet Survivalcraft.dll <args>`
    （`cwd=instance.dir`，`TERM=xterm-256color`），接入 PTY 数据流，注册到 Supervisor。
  - `stop` → 优先发 `/stop` 命令，超时后 `SIGTERM`，再 `SIGKILL`（可配优雅时长）。
  - `restart` → stop + start；`kill` → 强杀。
  - 崩溃检测：`exit` 事件 + 非预期退出 → 置 `crashed`，按 `autoRestart` 重启（带退避）。
- 端口分配：读取/改写实例 `Settings.xml` 的 `ServerPort`，选空闲端口。

### 5.3 控制台 `console`（WS + PTY）
- 连接：`ws://.../ws/console?instanceId=xxx`（握手校验 JWT）。
- 终端数据（原始 ANSI）：`pty.onData` → WS 帧 → 前端 `xterm.write()`；
  前端 `xterm.onData` → WS → `pty.write()`。支持 `terminal.resize`（`pty.resize`）。
- 服务端推送事件：
  - `console.raw`（原始终端字节流，供 xterm 渲染）
  - `console.line`（清洗后的纯文本行，供日志/检索/事件匹配）
  - `instance.status`（状态/pid/在线人数/CPU/内存/uptime）
  - `instance.stats`（周期采样，见 5.7）
  - `player.list`（来自 UDP 探测的人数/游戏模式等）
- 客户端操作：`console.write`/`console.input`(发送命令，自动补 `/`)；`terminal.resize`；限流防刷。
- 日志/终端回放：连接时下发最近 N 行纯文本 + 一屏 ANSI 快照（由后端维护回滚缓冲）。
- 前端用 **xterm.js**（仿真终端）+ 可切换的纯文本日志视图。

### 5.4 适配器 `adapter`
```ts
interface IServerAdapter {
  start(inst: Instance): Promise<void>;
  stop(inst: Instance, opts?: {gracefulMs?: number}): Promise<void>;
  sendCommand(inst: Instance, cmd: string): void;
  write(inst: Instance, data: string): void;               // 原始输入(含 Ctrl+C 等)
  resize(inst: Instance, cols: number, rows: number): void;
  on(event: 'raw'|'line'|'exit'|'status', cb): void;
}
```
- `PtyAdapter`（首期，硬性要求）：`node-pty` 伪终端实现，保留 TTY 语义。
- `PluginAdapter`：预留接口 + TODO（未来 C# 插件通过 TCP 上报结构化事件，
  可获得精确在线玩家列表、方块/背包事件等）。

### 5.5 配置管理 `config`
- 只读解析 + 安全写回（写回需**保留未知字段**、原子写、实例运行中提示风险）：
  - `Settings.xml`：`ServerPort`、`AllowLanConnection`、`LogMode`、`EnableMod` 等
    （XML，按 `Name`/`Value` 读写，保留未知条目与顺序）。
  - `ServerSetting.json`：世界参数（`WorldPath`/`WorldName`/`WorldMaxPlayers`/`GameMode`/
    `WorldPassword`/`WorldSeed`/`Autorun`/`AutoGenerateWorld`/`PVPEnabled`/`SeasonChanging` 等）。
  - `Configs/LevelConfig.json`：玩家权限 `{name: level}`（初始 `{}`）。
  - `Configs/BanConfig.json`：`BanUserList`/`BanUserIpList`/`BanIpList`。
  - `Configs/LimitConfig.json`：`WaterLength`/`MagmaLength`/`LimitBlockBreak`/`LimitExplode`。
  - `Plugins/Password.json`：`IsUse`/`DefaultPassword`/`PlayerPassword`（启用密码插件后生成；展示脱敏）。
- 类 MCSM：提供**表单化编辑器**，保存即写文件；权限/封禁/密码等也可通过
  控制台命令生效（`/paw` `/abp` `/rmg` 等），二者都支持。

### 5.6 存档与备份 `backup`
- 列存档（解析工作目录/存档目录，或解析 `/ls` 输出）。
- 备份：调用原生 `/backup`（若可用）+ 面板侧文件归档（`adm-zip`/`tar`）。
- 上传/下载/删除/恢复存档；定时自动备份（cron）。
- 存储于实例目录 `backups/`，记录 `BackupMeta`。

### 5.7 系统与探测 `system` `probe`
- 系统指标：CPU/内存/磁盘/网络（`systeminformation` 或 `os` + `pidusage`）。
- 服务端探测：实现 UDP `ServerInfo` 请求（`0x88` + ServerInfo id + `0x01`），
  解析回复得到版本/人数/上限/游戏模式/是否需要密码/时间。用于实例在线状态与大厅式信息。
- 探测既可用于"进程未托管但服务在跑"的只读监控。

### 5.8 版本下载 `version`
- `GET /api/version/releases`：拉取 Gitee releases 列表（缓存，含 tag/说明/资源直链）。
  - 注：Gitee 若对 API/下载有防盗链/验证码限制，则：
    1) 后端代理下载（带 Referer/UA，按需 Cookie）；
    2) 支持"**手动上传版本包**"作为兜底；
    3) 支持配置**自定义下载镜像/直链模板**。
- `POST /api/version/download`：按 tag+asset 下载 → 校验大小/可选校验和 →
  解压到目标实例目录（或暂存区）→ 调用 instance 安装流程。
- 识别服务端包命名（`服务端*.zip` / `Linuxx64` / `linux-arm64`）。

### 5.9 日志 `log`
- 服务端进程 stdout/stderr → pino + 按天/按大小轮转文件（对齐服务端"按天拆分日志"习惯）。
- 前端日志页 + 控制台内联检索。

---

## 6. 前端功能模块（Nuxt4 SPA）

| 页面 | 功能 |
|------|------|
| `/login` | 管理员登录（fuxsto Form/Input/Button） |
| `/` 概览 | 实例卡片网格（状态灯/版本/人数/CPU/内存）、一键启停重启、全局资源概览 |
| `/instances/[id]` | 标签页：**终端**(WS + xterm.js 仿真终端, 命令输入, resize) / **配置**(权限/封禁/密码/限制表单) / **存档与备份** / **玩家** / **日志** / **设置**(端口/启动参数/自动重启) |
| `/versions` | Gitee 版本列表、下载进度、安装到新实例或指定实例、手动上传 |
| `/settings` | 面板设置（备份策略、下载代理、主题、密码修改） |

- 设计基调：深色"控制台/运维"风格，fuxsto-design 单色 zinc + 语义色状态灯。
- 性能优化（SPA）：
  - `ssr:false` + 路由懒加载 + 组件按需导入；
  - 终端高频输出用 xterm.js 自带渲染管线 + WS 消息批量合帧（~16ms 节流）；
  - `definePageMeta` 预取实例摘要；
  - 大响应（版本列表/日志）分页或游标。
- 复用组件：`Table`(玩家/封禁/权限)、`Dialog`(确认)、`Drawer`(详情)、`Progress`(下载)、
  `Statistic`(CPU/内存)、`Tabs`、`Message`/`Notification`(操作反馈)、`Skeleton`(加载)。
- 终端组件：**xterm.js**（`@xterm/xterm` + `@xterm/addon-fit` + `@xterm/addon-search`），
  自适应容器尺寸（FitAddon → 触发 `terminal.resize`）。

---

## 7. 核心流程

### 7.1 启动实例
1. 校验元数据与目录（存在 `Survivalcraft.dll`）。
2. 选空闲端口；确保 `ServerPort/BroadcastPort` 生效（服务端设置文件或参数，待探明）。
3. **经 `node-pty` 创建伪终端** spawn `dotnet Survivalcraft.dll <args>`（注入
   `--sckey-token-local` 等），状态 `starting`；PTY 输出接入 raw/line 双通道。
4. 匹配启动成功特征日志（如监听/进入主循环）→ `running`；超时判失败。
5. 启动 UDP 探测与指标采样，推送 WS。

### 7.2 停止/重启
- `stop`：经 PTY 写入 `/stop` 命令 → 等待退出（默认 15s）→ `SIGTERM` → `SIGKILL`。
- `restart`：stop 后 start；可用服务端自带"3秒后重启"逻辑配合（待确认命令语义）。
- 强制中断：`write('\x03')`（Ctrl+C）作为交互式兜底。

### 7.3 版本安装
- 新实例：创建目录 → 下载解压服务端包 → 写入元数据（端口/参数）→ 生成默认配置。
- 已存在实例：备份现有 → 解压覆盖（保留 Configs/Plugins/存档）→ 提示重启。

---

## 7.5 PTY 专项验证要点（硬性）
- 启动后终端必须能看到**彩色/ANSI 输出**与增强控制台界面（`--enhanced`），
  而非降级/乱码。
- 键盘热键（如 `[keyhelp]`/`[ls]`/`[save]` 对应按键）经 xterm → WS → PTY 生效。
- `terminal.resize` 改变 PTY 尺寸后，服务端重绘/换行正确（`stty size` 类行为）。
- `Ctrl+C`（`\x03`）在游戏中进程的行为符合预期（不误杀或可按需触发）。
- Windows 下验证 `node-pty` 的 ConPTY 可用；Linux 下验证 `pty` 可用。

---

## 8. MCSM 代码复用与贡献标注

- 允许参考/移植 MCSM（`MCSManager/MCSManager`，MIT）的工具函数，典型候选：
  - 终端输出解析/ANSI 处理、路径安全拼接、进程优雅退出、端口占用检测、
    日志轮转、文件管理器基础操作。
- **规范**：
  - 仅移植与本项目兼容的**小颗粒工具**，重写为 TS 并适配本项目类型；
  - 顶部注明来源：
    ```ts
    // Adapted from MCSManager (MIT) - https://github.com/MCSManager/MCSManager
    // Contributors: ... ; Modifications: ...
    ```
  - 仓库根 `NOTICE`/`README` 增加第三方许可与贡献段落；
  - 保留 MCSManager 的 MIT LICENSE 文本于 `THIRD_PARTY_LICENSES/`。
- **实施现状（2026-10-01）**：许可框架已就位——仓库根 `THIRD_PARTY_LICENSES/` 已含
  MCSManager MIT LICENSE 文本，`README` 已设"第三方许可"段落。截至 M2，
  **尚未移植任何 MCSM 代码**（终端解析 `LineSplitter`/`stripAnsi`、`FileTailer`、
  端口分配 `allocatePort`、优雅停止等均为本项目独立实现）；后续如发生移植，
  必须在上表所列位置补 `Adapted from MCSManager` 标注并登记贡献。

---

## 9. 安全
- 单管理员：强密码 + bcrypt + HttpOnly/SameSite Cookie + JWT 过期与刷新。
- 命令注入防护：实例命令仅经 PTY 写入（不经过 shell）；进程参数白名单校验；禁止 shell 拼接。
- 路径遍历防护：所有文件操作基于 `instance.dir` 做 `path.resolve` 前缀校验。
- WS 握手鉴权；REST 限流（登录、下载、命令发送）。
- 下载：校验 host（gitee 域名白名单）、大小上限、可选校验和。
- 审计日志：登录、启停、命令、配置修改、备份、下载。

---

## 9.5 测试门禁（硬性要求 · 每阶段必过）

> **规则**：每个里程碑 M0–M7 结束时，必须执行本节对应的**功能验证清单**，
> 全部通过并留下**证据（命令输出 / 截图 / 日志片段 / 测试报告文件）**后，才允许进入下一阶段。
> 不得以"功能简单""只做单测/冒烟"为由跳过。禁止在未验证时声称阶段完成。

### 9.5.1 测试分层
| 层 | 手段 | 范围 |
|----|------|------|
| 单元测试 | Vitest | 纯函数（解析、端口分配、配置读写、探测包编解码） |
| 集成测试 | Supertest + 测试容器 | REST/WS 接口、鉴权、错误路径 |
| E2E/功能测试 | Playwright + 真实服务端进程 | 关键用户旅程（登录→建实例→启服→终端交互→备份） |
| 真实冒烟 | 手动/脚本对真实 `Survivalcraft.dll` | PTY 终端、命令回执、探测、文件落盘 |

### 9.5.2 每阶段"功能验证清单"（必须逐条实际运行）

**M0 脚手架门禁** ✅（2026-09-30 全部通过，证据 `docs/test-reports/M0.md`）
- [x] `pnpm dev` 前后端均可启动，前端页面加载无报错（浏览器 console 无红错）。
- [x] fuxsto-design 组件真实渲染（如 Button/Dialog 可见且样式正确）。
- [x] `pnpm test` 运行通过；`lint`/`typecheck` 无错。

**M1 认证+实例+启停门禁** ✅（2026-10-01 全部通过，证据 `docs/test-reports/M1.md`）（**必须真实启服**）
- [x] 未登录访问受限 API/WS 被拒（401/403）。
      > 实测以 REST 401 覆盖（未登录 `GET /instances`、`GET /auth/me` → 401）；
      > WS 网关属 M2，届时接入同一 `JwtAuthGuard`。
- [x] 正确密码登录成功并持有 Cookie；错误密码失败。
- [x] 从版本包/基础包**创建独立实例目录**（含 `Survivalcraft.dll`/`Content.scpak`/
      `Settings.xml`/`ServerSetting.json`），面板可改 `ServerPort` 与 `WorldMaxPlayers`。
      > 实测 `source=template`（模板拷贝）与 `source=upload`（上传 zip 22.8MB 解压）两条路径。
- [x] 点击**启动**，服务进程真实拉起（`tasklist`/`ps` 可见 `dotnet`/`Survivalcraft`）。
- [x] 服务端日志出现 `开启服务器成功，端口 <分配端口>` 且与配置一致。
      > PTY stdout + `tail Bugs/Game.log` 双通道消费；实测 `[StartServer]开启服务器成功，端口 28898`。
- [x] **停止/重启/强杀**均真实生效，状态机正确流转（stopped→starting→running→stopping→stopped）。
- [x] 崩溃场景：手动 kill 进程 → 面板识别 `crashed`，`autoRestart` 生效。
      > 外部 `taskkill` → `crashed` → 退避 5s 自动重启至 `running`。
- [x] 可并行启动**两个不同端口**的实例，互不干扰。
      > 实测两个 `dotnet.exe` 各绑定独立 UDP 端口（28961 / 28962）。
- **M1 实测说明**：
  - 交付：`apps/api` 新增 `auth`/`server-config`/`adapter`/`instance`（`PtyAdapter`+`FileTailer`）、
    `apps/web` 新增登录/概览/详情骨架，`packages/shared` 扩展 auth 类型并改为 **ESM+CJS 双产物**。
  - 验证：`pnpm test` **38 passed**（api 33 / web 3 / shared 3）；`lint` 0 错；`typecheck` 0 错；
    `pnpm build`（shared→api→web）成功；前端经 Nitro 代理登录并下发 HttpOnly Cookie 已实测。
  - 工程备注：Windows ConPTY 不接受裸命令名（须 `resolveExecutable` 解析绝对路径）；
    Vitest 需 `unplugin-swc` 产出 `emitDecoratorMetadata` 以支持 NestJS DI；
    服务端无 SCKey 令牌时输出 `使用游客模式启动`（不影响本地开服）。

**M2 终端门禁** ✅（2026-10-01 全部通过，证据 `docs/test-reports/M2.md`）（**必须真实 PTY**）
- [x] 浏览器 xterm.js 中看到**真实服务端启动日志**（非空、非乱码）。
- [x] 输入 `/help` 回车后，**终端回显命令帮助**（真实回执）。
- [x] ANSI 颜色/清屏/光标行为正确。
- [x] 调整浏览器窗口 → `terminal.resize` 后服务端重绘正确。
- [x] 多标签页观察同一实例时输出一致；命令行输入不交叉错乱。
- [x] 连接时能回放最近历史行。
      > 双通道回滚缓冲：5000 行纯文本 + 64KB ANSI 快照；`GET :id/history` 与
      > `console.history` 握手帧均已实测。

**M3 探测+指标门禁** ✅（2026-10-01 全部通过，证据 `docs/test-reports/M3.md`）（**必须真实探测 + 真实采样**）
- [x] 独立运行 UDP 探测脚本，对运行中的实例返回正确
      `version/playerCount/maxCount/gameMode/needPassword`（与游戏内一致）。
      > 门禁脚本独立实现协议：向实例 `ServerPort` 单播 `[0x09][rawDeflate(0x88,0x00,0x01)]`；
      > 实测 `version=x26.06.19 players=0/20 gameMode=2(Survival) needPass=false ping=38ms`，
      > `maxCount=20` 与实例配置一致。
- [x] 概览卡片 CPU/内存/磁盘数据来自真实采样且随负载变化。
      > `GET /system/stats`（os + statfs）CPU 实测 75.95→18.58；实例进程
      > `instance.stats` cpu/mem 经 WS 推送（mem≈310MB）；概览卡片/详情页消费。

**M4 配置门禁** ✅（2026-10-01 全部通过，证据 `docs/test-reports/M4.md`）
- [x] 在面板改 `ServerPort`、`WorldMaxPlayers`、`Autorun` 并保存到 `Settings.xml`/
      `ServerSetting.json`；重启后服务端读取到新值（日志/探测核实）。
      > 实测 `ServerPort 28940→28941`：`Settings.xml`/`ServerSetting.json` 落盘，
      > 重启后状态机日志 `开启服务器成功，端口 28941`，独立 UDP 探测新端口在线。
- [x] 在面板新增/删除一条权限、一条封禁、改一次密码、调一次限制并保存。
      > `LevelConfig`/`BanConfig`/`LimitConfig`/`Password.json` 均真实落盘。
- [x] **重新读取文件**确认已落盘且**未知字段/未管理条目未被破坏**；服务端重启后生效。
      > `BanConfig.Unknown`、`LimitConfig.CustomField`、`ServerSetting.Unknown_Field` 原样保留。
- [x] 非法输入（错误 JSON/XML 结构或类型）被拒绝并有清晰错误提示。
      > `{"X":-1}`→400、`BanUserList:"oops"`→400、未知分区→400。
- **M4 实测说明（重要）**：
  - **文件口径**：编译产物 `Survivalcraft.dll` 中真实存在的是
    `Settings.xml` / `ServerSetting.json` / `Configs/{Level,Ban}Config.json`；
    `Configs/LimitConfig.json` 与 `Plugins/Password.json` **未编译进服务端**，
    面板仍管理并标注「源码/遗留」（`effective=false`）。
  - **世界参数生效口径**：探测回执 `maxCount` 取自世界数据
    `WorldSettings.MaxOnlinePlayerCount`，**非** `ServerSetting.WorldMaxPlayers`；
    后者仅在**新建世界**时播种。故门禁以「新端口探测响应」核实新配置读取。
  - **运行中写保护**：`Settings.xml` 允许热改；世界/权限/封禁/密码/限制分区运行中返回 409。
  - 单测/集成 **80 passed**（api 66 / web 11 / shared 3）；`lint`/`typecheck`/`build` 全绿。

**M5 存档与备份门禁** ✅（2026-10-01 全部通过，证据 `docs/test-reports/M5.md`）
- [x] 列出真实存档；执行一次备份后 `backups/` 出现归档且大小合理。
      > `GET :id/worlds` 实测 `World files=2 size=6204`；`POST :id/backups` 落盘
      > `<uuid>.tar.gz` 3908B，元数据 `size` 与归档字节一致。
- [x] **恢复**备份后服务端可正常加载该存档启动。
      > 篡改 `Project.json` → 恢复（自动 `pre-restore` 快照）→ 数据逐字节还原 →
      > 真实启服进入 `running`（port 28950）。
- [x] 定时备份按时触发（用短周期验证一次）。
      > `BACKUP_INTERVAL_MS=15000` 实测每 15s 生成 `auto` 备份（连续 3 次）。

**M6 版本门禁** ✅（2026-10-01 全部通过，证据 `docs/test-reports/M6.md`）（**必须真实联网下载 + 真实启服**）
- [x] 能从 Gitee 拉到 releases 列表（或明确走兜底路径并在界面提示）。
      > `GET /api/versions/releases` 实测 `releases=2 host=windows fallback=false`；
      > 拉取失败降级到本地缓存并返回可读 `notice`（组件测试覆盖）。
- [x] 下载服务端包 → 解压到新实例目录 → **该实例可成功启动**。
      > 真实下载 `[服务端]SCNETx26.06.19z1.zip`（23.1MB）→ 解压安装 → PTY 启动进入
      > `running`（port 28889），日志 `开启服务器成功，端口 28889`。
- [x] 手动上传版本包兜底路径可用。
      > `POST /api/versions/upload`（multipart）新建实例，目录含 `Survivalcraft.dll`；
      > 覆盖既有实例保留 `Configs`（哨兵文件 `sentinelKept=true`）。
- [x] 下载失败/中断时错误可读，且有重试/续传或明确失败。
      > 缺资源 → `task.phase=failed` + `error=版本 x26.06.19 不包含资源: ...`；
      > 另有 HTTP 非 2xx / 体积超限 / 下载不完整 / 缺二进制等明确错误。

**M7 打磨门禁** ✅（2026-10-01 全部通过，证据 `docs/test-reports/M7.md`）
- [x] 全量测试通过；关键路径 Playwright E2E 通过。
      > `pnpm test` **134 passed**；`pnpm test:e2e` Playwright **2/2 PASS**——真实启服 →
      > 终端 `/help` 回执 → 停止 → 备份 → 审计可见；连续错误登录触发 429。
- [x] 限流/审计生效（可观测到审计记录）。
      > 登录限流 5/分（429 实测）；全局 300/分；下载/命令限流；`GET /api/audit` 返回
      > `auth.login(.failed)`/`instance.*`/`backup.*`/`console.command`/`settings.update` 等记录（含 IP/时间戳）。
- [x] 打包/运行方式（Docker 或 node）在本机真实跑通并完成一次完整旅程。
      > **node 方式**：`pnpm build` → `node apps/api/dist/main.js` + `nuxt preview`；
      > Playwright 真实全链路一次跑通（登录→建实例→启服→终端→备份→审计）。

### 9.5.3 证据留存
- 每阶段产出 `docs/test-reports/M<n>.md`：列出执行命令、预期/实际结果、证据（日志/截图路径）。
- 未通过项必须显式标注为**未完成/阻塞**，不得隐去。

---

## 10. 里程碑（阶段交付 · 每阶段以 §9.5 门禁收尾）

### M0 脚手架（0.5–1 天）✅ 已完成
- pnpm workspace；Nuxt4 SPA + fuxsto-design/Tailwind4 跑通；NestJS/Express 骨架；
- shared 类型包；.env、日志、错误处理基线；**测试框架接入（Vitest/Playwright）**。
- **门禁**：§9.5.2 M0。
- **状态（2026-09-30 完成）**：
  - 交付：`apps/web`（Nuxt4 SPA, ssr:false）、`apps/api`（NestJS12）、`packages/shared`；
    根 workspace、`.env.example`、pino 日志、zod 环境校验、统一错误包络、`/api/health`、CORS。
  - 前端：Tailwind4 + fuxsto-design 真实渲染（`Button`/`Dialog`）、Pinia、`/api` 经
    Nitro `routeRules.proxy` 代理后端。
  - 验证：`pnpm dev` 三进程并行启动（web:3000 / api:3001）；`pnpm test` **11/11 通过**；
    `lint` 0 错；`typecheck` 0 错（仅上游 Volar 告警）。
  - 证据：`docs/test-reports/M0.md`。
  - 工程备注：pnpm 需在 `pnpm-workspace.yaml` 显式允许 `esbuild`/`unrs-resolver` 构建脚本；
    `@tailwindcss/vite` 锁 `~4.1.18` 以匹配 Nuxt 内置 Vite 7。

### M1 认证 + 实例 CRUD + PTY 启停（2–3 天）✅ 已完成
- 登录/守卫；实例列表/详情；**独立实例目录初始化**（模板拷贝 + 端口分配）；
  **PtyAdapter(node-pty)**；start/stop/restart/kill；状态机。
- 前端：登录、概览、实例详情骨架、启停按钮。
- **门禁**：§9.5.2 M1（含真实启服 + 双实例并行）。
- **状态（2026-10-01 完成）**：
  - 交付：`apps/api` 新增 `auth`（JWT HttpOnly Cookie + 全局守卫）、`server-config`
    （`Settings.xml`/`ServerSetting.json` 保留未知字段原子写）、`adapter`（`IServerAdapter`
    + `PtyAdapter` + `LineSplitter`）、`instance`（`InstanceStore` JSON 持久化、
    `InstanceSupervisor` 状态机/崩溃/自动重启、`FileTailer` 双通道日志、`InstanceService`
    CRUD/模板拷贝/zip 上传/导入/端口分配/配置写回）；`apps/web` 新增登录页/概览卡片/
    实例详情骨架/启停按钮/`useApi`/auth 中间件/Pinia；`packages/shared` 新增 `auth.ts`
    并改为 **ESM+CJS 双产物**（修复 CJS 后端运行时 `require`）。
  - 验证：真实启服（`tasklist` 双 `dotnet.exe`，UDP 28961/28962 各绑端口）；
    状态机 `stopped→starting→running→stopping→stopped`（服务端权威日志）；
    外部 `taskkill` 识别 `crashed` 且 `autoRestart` 退避重启；`pnpm test` **38/38 通过**；
    `lint` 0 错；`typecheck` 0 错；`build` 成功。
  - 证据：`docs/test-reports/M1.md`。
  - 工程备注：Windows ConPTY 需 `resolveExecutable` 解析绝对路径；Vitest 需
    `unplugin-swc` 产出 `emitDecoratorMetadata`；`SERVER_TEMPLATE_DIR` 相对后端 cwd
    （默认 `../../../SurvivalcraftNet/服务端本体`）。

### M2 WebSocket 终端 + 状态推送（2 天）✅ 已完成
- WS 网关 + PTY 桥接；xterm.js 终端；raw/line 双通道；命令输入；状态/指标推送；
  回滚缓冲与回放；resize。
- **门禁**：§9.5.2 M2（含真实 PTY 交互）。
- **状态（2026-10-01 完成）**：
  - 交付：`apps/api` 新增 `console`（`ConsoleGateway`（`@nestjs/platform-ws`，路径
    `/ws/console`，HttpOnly Cookie 握手鉴权、多观察者可写、命令队列化 + 16ms 合帧）、
    `ConsoleService`（订阅 Supervisor raw/line/status 并扇出）、`RollbackBuffer`
    （5000 行 + 64KB ANSI 环形缓冲）、`GET :id/history`）；`main.ts` 接入 `WsAdapter`；
    `apps/web` 新增 `useConsoleSocket`（复用 Cookie，`ws://host/ws/console`）、
    `components/console/TerminalView.vue`（`@xterm/xterm` + fit + search，动态导入避免
    SSR，`onData → write`、`onResize → terminal.resize`），实例详情页终端标签替换占位；
    `packages/shared` 扩展 `ws.ts`（history/hello/error 帧 + 客户端消息类型 + 缓冲常量）。
  - 验证：真实门禁脚本 `apps/api/scripts/m2-gate.cjs` **15/15 PASS**——真实启服
    （日志 `开启服务器成功，端口 28887` → `Entered screen "Game"`）、WS 收到 raw/line/
    instance.status=running、`/help` 回执为真实命令帮助（`[可用命令] 第 1/3 页`）、
    第二个观察者收到 49 行历史 + 47 帧实时输出、`terminal.resize` 正常、`history` 接口
    非空；`pnpm test` **52/52 通过**（api 43 / web 6 / shared 3）；`lint` 0 错；
    `typecheck` 0 错；`build` 成功（xterm CSS 已打包）。
  - 工程备注：`@nestjs/websockets`+`@nestjs/platform-ws`+`ws` 需显式安装并
    `app.useWebSocketAdapter(new WsAdapter(app))`；面板托管实例创建时 `autoRun` 默认
    `true`（`Autorun=true`）以无人值守直接进世界，否则停在 Play 界面不打印启动特征；
    enhanced 控制台会整屏重绘（ANSI 光标控制），raw 通道交 xterm 渲染即为正确画面。

### M3 探测 + 指标（1 天）✅ 已完成
- UDP `ServerInfo` 探测；在线人数/游戏模式/密码；CPU/内存/磁盘采样；概览卡片。
- **门禁**：§9.5.2 M3。
- **状态（2026-10-01 完成）**：
  - 交付：`packages/shared` 新增 `probe.ts`（协议常量）+ 扩展 `ProbeResult`/`SystemStats`/WS 事件；
    `apps/api` 新增 `probe/`（`server-info.protocol`、`udp-probe`、`metrics.service`、
    `probe.controller`、`probe.module`）与 `system/`（`system.service`、`process-sampler`、
    `system.module`）；`console.gateway` 转发 `instance.stats`/`player.list`/`system.stats`；
    `apps/web` 新增 `useSystemStats`、扩展 `useConsoleSocket`、概览指标条与卡片/详情页实时字段。
  - 验证：真实门禁脚本 `apps/api/scripts/m3-gate.cjs` **12/12 PASS**——独立 UDP 探测
    （`version=x26.06.19 players=0/20 mode=Survival`）、REST `/instances/:id/probe` 一致、
    WS `player.list`+`instance.stats`、系统指标随负载变化、停止后清理；`pnpm test` **62/62 通过**
    （api 53 / web 7 / shared 3）；`lint` 0 错；`typecheck` 0 错；`build` 成功。
  - 证据：`docs/test-reports/M3.md`。
  - 工程备注：探测须正确包裹 LiteNetLib `UnconnectedMessage` property 字节（`0x09`）并经
    raw-deflate；`BroadcastPort` 在编译产物中不存在，故采用 `ServerPort` 单播；Windows 进程采样
    用 `wmic`（回退 PowerShell），Linux 用 `/proc`。

### M4 配置管理（1–2 天）✅ 已完成
- `Settings.xml`/`ServerSetting.json` + `Configs/{Level,Ban,Limit}Config.json` +
  `Plugins/Password.json` 表单化读写（保留未知字段/条目、原子写）。
- **门禁**：§9.5.2 M4。
- **状态（2026-10-01 完成）**：
  - 交付：`packages/shared` 新增 `config.ts`（`ConfigBundle`/`ConfigSection`/分区类型/脱敏常量）
    并扩展 `UpdateInstanceInput`；`apps/api` 扩展 `ServerConfigService`（JSON 分区通用读写、
    结构校验、密码脱敏与占位符合并、`readBundle` 汇总），新增 `instance/config.controller.ts`
    （`GET /instances/:id/config`、`PUT /instances/:id/config/:section`）与运行中写保护；
    `apps/web` 新增 `components/instance/ConfigPanel.vue`（6 分区表单 + 遗留徽标）并在实例详情页
    新增「配置」标签、`useApi.put`。
  - 验证：真实门禁脚本 `apps/api/scripts/m4-gate.cjs` **14/14 PASS**——权限/封禁/限制/密码
    真实落盘且未知字段保留、世界参数写入并重启后新端口探测在线、运行中写保护 409、非法输入 400；
    `pnpm test` **80 passed**（api 66 / web 11 / shared 3）；`lint` 0 错；`typecheck` 0 错；`build` 成功。
  - 证据：`docs/test-reports/M4.md`。
  - 工程备注（实测）：编译产物仅含 `Settings.xml`/`ServerSetting.json`/`Configs/{Level,Ban}Config.json`；
    `LimitConfig.json`/`Password.json` 为源码/遗留（面板管理并标注）；探测 `maxCount` 来自世界数据
    `WorldSettings.MaxOnlinePlayerCount`，`ServerSetting.WorldMaxPlayers` 仅首建世界时播种。

### M5 存档与备份（1–2 天）✅ 已完成
- 存档列表；手动/定时备份；上传/下载/恢复；原生 `/backup` 整合。
- **门禁**：§9.5.2 M5。
- **状态（2026-10-01 完成）**：
  - 交付：`packages/shared` 新增 `backup.ts`（`BackupMeta`/`WorldInfo`/`BackupType`/
    `CreateBackupInput`/`RestoreBackupInput`）；`apps/api` 新增 `backup/`
    （`archive.ts` 流式 tar.gz、`backup.service.ts` 列存档/创建/恢复+预快照/删除/
    保留淘汰/定时任务、`backup.controller.ts`、`backup.module.ts`）；`config/env.ts`
    新增 `BACKUP_INTERVAL_MS`/`BACKUP_KEEP`；新增依赖 `tar`；`apps/web` 新增
    `components/instance/BackupPanel.vue` 并在实例详情页「存档与备份」标签接入。
  - 决策（用户确认）：归档 **tar.gz 流式**；范围 **世界目录 + 配置**；**仅面板文件级**
    （不整合未证实的原生 `/backup`）；定时用 **setInterval + 环境变量**；恢复前
    **自动预恢复快照**。
  - 验证：真实门禁脚本 `apps/api/scripts/m5-gate.cjs` **10/10 PASS**——真实列存档、
    备份落盘、篡改→恢复→数据还原、恢复后真实启服 `running`、运行中 409、预恢复快照、
    删除归档；短周期定时备份实测每 15s 触发；`pnpm test` **94 passed**
    （api 76 / web 15 / shared 3）；`lint` 0 错；`typecheck` 0 错；`build` 成功。
  - 证据：`docs/test-reports/M5.md`。
  - 工程备注：`BackupModule` 须在 `app.module.ts` 中**先于 `InstanceModule`** 注册，
    否则 `InstanceController` 的 `@Post(':id/:action')` 会吞噬 `/instances/:id/backups`；
    运行中禁止备份以保证世界数据快照一致性；恢复采用解压覆盖（不删除未知配置）。

### M6 版本下载与安装（1–2 天）✅ 已完成
- Gitee releases 拉取/缓存；下载（代理/镜像/手动上传兜底）；解压安装。
- **门禁**：§9.5.2 M6。
- **状态（2026-10-01 完成）**：
  - 交付：`packages/shared` 新增 `version.ts`（`ReleaseInfo`/`ReleaseAsset`/`DownloadTask`/
    `CreateDownloadInput`/`UploadInstallInput`/`VersionSourceInfo` 等）并将 `InstanceSource`
    扩展 `'version'`；`apps/api` 新增 `version/`（`version-source`（asset 识别/评分）、
    `gitee-source`（Gitee API + `GITEE_MIRROR` 镜像）、`version.service`（缓存+失败降级）、
    `download.service`（流式下载+进度+解压安装+上传兜底+任务机）、`version.controller`、
    `version.module`）；`instance.service` 新增 `createFromVersion`/`overwriteInstall`
    与配置播种；`server-config.service` 新增 `ensureSettingsXml`/`ensureServerSetting`；
    `config/env.ts` 新增 `VERSION_SOURCE`/`VERSION_CACHE_MS`/`VERSION_MAX_BYTES`；
    `apps/web` 新增 `pages/versions.vue` + `components/version/VersionsPanel.vue`。
  - 验证：真实门禁脚本 `apps/api/scripts/m6-gate.cjs` **13/13 PASS**——真实拉 Gitee releases、
    真实下载 23.1MB 服务端包、解压安装到新实例并真实启动 `running`、手动上传兜底、
    覆盖安装保留 Configs、失败错误可读；`pnpm test` **124 passed**（api 102 / web 19 / shared 3）；
    `lint` 0 错；`typecheck` 0 错；`build` 成功。
  - 证据：`docs/test-reports/M6.md`。
  - 工程备注（实测）：下载的版本包无 `Settings.xml`/`ServerSetting.json`/世界存档，安装须播种
    `ServerPort`+`Autorun=true` 且**世界缺失时 `AutoGenerateWorld=true`**（否则「自动运行存档
    不存在」退出）；同一 release 存在 `PocketSurvival`（C++/`PSTerminal.exe`）变体，按
    `serverPackageScore` 优先托管式 `Survivalcraft.dll` 包；Gitee 直链 302 跳转到临时 token
    链接，后端代理跟随重定向并带 Referer/UA。

### M7 打磨与安全（1–2 天）✅ 已完成
- 权限/限流/审计；错误与崩溃自动重启；暗色主题；性能回归；文档。
- **门禁**：§9.5.2 M7。
- **状态（2026-10-01 完成）**：
  - 交付：`packages/shared` 新增 `audit.ts` / `settings.ts`；`apps/api` 新增 `audit/`
    （JSONL 追加 + 按天轮转 + 查询接口，全局模块）、`settings/`（运行期可调设置 + 改密，全局模块）、
    接入 `@nestjs/throttler`（全局 + 登录/下载/命令 `@Throttle`）与 WS 命令限速；
    auth/instance/config/backup/version/console 全链路审计埋点；`auth.service` 运行期改密持久化；
    M6 遗留：下载 **Range 断点续传 + 重试**、覆盖安装 **pre-install 快照自动清理**；
    `apps/web` 新增 `/settings`、`/audit` 页面与 `useSettings`、导航项，终端加纯文本镜像，
    WS 直连后端（`plugins/api-base`）。
  - **重要修复**：`fuxsto-design@1.0.4` 的 `Dialog` 默认插槽/正文**恒不渲染**（含命令式 `content`）；
    新增自研 `AppDialog`/`dialog.ts`（API 对齐）并全站替换，修复原「对话框无内容」隐性缺陷。
  - 验证：`pnpm test` **134 passed**（api 109 / web 22 / shared 3）；`lint` 0 错；`typecheck` 0 错；
    `build` 成功；`apps/api/scripts/m7-gate.cjs` **12/12 PASS**；Playwright E2E
    `apps/web/e2e/m7-journey.spec.ts` **2/2 PASS**（真实启服 → 终端 `/help` 回执 → 备份 → 审计；
    连续错误登录触发 429）。
  - 证据：`docs/test-reports/M7.md`。

---

## 11. 开放问题（待确认）

> **已由实测确认**：① 后端框架＝**NestJS**；② 端口＝`Settings.xml` 的 `ServerPort`；
> 世界参数＝`ServerSetting.json`；存档＝`Worlds/<name>/`；日志＝`Bugs/Game.log`；
> 限制配置＝`Configs/LimitConfig.json`；③ **探测协议**＝向 `ServerPort` 单播
> `[0x09][rawDeflate(0x88,0x00,0x01)]`，回执含 version/人数/上限/模式/密码/时间（M3 实测）。

1. **`BroadcastPort` 来源**（M3 实测结论）：编译产物 `Survivalcraft.dll` 中**不存在**
   该键/常量（仅 `ServerPort`），源码引用 `SettingsManager.BroadcastPort` 但未编译进去。
   因此 LAN 广播发现不可用；面板改为**单播探测 `ServerPort`**，功能不受影响。
   如需发现「未托管但局域网在跑」的实例，需另行抓包确定广播端口。
2. **`--sckey-token-local` 语义**：是否所有面板托管实例都必须带？是否与联机号绑定？
   （面板默认携带；实测无令牌时服务端输出「使用游客模式启动」，不影响本地开服。）
3. **`/stop` 是否真正退出进程**：源码 `CmdStop` 为空实现，但编译版有"关闭服务器并退出"
   文案；需实跑确认是否需要额外信号/是否直接 `Environment.Exit`。
4. **`Autorun` 无人值守**：面板托管时是否应默认 `ServerSetting.json.Autorun=true`
   让服务端跳过交互直接进世界。
5. **Gitee 下载限制**：直链是否存在验证码/防盗链；决定代理与手动上传策略权重。
6. **多实例资源隔离**：是否需要容器化（Docker）？或纯进程（工作目录）隔离即可。
7. **是否需要群组服（GroupServer）能力**（发布日志提到群组服）。
8. **打包分发**：面板本身用 Docker / 二进制(pkg) / 直接 node 运行？

---

## 12. 验收标准（Definition of Done）
- 单管理员登录，未登录无法访问任何 API/WS。
- 可创建/导入多个实例并在同一面板管理；启停重启稳定，崩溃可识别/可选自动重启。
- **服务端经 PTY 运行，xterm.js 终端真实还原 ANSI/增强控制台/热键/resize。**
- 终端 WebSocket 实时输出，命令可下发并回显；大日志/高频输出不卡顿。
- 概览展示每实例状态/人数/CPU/内存；UDP 探测可用。
- 配置（`Settings.xml`/`ServerSetting.json`/`Configs/*.json`/`Plugins/Password.json`）
  可表单化读写且不破坏原格式。
- 存档可列、可备份、可恢复；支持定时备份。
- 可从 Gitee 拉取版本、下载并安装服务端到实例（含手动上传兜底）。
- 移植 MCSM 的代码均有来源与贡献标注，许可合规。
- **每个里程碑均通过 §9.5 测试门禁并留存 `docs/test-reports/M<n>.md` 证据。**

---

*本计划基于仓库 `master` 源码、`服务端本体/` 编译产物字符串、一次真实运行生成的
运行时布局（`Settings.xml`/`ServerSetting.json`/`Configs/`/`Worlds/`/`Bugs/Game.log`）
与 Gitee Releases 页面整理。*
