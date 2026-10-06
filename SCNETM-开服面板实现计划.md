# SCNETM — 生存战争2 联机版开服面板（Go 实现）实现计划

> 目标：用 Go 实现一个类 MCSM（Minecraft Server Manager）的 Web 开服面板，用于管理 SurvivalcraftNet（生存战争2 联机版）**纯服务端**实例：创建/启停实例、配置世界、查看日志与状态、管理存档、备份、多实例隔离、用户与权限。
>
> 本文中的技术结论均来自对官方发行包与源码的**实际解包验证**，验证命令与原始输出见 [附录 A](#附录-a验证证据)。未验证项已明确标注为"待验证"。

---

## 1. 结论先行（TL;DR）

| 问题 | 结论 | 依据 |
|---|---|---|
| 服务端能否在 Linux 无头运行？ | **能，已实测**。无需 GUI/X11/Wine | `使用游客模式启动` → `[StartServer]开启服务器成功，端口 28887` → `Entered screen "Game"` |
| 服务端是什么形态？ | **.NET 10 控制台应用**，`Framework-dependent`，需装 `dotnet` 运行时 | `Survivalcraft.runtimeconfig.json`：`tfm=net10.0`，仅 `Microsoft.NETCore.App`（**无** WindowsDesktop） |
| 有无独立无头服务端？ | **有**。与客户端包是**两个不同的发行物** | `客户端` 包依赖 `Microsoft.WindowsDesktop.App`；`服务端` 包无此依赖 |
| 自动化开服怎么触发？ | 改 `ServerSetting.json` 的 `Autorun=true`（可配 `AutoGenerateWorld`） | 已实测：日志 `已自动生成存档目录 app:/Worlds/...` + `开启服务器成功，端口 28887` |
| 控制通道（发指令/收日志）？ | **日志：PTY 读取（保色，待 V0-1 运行时验证）**；**指令：PTY 写 stdin**，`command-fifo` 作为备选 | 服务端有 `EnhancedTerminalLogSink`（ANSI 彩色）；`CmdManager`/`AbstractProcessCmd` 源码显示终端来源的指令回复走 `Console.WriteLine` |
| 关键配置在哪？ | `ServerSetting.json`（开服）、`Settings.xml`（端口等）、`Configs/*.json`（插件·闭源格式）、`ModSettings.xml`、`Worlds/<dir>/Project.json`（存档） | 首次运行自动生成，已逐个读取 |
| **存档目录 vs 世界名** | **目录由 `WorldPath` 决定，`WorldName` 只是显示名**（已实测证明） | 设 `WorldPath=app:/Worlds/MyWorldA` + `WorldName=ShowNameX` → 实建目录 `MyWorldA`，日志显示 `WorldName=ShowNameX`；见 §2.7 |
| 面板语言/架构选型 | **Go 单体 + 进程管理 + SQLite + REST/WS + 内嵌前端（Vue 3 + fuxsto-design）** | 见 §4 |
| 部署目标平台 | **仅 Linux**（不实现 Windows 托管） | 已确认 |
| 用户模型 | **单用户开箱即用**，但预留多用户/多租户扩展空间 | 已确认 |

### 1.1 已确认的决策（2026-10 与需求方确认）

| # | 决策项 | 结论 |
|---|---|---|
| D1 | **终端形态** | **优先仿真终端（PTY）**，保证**彩色输出**。不使用 `script`/重定向降级路径作为主方案 |
| D2 | **平台范围** | **仅 Linux**。进程管理、信号、部署均按 POSIX 实现，不做 Windows 分支 |
| D3 | **用户模型** | **暂只考虑单人使用**；但数据模型与中间件**保留多用户/多租户扩展空间**（表结构、权限中间件、实例归属字段现在就留好） |
| D4 | **UI 组件库** | **`fuxsto-design`**（Vue 3 + Tailwind CSS v4，单色 zinc 风格，82 个导出组件，暗色模式） |
| D5 | **GameMode 映射** | 暂用推断值：`0=Creative, 1=Harmless, 2=Survival, 3=Challenging, 4=Cruel, 5=Adventure, 6=Mirror`（见 §6.2 证据与校验方法） |
| D6 | **交付范围** | 完整 MVP 到可上线（含分阶段里程碑与验收） |

**最大的两个技术风险**（必须在阶段 0 用 1~2 天打掉）：
1. **指令通道**：`command-fifo` 的确切路径/启用方式尚未在生产环境实测（本机沙箱禁止创建 PTY，无法验证交互式输入）。→ 任务 `V0-1`
2. **服务端停机的优雅性**：`CmdStop` 在当前源码中 `ProcessCmd()` 是空实现（`Server/EssentialCmd/CmdStop.cs`），停止可能需要靠信号或存档前手动触发保存。→ 任务 `V0-2`

---

## 2. 背景：对服务端的事实认定

### 2.1 发行物与运行方式

官方服务端包 `服务端X26.07.01.01.zip` 解包结构（`net10.0/`）：

```
Content.scpak                 # 游戏资源包（19MB）
Survivalcraft.dll             # 主程序集（.NET 10）
Survivalcraft.exe             # Windows apphost（Linux 下无用，走 dll）
Survivalcraft.runtimeconfig.json
start.sh                      # 官方启动脚本
Engine.dll / EntitySystem.dll / LiteNetLib.dll / MessagePack.dll / OpenTK.dll ...
```

`start.sh` 逻辑：优先执行 `./Survivalcraft`（self-contained 原生启动器），否则 `dotnet ./Survivalcraft.dll`，最后报错退出。**当前包没有 `Survivalcraft` 原生启动器，因此走 `dotnet` 分支** —— 面板必须确保运行时存在。

关键点：`tfm = net10.0`，`framework: Microsoft.NETCore.App 10.0.0`，**不含 `Microsoft.WindowsDesktop.App`**。这印证了"纯服务端"说法，也是它能跑 Linux 的根本原因。

### 2.2 首次运行生成的文件（面板的管理对象）

实测首次运行后工作目录自动产生：

| 路径 | 作用 | 面板是否需要管理 |
|---|---|---|
| `ServerSetting.json` | **开服主配置**：Autorun、世界名/种子/密码/人数/游戏模式/PVP 等 | ✅ 核心 |
| `Settings.xml` | 引擎设置：**`ServerPort`（默认 28887）**、渲染、日志开关、`WillEnterServer` 等 | ✅ 端口必管 |
| `Configs/BanConfig.json` | 封禁：`BanUserList` / `BanUserIpList` / `BanIpList` | ✅ |
| `Configs/LimitConfig.json` | 方块/爆炸限制：`WaterLength 7`、`MagmaLength 4`、`LimitBlockBreak`、`LimitExplode` | ✅ |
| `Configs/LevelConfig.json` | 等级/权限配置（默认 `{}`） | ✅ |
| `ModSettings.xml` | Mod 设置 | ⚪ 可选 |
| `Plugins/`、`NetMods/` | 插件与网络 Mod 投放目录 | ✅ 文件管理 |
| `Worlds/<WorldName>/` | **存档**：`Project.json` + `Project.json.bak` + `Regions/` | ✅ 核心 |
| `Bugs/`、`CharacterSkins/`、`FurniturePacks/`、`TexturePacks/` | 资源目录 | ⚪ 可选 |

`ServerSetting.json` 实测内容（默认值）：

```json
{
  "CheckLogin": false, "ScKeyServerId": "", "ScKeyServerName": "",
  "Autorun": false, "AutoGenerateWorld": false,
  "WorldPath": "app:/Worlds/World", "WorldName": "ScWorld", "WorldSeed": "",
  "WorldPassword": "", "WorldKeywordBlocking": "", "WorldMaxPlayers": 20,
  "WorldDaySpeed": 1, "WorldRecoverySpeed": 1, "WorldDisableBlocks": "",
  "RandomSpawnPosition": false, "GameMode": 1,
  "SeasonChanging": true, "PVPEnabled": true
}
```

> 注意路径语义：`WorldPath` 用 `app:/` 虚拟前缀（映射到工作目录），面板改写时需保持该前缀风格。

### 2.3 存档格式

`Worlds/<dir>/Project.json` 是 **带 UTF-8 BOM 的 JSON**，值以类型标注数组形式存储（`{"Field":["类型","值"]}`），例如：

```json
{"Version":["string","2.4"],"Guid":["System.Guid","9e9a67f8-..."],"Name":["string","GameProject"],
 "Subsystems":{"Players":{"BlackPlayerGuidList":{},"NoMsgPlayerGuidList":{},"GlobalSpawnPosition":["Vector3",...]}}}
```

含义：
- **不要用结构化 struct 直接反序列化整个存档**（类型标注是 MessagePack/自定义风格的混合）。面板只应做**定点读写**（如改 `MaxOnlinePlayerCount`、读 `Name`/`Guid`）与**整体备份/复制/删除**。
- 存在 `.bak` 伴生文件 → 说明服务端自己有备份机制，面板备份策略可与之协同（见 §6.5）。
- `Regions/` 存区块地形，体积增长的主要来源 → 容量统计与配额要覆盖它。

### 2.4 可解析的日志证据（面板状态机的依据）

实测启动日志中的稳定锚点（用于状态判定）：

```
使用游客模式启动，清理SCKey服务端绑定信息
[自动检测] 输出被重定向，使用基础终端模式
已自动生成存档目录 app:/Worlds/World
[StartServer]开启服务器成功，端口 28887
Loaded world, GameMode=Harmless, StartingPosition=Easy, WorldName=ShowNameX, VisibilityRange=128, Resolution=High
Entered screen "Game"
```

格式为 `HH:mm:ss.fff LEVEL: message`（本地时间）。插件加载形如 `开始加载插件 XXX(...) 版本: 1:0:0`。

> **注意**：上表中的 `输出被重定向，使用基础终端模式` 是**管道模式下**的实测结果。PTY 下预期改为 `EnhancedTerminalLogSink initialized...`，但**该路径尚未运行时验证**（见 §A.8 与任务 V0-1）。面板日志规则须同时容忍两种首行。

### 2.7 `WorldPath` 与 `WorldName` 的语义（重要，实测证明）

这是面板**最容易搞错**的一处，已用对照实验确认：

```jsonc
// ServerSetting.json（实验配置）
"WorldPath": "app:/Worlds/MyWorldA",   // ← 决定磁盘目录
"WorldName": "ShowNameX"               // ← 仅显示名
```

实测输出：

```
INFO: 已自动生成存档目录 app:/Worlds/MyWorldA     ← 用 WorldPath
INFO: Loaded world, ..., WorldName=ShowNameX      ← 用 WorldName
$ ls Worlds/
MyWorldA                                          ← 目录名取自 WorldPath
```

**结论与面板设计要求**：

| 项 | 规则 |
|---|---|
| 磁盘目录 | 由 `WorldPath` 决定，格式 `app:/Worlds/<目录名>`（`app:` = 实例工作目录，见 `ModsManager.cs` 的 `ExternelPath`） |
| 显示名 | `WorldName` 仅用于客户端列表/日志展示，**与目录无关** |
| 默认值 | `WorldPath=app:/Worlds/World`、`WorldName=ScWorld` → 目录默认就叫 `World` |
| 多存档切换 | **必须改 `WorldPath`**（只改 `WorldName` 不会切存档！） |
| 面板建实例 | 建议 `WorldPath=app:/Worlds/<slug>`，`WorldName` 由用户自由填（可中文） |
| 存档列表扫描 | 扫描 `<实例>/Worlds/*/` 读取各自 `Project.json`，**不要**用 `WorldName` 反推目录 |

### 2.5 指令与权限（源码事实）

服务端内置命令（`Server/EssentialCmd/`，均受 `NOGUI` 条件编译）：

| 命令 | AuthLevel | 说明 |
|---|---|---|
| `help` | 0 | 分页帮助（`/help <页码>`，每页 5 条） |
| `player` | 0 | **`/player list <页码>`** 查看玩家（每页 8 条） |
| `tp` | 2 | 传送 |
| `auth` / `ban` / `clear` / `item` / `kick` / `kill` / `time` | 100 | 管理指令 |
| `test` | 999 | 测试 |
| `stop` | 99999 | 关闭服务器（**当前 ProcessCmd 为空实现**） |

**两个影响面板实现的细节（源码确认）**：

1. **`player` 必须带页参数**：`CmdPlayer.ProcessCmd()` 中 `if (m_messageDatas.Length < 3)` 就只返回帮助文本 —— 即裸写 `/player` 或 `/player list` **拿不到玩家列表**，必须发 **`/player list 0`**。面板采集在线玩家时务必按此格式发送并按结果分页拉取。
2. **`help` 分页显示**：每页仅 5 条，面板若要生成命令提示需循环拉取全部页（或直接内置命令表，更省事）。

关键实现细节（源码阅读，非运行时实测）：`CmdManager.HandleMessage(..., bool isTerminal)` 中，当 `isTerminal == true` 时**跳过权限校验**且回复走 `Console.WriteLine($"[{title}]: {message}")`。这正是面板/终端发指令的通道，其配套类为 `CommandFifoMonitor` / `CommandFifoPath`（字面量 `command-fifo`）。

### 2.6 网络协议

基于 **LiteNetLib（UDP）**。端口来自 `Settings.xml` 的 `ServerPort`（默认 **28887**），另有 `BroadcastPort` 用于局域网广播发现。面板做端口占用检测/分配时应按 **UDP** 而非 TCP 判断。

---

## 3. 需求范围

### 3.1 核心功能（对齐 MCSM）

| 模块 | 能力 |
|---|---|
| 实例管理 | 创建/删除/复制实例；每个实例独立工作目录与端口；启停/重启/强杀 |
| 生命周期 | 启动、优雅停止、强制结束、崩溃自动重启（可配）、开机自启 |
| 状态监控 | 运行状态机、CPU/内存/运行时长、在线玩家数、端口监听状态 |
| 控制台 | 实时日志流（WebSocket）、历史日志检索、发送指令、命令补全 |
| 配置管理 | 图形化编辑 `ServerSetting.json` / `Settings.xml` / `Configs/*.json`，带校验与回滚 |
| 世界管理 | 列存档、导入/导出、备份/还原、删除、定点改关键字段、容量统计 |
| 文件管理 | 实例目录浏览/上传/下载/重命名/删除、解压上传 `Plugins`/`NetMods`/`Mods` |
| 用户权限 | 多用户、角色（管理员/操作员/访客）、实例级授权、审计日志 |
| 定时任务 | 定时备份、定时重启、定时广播（可选） |
| 通知 | Webhook（QQ/钉钉/企微/Discord）、服务端上下线、崩溃告警 |

### 3.2 明确的非目标（本期不做）

- 不实现游戏内 Mod 编辑/编译（仅投放与启停）。
- 不做社区服列表（SCKey/ScKeyServer）对接的写操作，仅展示。
- 不做玩家间经济/商店系统。
- 不做 `Survivalcraft.exe` GUI 版（客户端/整合端）托管 —— 只托管纯服务端。

### 3.3 目标用户与典型场景

- 服主：一人管 1~5 个实例，需图形化配置与一键备份。
- 小社区：多人协作，需权限分离与审计。
- 场景：新开一个 20 人 PVP 生存服 → 面板建实例 → 选定种子与游戏模式 → 启动 → 分享 IP:端口 → 每日 4:00 自动备份。

---

## 4. 总体架构

### 4.1 技术选型（Go）

| 层 | 选型 | 理由 |
|---|---|---|
| 语言 | Go 1.22+ | 单文件部署、跨平台交叉编译、并发模型契合进程/日志管理 |
| Web 框架 | `gin`（或 `echo`） | 生态成熟；仅用其中间件与路由 |
| 实时通道 | `gorilla/websocket` | 日志流与状态推送 |
| 数据库 | SQLite（`modernc.org/sqlite`，纯 Go 无 CGO） | 零依赖部署；单机面板足够 |
| ORM | `gorm` 或 `sqlc` | 视团队偏好；建议 `sqlc` 换取显式 SQL 与可测性 |
| 配置 | `viper` 或 `koanf` | 支持面板自身配置文件 + 环境变量覆盖 |
| 日志 | `zerolog` / `zap` | 面板自身日志（与服务端日志分开存储） |
| 认证 | JWT（`golang-jwt`）+ bcrypt | 无状态；会话存 SQLite 以支持踢下线 |
| 前端 | Vue 3.5 + Vite + **`fuxsto-design`** + Tailwind CSS v4，**`embed.FS` 内嵌** | 单二进制交付；前端构建产物嵌入 Go。UI 库见 §4.4 |
| 终端 | `xterm.js` + `@xterm/addon-fit` + `@xterm/addon-web-links` | 渲染 ANSI 彩色输出，与 PTY 尺寸联动（§5.2） |
| 进程 | `os/exec`（非 shell） | 避免注入；直接 exec `dotnet Survivalcraft.dll` |
| 打包 | 前端 `dist` → `go:embed` | 最终一个二进制 + 数据目录 |

> 若团队更熟 React，前端可替换；架构其余部分不受影响。

### 4.2 组件图

```
┌──────────────────────────────────────────────────────────────┐
│         浏览器 (Vue3 SPA + fuxsto-design + Tailwind v4)        │
│  实例列表 │ 控制台(xterm.js) │ 配置编辑 │ 文件管理 │ 账户        │
└───────────────┬──────────────────────────┬───────────────────┘
                │ REST (JSON)              │ WebSocket（含色日志流/指令/尺寸）
┌───────────────▼──────────────────────────▼───────────────────┐
│                      Go 面板 (单二进制)                        │
│  ┌────────────┐ ┌────────────┐ ┌────────────┐ ┌───────────┐  │
│  │ Auth/RBAC  │ │ Instance   │ │ Config     │ │ Scheduler │  │
│  │ 中间件      │ │ Service    │ │ Service    │ │ (cron)    │  │
│  └────────────┘ └─────┬──────┘ └─────┬──────┘ └─────┬─────┘  │
│  ┌────────────────────▼──────────────▼──────────────▼─────┐  │
│  │              Supervisor（进程与日志中枢）                │  │
│  │  • Runner      启动/停止/重启（进程组 + 信号）            │  │
│  │  • PTY         伪终端分配/尺寸同步/降级判定  【D1】        │  │
│  │  • LogPipe     字节流分帧 → 保色原文 + 无色纯文本 双写     │  │
│  │  • ANSI        CSI/OSC 解析与剥离（容错不完整序列）【D1】  │  │
│  │  • CmdChannel  指令下发（PTY 写 stdin 主 / FIFO 备）§5.3   │  │
│  │  • StateMachine 由日志锚点驱动状态迁移                    │  │
│  │  • Metrics     /proc 采集 CPU/内存                        │  │
│  └────────────────────────┬───────────────────────────────┘  │
│  ┌────────────────────────▼───────────────────────────────┐  │
│  │  Storage: SQLite(元数据) + 文件系统(实例目录/日志/备份)   │  │
│  └────────────────────────────────────────────────────────┘  │
└───────────────────────────────┬──────────────────────────────┘
                                │ os/exec + PTY (dotnet Survivalcraft.dll)
                                │ TERM=xterm-256color → 彩色输出
┌───────────────────────────────▼──────────────────────────────┐
│  实例目录 /srv/scnetm/instances/<id>/                         │
│   Content.scpak, Survivalcraft.dll, ServerSetting.json,       │
│   Settings.xml, Configs/, Plugins/, Worlds/, logs/            │
└──────────────────────────────────────────────────────────────┘
```

### 4.3 进程模型与隔离

- **每实例一进程组**：`cmd.SysProcAttr.Setpgid = true`（Linux），停止时对进程组发信号，避免 `dotnet` 子进程泄漏。
- **工作目录 = 实例目录**：实测服务端以 CWD 为根生成全部配置，因此 `cmd.Dir` 必须指向实例目录，这是隔离的关键。
- **环境变量**：注入 `DOTNET_ROOT`、`PATH`、`DOTNET_CLI_TELEMETRY_OPTOUT=1`、`LANG=C.UTF-8`（日志中文编码）、`TZ`。
- **文件描述符**：**PTY（`pty.StartWithSize`）作为 stdin/stdout/stderr 的统一终端**（§5.2），
  仅在 PTY 不可用时降级为 `os.Pipe` 直连解析器；两种方式都**不经过 shell**。
- **端口分配**：从配置的端口池取，用 UDP 探测占用；写入 `Settings.xml` 的 `ServerPort`。
- **资源限制**（可选）：cgroup v2 或 `setrlimit` 限制内存，防止单实例拖垮宿主。

### 4.4 UI 组件库：fuxsto-design

已核实（`registry.npmmirror.com/fuxsto-design`，最新 `1.0.5`，MIT，2026-10-02 发布）：

- **定位**：Monochrome zinc 单色设计语言的 **Vue 3 组件库**，基于 **Tailwind CSS v4**，shadcn 风格，**82 个导出子路径**。
- **Peer 依赖**：`vue ^3.5.0`、`tailwindcss ^4.0.0`、`lucide-vue-next ^0.577.0`。运行时依赖仅 `clsx`、`tailwind-merge`、`@floating-ui/vue`（**轻量，无重量级依赖**）。
- **样式自包含**：只需两条 CSS（`@import "tailwindcss"; @import "fuxsto-design/styles";`），**消费方无需配置 `@source`/safelist** —— 对"Go 内嵌前端产物"很友好。
- **暗色模式**：系统偏好 + `.dark` 类双触发，适合做服务器控制台的深色主题。
- **按需引入**：每个组件有独立子路径入口（`fuxsto-design/button`），利于 tree-shaking。
- **类型**：全量 TypeScript。

**本项目将用到的组件映射**：

| 面板功能 | 选用组件 |
|---|---|
| 布局/导航 | `menu`、`tabs`、`breadcrumb`、`segmented`、`drawer` |
| 实例列表/监控 | `card`、`table`、`pagination`、`statistic`、`progress`、`timeline`、`badge`、`tag` |
| 配置表单 | `form`、`form-item`、`input`、`input-number`、`select`、`switch`、`slider`、`checkbox`、`radio`、`textarea`、`auto-complete` |
| 存档/文件管理 | `tree`、`table`、`upload`、`transfer`、`virtual-list`（大目录） |
| 交互反馈 | `dialog`、`popconfirm`、`message`、`notification`、`alert`、`result`、`tooltip`、`loading`、`empty`、`skeleton` |
| 日志实时性 | `streaming-text`（流式文本）、`scroll-area`、`virtual-list` |

**关键缺口与补位（务必注意）**：
1. **没有终端组件** → 控制台必须用 **`xterm.js`** 渲染，`fuxsto-design` 只负责其外层容器/工具栏。不要把终端塞进 `textarea` 或 `<pre>`（会丢 ANSI 颜色，违背 D1）。
2. **没有图表组件**（无折线/面积图） → 监控时序图需引入 **`uplot`** 或 **`echarts`**（建议 `uplot`：体积小、适合高频指标；`contribution-chart` 是热力图，不适用）。
3. 版本策略：库较新（1.0.x，发布仅数月）→ **锁定小版本**（`~1.0.5`），并在 `web/` 内封装一层薄适配（如 `components/ui/*` 再导出），以便未来替换或打补丁。

**引入方式（`web/` 侧）**：
```ts
// vite.config.ts —— 前端需装 tailwindcss v4 的 vite 插件
import tailwindcss from '@tailwindcss/vite'
export default defineConfig({ plugins: [vue(), tailwindcss()] })
```
```css
/* src/style.css */
@import "tailwindcss";
@import "fuxsto-design/styles";   /* 库自带样式，无需 @source 扫描 */
```

---

## 5. 关键设计

### 5.1 实例状态机

```
                ┌──────────┐
                │ Created  │
                └────┬─────┘
                     │ start
                ┌────▼─────┐  超时(默认90s)  ┌─────────┐
                │ Starting ├────────────────►│ Failed  │
                └────┬─────┘                 └────┬────┘
     日志锚点命中    │                            │ retry
  "开启服务器成功"   │                            │
  +"Entered screen  │                            │
   \"Game\""        │                            │
                ┌────▼─────┐   stop/信号     ┌───▼──────┐
                │ Running  ├────────────────►│ Stopping │
                └────┬─────┘                 └────┬─────┘
                     │ 进程意外退出                │ 退出码 0/超时
                ┌────▼─────┐                 ┌───▼──────┐
                │ Crashed  │                 │ Stopped  │
                └──────────┘                 └──────────┘
```

状态迁移**由日志锚点 + 进程存活**共同判定，而非仅靠进程存活（因为服务端"已监听端口"与"世界已加载可加入"是两个不同就绪阶段）。

就绪判据（必须同时满足才可对外声称"可加入"）：
1. 进程存活；
2. 命中 `[StartServer]开启服务器成功，端口 (\d+)`；
3. 命中 `Entered screen "Game"`（世界加载完成）。

### 5.2 日志与终端输出（PTY 仿真终端，保色）

**核心决策（D1）**：实例以 **PTY（伪终端）** 方式启动，而非管道重定向。理由是服务端有两条互斥的终端路径：

| 模式 | 触发条件 | 输出特征 |
|---|---|---|
| **基础终端模式** | stdout 被重定向/管道 | 已实测：`[自动检测] 输出被重定向，使用基础终端模式`，无 ANSI 色 |
| **增强终端模式** | 真实 TTY，或显式 `--enhanced` | `EnhancedTerminalLogSink initialized With ANSI Support: ... 增强终端安全退出`，**带 ANSI 彩色**、支持终端尺寸与安全退出 |

因此**必须用 PTY**（`github.com/creack/pty`）才能拿到彩色输出与交互能力：

```go
cmd := exec.Command(dotnetPath, "Survivalcraft.dll")
cmd.Dir = instDir
cmd.Env = append(os.Environ(),
    "DOTNET_ROOT="+dotnetRoot,
    "LANG=C.UTF-8", "LC_ALL=C.UTF-8",
    "TERM=xterm-256color",
    "DOTNET_CLI_TELEMETRY_OPTOUT=1",
)
ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 40, Cols: 160})
```

**彩色输出如何端到端保留**（这是 D1 的落地要点，四段链路都不能丢色）：

1. **采集**：从 `ptmx` 原始字节流读取，**不要** `bufio.Scanner` 直接切字符串后再去 ANSI —— 先按字节流做分帧，再按 `\r?\n` 切行，**保留 ESC 序列原文**。
2. **存储**：落盘保存**含 ANSI 的原始行**（用于回放保色）；同时在结构化事件里存**剥离 ANSI 的纯文本**（用于检索/规则匹配）。两者并存，避免"为了搜索而丢色"。
3. **传输**：WebSocket 推**原始字节/含色文本**；给前端一个开关（`raw` / `plain`），用户可切换。
4. **渲染**：前端用 `xterm.js` 渲染（原生支持 ANSI/256 色/光标控制），**不要**用 `<pre>` + 正则上色。

**PTY 的三个额外好处**（顺带解决多个问题）：
- **指令下发**：直接 `ptmx.Write([]byte(cmd + "\n"))`，就是真实键盘输入（见 §5.3）。
- **终端尺寸**：服务端会查询窗口大小，`pty.Setsize` 可在前端 xterm.js `fit()` 后同步，避免换行错乱。
- **安全退出**：服务端实现了 `增强终端安全退出`，PTY 下能走完整退出路径，比裸信号更干净。

**注意与坑**：
- PTY 输出是 `\r\n` 行尾 + 可能含 `\r` 覆盖写法（进度条/刷新行）→ 解析器需处理 `\r` 回退，**行重建不能用简单 split**。
- PTY 会把**服务端回显**（echo）也送回来 → 需区分"我们发的指令回显"与"服务端真实输出"，避免日志重复。建议启动时 `stty -echo`（通过 PTY 写 `stty` 或依赖服务端自身不 echo）。
- `TERM` 必须是 `xterm-256color`（或 `xterm`），否则服务端可能降级配色；面板应允许该值可配。
- **优雅降级**：若 `pty.Start` 失败（如无 `/dev/ptmx` 权限、容器缺 `CAP_SYS_ADMIN` 之外的 tty 设备），自动退回管道模式并**在 UI 明确告警"当前为无色模式"**，而不是静默降级。

> 兜底：即使 PTY 不可用，服务端仍可显式 `--enhanced` 参数尝试激活增强模式（已从日志提示中确认该参数存在，**待实测其在管道下的行为**，见 V0-1）。

### 5.2.1 日志解析规则

- 按行读取，**保留原始行（含 ANSI）**用于展示，同时产出**剥离 ANSI 的纯文本**做规则匹配与全文检索。
- 规则表（可配置、热更新）：

| 规则 | 匹配 | 产出事件 |
|---|---|---|
| 终端模式 | `\[自动检测\] 输出被重定向` / `EnhancedTerminalLogSink initialized` | `term.mode{basic\|enhanced}` |
| 服务端就绪 | `\[StartServer\]开启服务器成功，端口 (\d+)` | `server.listening{port}` |
| 世界加载 | `Loaded world,.*WorldName=([^,]+)` | `world.loaded{name}` |
| 进入游戏态 | `Entered screen "Game"` | `server.ready` |
| 玩家加入 | 玩家进出相关行（**待确认实际文案**） | `player.join{name}` |
| 插件加载 | `开始加载插件 (.+?)\((.+?)\) 版本: (\d+):(\d+):(\d+)` | `plugin.loaded` |
| 错误 | `\bERROR:` | `log.error`（可触发告警） |
| 崩溃线索 | `Unhandled exception` / `at Game\.` | `server.crash` |

- **环形缓冲**：内存保留最近 N 行（默认 2000，**含色原文**）供新连接回放；全量落盘 `logs/<date>.log`（原始含色字节，便于 `cat` 回放）与 `logs/<date>.plain.log`（无色，便于 grep）。
- 中文编码：实测输出为 UTF-8，强制 `LANG=C.UTF-8`；解析器需容忍非法字节（`strings.ToValidUTF8`）与 ANSI 序列被截断的情况。
- ANSI 处理：正则 `\x1b\[[0-9;?]*[ -/]*[@-~]`（CSI）与 `\x1b\][^\x07\x1b]*(\x07|\x1b\\)`（OSC）用于剥离；**仅在生成 plain 文本时使用，绝不改动原始流**。

### 5.3 指令通道（决策：PTY 写 stdin 为主）

**主方案 — 写 PTY（D1 直接收益）**
最简单也最可靠：指令就是"键盘输入"。**源码阅读**（`Server/CmdManager.cs`、`Server/AbstractProcessCmd.cs`）显示：`CmdManager.HandleMessage(..., isTerminal: true)` 在终端来源时**跳过权限校验**，且 `SendMessage` 走 `Console.WriteLine($"[{title}]: {message}")` —— 也就是**直接出现在同一路输出里**，面板无需额外通道即可拿到命令结果。

> ⚠️ 上述为**源码结论，尚未运行时验证**（终端来源标记如何被置为 `true`，取决于闭源 `Program` 的输入循环）。这是 V0-1 必须实测确认的第一件事 —— 若终端来源未被正确识别，面板指令可能因权限校验而被拒。

```go
func (r *Runner) SendCommand(line string) error {
    if r.ptmx == nil { return ErrNoTTY }     // PTY 不可用则走配置热改
    if _, err := r.ptmx.Write([]byte(line + "\n")); err != nil { return err }
    return nil
}
```

命令结果不单独解析，统一走日志流 + 规则匹配（如 `^\[(help|player|ban)\]:` 提取回复），前端在控制台里直接可见。

**备选方案 — `command-fifo` 命名管道**
程序集存在 `CommandFifoMonitor`、`CommandFifoPath` 与字面量 `command-fifo`（且与 `ServerSetting.json` 常量相邻，推测由配置驱动）。若 PTY 下服务端额外创建了该 FIFO，可作为**独立于显示的指令通道**（好处：即使前端只订阅日志，也能发指令）。实现为 `CmdChannel` 接口的第二个实现，运行期自动探测：

```go
type CmdChannel interface {
    Send(string) error
    Kind() string            // "pty" | "fifo" | "none"
    Close() error
}
```

**验证清单（V0-1，阶段 0 首要任务）**：
1. 以 PTY 启动，确认日志出现 `EnhancedTerminalLogSink initialized`（**彩色模式已激活**）而非"输出被重定向"。
2. 通过 PTY 写 `help\n`，确认收到带色的 `[help]: ...` 回复 → **主方案成立**。
3. 观察实例目录是否出现 `command-fifo`；若出现，写入 `help\n` 看是否等价生效 → 决定是否启用 FIFO 双通道。
4. 记录 `TERM` 取值对配色的影响、是否需要 `stty -echo` 去回显。
5. 在 **Docker 容器**内重复上述验证（容器 tty 分配是最常见的 PTY 失效点）→ 直接影响 §9.2 的部署参数。

3. 若 FIFO 未出现，改用 PTY 写 stdin，重复步骤 2。
4. 记录结论：通道类型、是否需权限、是否支持中文、超时与错误行为。

**兜底方案 C（若 A/B 均不可用）**：
- 指令能力降级为：**配置热改 + 重启**（`ServerSetting.json` 写入后重启实例）；
- 玩家管理（踢人/封禁）改为**直接写 `Configs/BanConfig.json`** 后再重启或等其热载；
- 并规划"自研插件"（`ServerPlugin`，源码 `Server/ServerPlugin.cs`）在游戏内暴露 RCON-like 接口（HTTP/Unix Socket），作为长期正解。

### 5.4 停止与保存策略

- `CmdStop.ProcessCmd()` 在当前源码中为空 → `/stop` 可能**不会真正停服**。因此面板的停止流程不应依赖它：
  1. 尝试优雅路径：指令通道发 `/stop`（若 V0-1 结论支持），等待退出码 0；
  2. 超时（默认 30s）→ 对进程组发 `SIGTERM`；
  3. 再超时（默认 15s）→ `SIGKILL`，并在面板告警"未优雅退出，存档可能未保存"。
- **存档安全**：强杀前尽量确认最后一次存档落盘。观察 `Worlds/<dir>/Project.json` 的 `mtime` 变化作为"已保存"信号；启动前**强制备份**存档（见 §6.5），把强杀风险降到最低。
- **平台范围（D2）**：**仅实现 Linux**。信号走 `syscall.Kill(-pgid, SIGTERM/SIGKILL)`，PTY 走 `/dev/ptmx`，不做 Windows 分支（不写 `taskkill`/`GenerateConsoleCtrlEvent`）。代码中所有进程相关逻辑用 `_linux.go` 构建约束，避免跨平台假设污染主逻辑。
- **PTY 下的停止顺序**（保色模式专属）：优先尝试**通过 PTY 发送 Ctrl+C**（`\x03`），命中服务端的 `增强终端安全退出` 路径 —— 这比 `SIGTERM` 更可能触发其存档逻辑；超时再升级为进程组 `SIGTERM` → `SIGKILL`。

### 5.5 数据模型（SQLite）

```sql
-- 面板用户与权限
-- 【D3 扩展位】单用户模式下仅一行（id=1, role='admin'），首次启动自动创建并要求设置密码。
-- 表结构、中间件、owner_id 现在就留好，将来开启多用户无需改表、无需数据迁移。
CREATE TABLE users (
  id INTEGER PRIMARY KEY, username TEXT UNIQUE NOT NULL,
  password_hash TEXT NOT NULL, role TEXT NOT NULL,      -- admin/operator/viewer
  created_at DATETIME, last_login_at DATETIME, disabled INTEGER DEFAULT 0
);
CREATE TABLE user_instance_grant (                      -- 实例级授权（单用户下恒为空，逻辑已就位）
  user_id INTEGER, instance_id INTEGER, perm TEXT,       -- read/control/config
  PRIMARY KEY (user_id, instance_id, perm)
);

-- 实例
CREATE TABLE instances (
  id INTEGER PRIMARY KEY, name TEXT UNIQUE NOT NULL,
  dir TEXT NOT NULL,                                     -- 工作目录绝对路径
  port INTEGER NOT NULL,                                 -- UDP
  dotnet_path TEXT, server_jar TEXT,                     -- Survivalcraft.dll
  owner_id INTEGER DEFAULT 1,                            -- 【D3 扩展位】实例归属
  auto_start INTEGER DEFAULT 0, auto_restart INTEGER DEFAULT 0,
  max_restart INTEGER DEFAULT 5,
  stop_timeout_sec INTEGER DEFAULT 30,
  term TEXT DEFAULT 'xterm-256color',                    -- 【D1】PTY TERM
  color_mode TEXT DEFAULT 'enhanced',                    -- enhanced|basic（降级时记录）
  created_at DATETIME, memo TEXT
);

-- 运行态快照（重启后可恢复展示）
CREATE TABLE instance_state (
  instance_id INTEGER PRIMARY KEY, state TEXT, pid INTEGER,
  started_at DATETIME, stopped_at DATETIME, exit_code INTEGER,
  last_error TEXT, online_players INTEGER DEFAULT 0
);

-- 日志索引（正文在文件，库中只存索引与检索关键词）
CREATE TABLE log_events (
  id INTEGER PRIMARY KEY, instance_id INTEGER, ts DATETIME,
  level TEXT, event TEXT, payload TEXT                     -- JSON
);
CREATE INDEX idx_log_events ON log_events(instance_id, ts);

-- 备份
CREATE TABLE backups (
  id INTEGER PRIMARY KEY, instance_id INTEGER, world TEXT,
  path TEXT, size_bytes INTEGER, sha256 TEXT,
  kind TEXT,                                               -- manual/scheduled/pre-start
  created_at DATETIME, note TEXT
);

-- 任务与审计
CREATE TABLE jobs (
  id INTEGER PRIMARY KEY, instance_id INTEGER, type TEXT,  -- backup/restart/command
  cron TEXT, payload TEXT, enabled INTEGER, last_run DATETIME, last_result TEXT
);
CREATE TABLE audit_logs (
  id INTEGER PRIMARY KEY, user_id INTEGER, action TEXT,
  target TEXT, detail TEXT, ip TEXT, ts DATETIME
);
```

### 5.6 API 设计（REST + WS）

统一前缀 `/api/v1`，认证 `Authorization: Bearer <JWT>`。

```
POST   /auth/login                     登录
POST   /auth/logout                    登出
GET    /auth/me                        当前用户

GET    /instances                      列表（含实时状态）
POST   /instances                      创建（模板化：目录+端口+初配）
GET    /instances/:id                  详情
DELETE /instances/:id                  删除（可选删目录）
POST   /instances/:id/start
POST   /instances/:id/stop              body:{force:bool}
POST   /instances/:id/restart
GET    /instances/:id/stats            CPU/内存/运行时长/在线数
GET    /instances/:id/players          在线玩家（依赖 V0-1）

GET    /instances/:id/config           读配置（ServerSetting/Settings/Configs）
PUT    /instances/:id/config           写配置（校验+备份+可选重启）
POST   /instances/:id/config/validate  仅校验

GET    /instances/:id/worlds           存档列表（大小/时间/人数上限）
POST   /instances/:id/worlds/import    导入 zip
GET    /instances/:id/worlds/:w/export 导出下载
POST   /instances/:id/worlds/:w/backup
POST   /instances/:id/worlds/:w/restore
POST   /instances/:id/worlds/:w/activate  切换为启用存档
DELETE /instances/:id/worlds/:w

GET    /instances/:id/files?path=      目录列表
POST   /instances/:id/files/upload
GET    /instances/:id/files/download?path=
POST   /instances/:id/files/mkdir|rename|delete|unzip

GET    /instances/:id/logs?tail=&grep= 历史日志
GET    /instances/:id/logs/download
GET    /backups?instance_id=
POST   /backups/:id/restore
DELETE /backups/:id

GET    /jobs  POST /jobs  PUT /jobs/:id  DELETE /jobs/:id
GET    /users POST /users PUT /users/:id DELETE /users/:id
GET    /audit?instance_id=&from=&to=
GET    /system/info                    宿主信息/运行时检测

WS     /ws/instances/:id/console       双向：日志推送 + 指令下发
WS     /ws/events                      全局事件（状态变更/告警）
```

状态码约定：`409` 状态冲突（如对 Running 实例再 start）、`422` 配置校验失败、`504` 操作超时。

### 5.7 安全设计

- **路径穿越防护**（文件管理最重要）：所有文件 API 的 `path` 必须经 `filepath.Clean` + `filepath.EvalSymlinks` 后校验 `strings.HasPrefix(resolved, instanceDir)`；拒绝符号链接逃逸。
- **上传限制**：大小上限、类型白名单（`.zip/.dll/.scpak/.json/.xml/.txt`）、`unzip` 防 zip-slip（逐条目校验目标路径）。
- **命令注入**：`exec.Command` 传参数数组，**永不拼 shell**；指令下发限制字符集与长度，禁止换行注入多条。
- **认证与授权**：JWT 短期 + 刷新；RBAC 三档；**实例级授权中间件**；敏感操作（删除/覆盖存档、停止）二次确认。
- **速率限制**：登录、指令下发、文件上传分别限流。
- **审计**：所有写操作记 `audit_logs`（含 IP 与变更前后差异摘要）。
- **面板自身硬化**：默认仅监听 `127.0.0.1:8080`；建议反代 + TLS；避免默认密码（首次启动强制设置管理员密码）。

---

## 6. 核心功能实现要点

### 6.1 实例创建（模板化）

1. 在 `instances_root` 下建 `<slug>` 目录；
2. 从**服务端模板包**解压 `Content.scpak`、`*.dll`、`Survivalcraft.dll`、`Survivalcraft.runtimeconfig.json`、`start.sh`；
   - 建议：模板只存一份，实例用**硬链接**（`os.Link`）共享只读大文件（`Content.scpak` 19MB），节省磁盘；
3. 写入初始 `ServerSetting.json`（面板生成，含用户指定世界名/种子/人数/游戏模式）；
4. 写入/合并 `Settings.xml` 的 `ServerPort`；
5. 首次启动由服务端自动补齐 `Configs/`、`Worlds/` 等；
6. 记录 `instances` 表并置 `Created` 状态。

> **首启顺序陷阱**：服务端首次运行才生成配置文件。面板若要"创建即展示配置"，应先生成一份基准 `ServerSetting.json` 与 `Settings.xml`（可从模板目录复制），避免依赖首启。

### 6.2 配置读写

- `ServerSetting.json`：Go struct 强类型映射（字段少且稳定），写入时 `encoding/json` + `MarshalIndent` 保留可读性；**写前备份** `.panel.bak`。
  - `GameMode int` 采用推断映射（见 §6.2.1），UI 用下拉展示中文名并用 tooltip 标注"推断值，待实测校验"。
  - `WorldPath` 保持 `app:/Worlds/<dir>` 前缀风格 —— **这是决定磁盘目录的字段**（§2.7），`WorldName` 只是显示名，两者要分别处理。
  - 写入需**保留未知/新增字段**：结构体用 `map[string]any` 兜底或 `json.RawMessage`，避免服务端升级新增字段后被面板抹掉。
- `Settings.xml`：`encoding/xml` 解析为 `[]Setting{Name,Value}`，**只改目标键**，保留其余原样与顺序（避免破坏引擎兼容）。
- `Configs/*.json`：`BanConfig`/`LimitConfig` 有实测样本可强类型；但**注意这三个文件由闭源插件产生**（`BanUserPlugin`/`LimitPlugin`/`ChatLimitPlugin` 不在开源源码树内），字段可能在版本升级时变化 → 建议**透传编辑为主**（JSON 编辑器 + 基础校验 + 写前备份），仅在字段稳定后再逐步强类型化。
- **变更生效策略**：启动前写 → 生效；运行时写 → 标注"需重启生效"，并提供一键重启。
- **校验器**：端口范围与占用、人数上限、**世界目录名合法性（`WorldPath` 末段禁止路径分隔符与 `..`）**、世界显示名（可宽松，允许中文）、种子格式、密码长度。

#### 6.2.1 `GameMode` 映射（D5）

推断结果（**按枚举声明顺序**）：

| 值 | 游戏内名称 | 难度定位 | 备注 |
|---|---|---|---|
| 0 | `Creative` | 创造 | 无生存机制、可飞行 |
| 1 | `Harmless` | 无害 | **已实测验证**：`GameMode=1` 加载日志显示 `GameMode=Harmless` |
| 2 | `Survival` | 生存 | 标准生存 |
| 3 | `Challenging` | 挑战 | 源码 `GameMode < GameMode.Challenging` 断言其为**数值比较**，故枚举按序号递增 |
| 4 | `Cruel` | 残酷 | 最高难度；源码多处限制（`ModifyWorldScreen` 禁止改难度、`PlayersScreen` 特殊处理） |
| 5 | `Adventure` | 冒险 | 源码 `GameMenuDialog` 中与 `AdventureRestart` 快照机制关联 |
| 6 | `Mirror` | — | **存疑**：名字来源不确定，可能是镜像/自定义模式，待实测 |

**推断依据**：
1. 实测 `GameMode=1` → `Harmless`（锚点）。
2. `SubsystemMatchBlockBehavior.cs:65` 使用 `GameMode < GameMode.Challenging` —— **枚举支持大小比较**，证明定义有序。
3. `Cruel` 在多处被当作最严格难度处理（不可改、特殊重生逻辑），符合末位高值。
4. 程序集字符串中确认存在 `Creative`/`Harmless`/`Survival`/`Challenging`/`Cruel` 五个名称。

**上线前必须校验**（低成本、高收益）：面板配置页把 `GameMode` 建成下拉，逐个取值启动实例并比对日志中的 `Loaded world, GameMode=XXX`，一次性锁定真实映射。**校验完成前，UI 需标注"推断值"**。

### 6.3 世界（存档）管理

- **列表**：扫描 `Worlds/*/`，读各目录的 `Project.json`（**去 BOM**）取 `WorldName`、`Guid`、`MaxOnlinePlayerCount`、`GameMode` 等；统计 `Regions/` 体积与 `mtime`。**目录名即 `WorldPath` 末段，不要用日志里的 `WorldName` 反推**（§2.7）。
- **导入/导出**：整目录打包 zip；导出时可选剔除 `Regions` 以缩小体积。导入时校验目录名不与现有冲突。
- **激活切换**：改 `ServerSetting.json` 的 **`WorldPath`**（`WorldName` 可一并同步以保持一致；**仅改 `WorldName` 不会切换存档**）。**仅在实例停止时允许**。
- **删除**：先备份再删；二次确认；保护正在使用的世界（比对当前 `WorldPath`）。
- **容量**：`du` 统计 `Regions/`，加配额告警。

#### 6.3.1 存档 `GameInfo` 可配置字段全表（面板能力清单）

实测 `Project.json` → `Subsystems.GameInfo` 含以下字段（值均为 `["类型", 值]` 标注格式）。**这意味着面板能配置的世界参数远比 `ServerSetting.json` 暴露的多** —— 两张表需保持一致（写入策略见下）。

| 字段 | 类型 | 实测值 | 面板可暴露 |
|---|---|---|---|
| `WorldName` | string | `ShowNameX` | ✅ 世界显示名 |
| `GameMode` | `Game.GameMode` | `Harmless` | ✅ **存档内是字符串**，见下方注意 |
| `EnvironmentBehaviorMode` | `Game.EnvironmentBehaviorMode` | `Living` | ✅ 环境行为 |
| `TimeOfDayMode` | `Game.TimeOfDayMode` | `Changing` | ✅ 昼夜模式 |
| `AreSeasonsChanging` | bool | `true` | ✅ 季节变化 |
| `YearDays` / `TimeOfYear` | float | `24` / `0.125` | ⚠️ 高级 |
| `RecoverFator` / `DaySpeed` | float | `1` / `1` | ✅ 对应 `WorldRecoverySpeed`/`WorldDaySpeed` |
| `AreWeatherEffectsEnabled` | bool | `true` | ✅ |
| `IsAdventureRespawnAllowed` | bool | `true` | ⚠️ 冒险模式相关 |
| `AreAdventureSurvivalMechanicsEnabled` | bool | `true` | ⚠️ |
| `AreSupernaturalCreaturesEnabled` | bool | `true` | ✅ 超自然生物 |
| `IsFriendlyFireEnabled` | bool | `true` | ✅ 对应 `PVPEnabled` |
| `Password` | string | `""` | ✅ 对应 `WorldPassword` |
| `RunServer` | bool | `true` | ⚠️ **勿随意改** |
| `KeywordBlocking` | string | `""` | ✅ 对应 `WorldKeywordBlocking` |
| `WorldSeedString` | string | `"999"` | ✅ **用户输入的原始种子** |
| `WorldSeed` | int | `5130` | ⚠️ **派生值**，只读勿写 |
| `TerrainGenerationMode` | `Game.TerrainGenerationMode` | `Continent` | ✅ 地形生成模式 |
| `IslandSize` | `Vector2` | `"400,400"` | ✅ 岛屿尺寸 |
| `TerrainLevel` | int | `64` | ✅ 地形高度 |
| `ShoreRoughness` | float | `0.5` | ✅ 海岸粗糙度 |
| `TerrainBlockIndex` / `TerrainOceanBlockIndex` | int | `8` / `18` | ⚠️ 方块索引 |
| `TemperatureOffset` / `HumidityOffset` | float | `0` / `0` | ✅ 气候偏移 |
| `SeaLevelOffset` | int | `0` | ✅ 海平面偏移 |
| `BiomeSize` | float | `1` | ✅ 生物群系尺寸 |
| `StartingPositionMode` | `Game.StartingPositionMode` | `Easy` | ✅ 出生点模式 |
| `BlockTextureName` | string | `""` | ⚠️ 材质包 |
| `Palette` | object | `{Colors:"...",Names:"..."}` | ⚠️ 调色板 |
| `MaxOnlinePlayerCount` | ushort | `20` | ✅ 人数上限 |
| `DisableBlocks` | string | `""` | ✅ 禁用方块（对应 `WorldDisableBlocks`） |
| `RandomSpawnPosition` | bool | `false` | ✅ |
| `WorldDirectoryName` | string | `app:/Worlds/MyWorldA` | ✅ 与 `WorldPath` 联动，**勿手改** |
| `OriginalSerializationVersion` | string | `"2.4"` | ⚠️ 只读 |

**三条关键注意（都必须写进实现）**：

1. **`GameMode` 在存档内是字符串，在 `ServerSetting.json` 里是数字** —— 两处格式不同，面板需双向映射（数字 ↔ 枚举名），不能混用。
2. **`WorldSeed`（int）是 `WorldSeedString` 派生的**（实测：输入 `"999"` → 生成 `5130`）。面板应**只写 `WorldSeedString`**，`WorldSeed` 保持只读，避免种子不一致。
3. **两份配置存在重叠字段**（`WorldName`/`GameMode`/`Password`/`DaySpeed`/`MaxOnlinePlayerCount`/`PVPEnabled`/`DisableBlocks`/`SeasonChanging`/`RandomSpawnPosition` 等同时出现在 `ServerSetting.json` 与存档 `GameInfo`）。面板的写入策略必须明确：
   - **建议**：以 `ServerSetting.json` 为**唯一写入口**（服务端启动时用它覆盖存档），面板 UI 只编辑它；
   - 存档编辑仅用于**只读展示**与**高级/离线修正**（如导入外部存档后调参），且必须**在实例停止时**进行；
   - **禁止两边同时改**，否则会出现"改了不生效"或"重启后回退"的困惑。
   - （该覆盖行为**需在阶段 0 用实验确认**：改 `ServerSetting.json` → 启动 → 检查存档对应字段是否被覆盖。→ 加入 V0-5）

### 6.4 文件管理

- 目录树懒加载（大目录分页）；上传分片（大文件）；下载走 `http.ServeContent` 支持断点。
- `unzip` 到目标目录需逐条校验路径并防 zip-slip；对 `Plugins`/`NetMods` 提供"上传即解压"快捷入口。
- 内置文本编辑器（限 `.json/.xml/.txt/.properties`，建议 <2MB）。

### 6.5 备份策略（三级）

| 类型 | 触发 | 保留策略 | 说明 |
|---|---|---|---|
| `pre-start` | 每次启动前 | 保留最近 3 份 | 强杀/崩溃风险兜底 |
| `scheduled` | cron（默认每日 4:00） | 保留 N 份或 N 天 | 主要恢复手段 |
| `manual` | 用户手动 | 手动管理 | 大版本前留存 |

- 备份**仅在实例停止时**做文件级拷贝最安全；运行时备份需接受一致性风险（建议先发一次保存/优雅停，或直接对 `Regions` 做尽力拷贝并在 UI 标注"热备份，可能不一致"）。
- 与 `.bak` 协同：备份时一并收集 `Project.json.bak`。
- 去重与压缩：zip + `sha256` 去重；超大备份支持"仅保留最近 N 份，超龄删除"。
- **异地**：支持可选 rclone/S3 目标（后期）。

### 6.6 监控指标

- 通过 `/proc/<pid>/stat`、`/proc/<pid>/status`、`/proc/<pid>/io`、`/proc/<pid>/fd` 采集：CPU%、RSS、线程数、FD 数、字节读写。
- 运行时长、重启次数、最后退出码。
- 在线玩家数：依赖 V0-1（指令 `player list` 解析）或后续自研插件；暂不可用时 UI 显示"需指令通道"。
- 端口监听：UDP 绑定探测（`net.ListenPacket("udp", ":port")` 失败即被占用）。
- 宿主指标：负载、内存、磁盘剩余（服务端启动日志也有 `Storage.AvailableFreeSpace`，可交叉校验）。

### 6.7 调度与通知

- 内置 cron（`robfig/cron/v3`）：定时备份、定时重启、定时广播。
- 通知渠道抽象 `Notifier` 接口，实现 Webhook（钉钉/企微/QQ Bot/Discord/自定义）；事件：启动成功、崩溃、备份完成/失败、磁盘不足。

---

## 7. 项目结构

```
scnetm/
├─ cmd/scnetm/main.go              # 入口：加载配置、迁移、起 HTTP
├─ internal/
│  ├─ api/                         # 路由、handler、中间件(auth/rbac/ratelimit/audit)
│  ├─ supervisor/                  # 进程与日志中枢
│  │   ├─ runner.go                #   启停重启、进程组、信号（_linux.go）
│  │   ├─ pty.go                   #   【D1】PTY 分配、尺寸同步、降级判定
│  │   ├─ logpipe.go               #   字节流分帧、环形缓冲、双写落盘、轮转
│  │   ├─ state.go                 #   状态机与就绪判据
│  │   ├─ cmdchannel.go            #   指令通道接口（pty 主 / fifo 备）
│  │   └─ metrics.go               #   /proc 采集
│  ├─ ansi/                        # 【D1】ANSI/CSI/OSC 解析与剥离、不完整序列容错
│  ├─ config/                      # ServerSetting.json / Settings.xml / Configs 读写与校验
│  ├─ world/                       # 存档扫描/导入导出/备份还原
│  ├─ files/                       # 安全文件操作（路径校验、zip 安全解压）
│  ├─ store/                       # SQLite + 迁移 + 仓储（含 users/owner_id 扩展位）
│  ├─ auth/                        # JWT、bcrypt、RBAC（单用户模式已就位但收敛）
│  ├─ scheduler/                   # cron 任务
│  ├─ notify/                      # 通知渠道
│  └─ runtime/                     # dotnet 运行时检测/引导
├─ web/                            # Vue3 + fuxsto-design 前端（构建产物 embed 进二进制）
│  └─ src/components/ui/           #   对 fuxsto-design 的薄适配层（便于替换/打补丁）
├─ deployments/                    # Dockerfile(多阶段)、systemd unit、compose
└─ docs/                           # 含阶段 0 的验证报告
```

**运行时检测（`internal/runtime`）**：面板启动时执行 `dotnet --list-runtimes`，校验存在 `Microsoft.NETCore.App 10.x`；缺失则给出明确指引（或用 `dotnet-install.sh` 引导安装到面板目录）。这是**部署成功的头号前提**。

---

## 8. 前端设计

> 技术栈：**Vue 3.5 + Vite + Tailwind CSS v4 + fuxsto-design**（§4.4），终端用 `xterm.js`，图表用 `uplot`。

页面：
1. **总览**：实例卡片（状态灯、端口、在线数、CPU/内存、快捷启停）。用 `card` + `statistic` + `badge` + `progress`。
2. **实例详情**
   - **控制台（核心页面）**：外层 `fuxsto-design` 的 `card`/`tabs` 工具栏，内层 **`xterm.js`** 渲染 ANSI 彩色输出。含日志流、指令输入、命令提示（从 `help` 解析出可用命令）。
     - 色彩模式指示：显示当前是"彩色（增强终端）"还是"无色（基础模式降级）"徽标 —— 直接对应 D1 的验收点。
     - 终端尺寸联动：`FitAddon.fit()` 后通过 WS 上报行列数 → 后端 `pty.Setsize`。
     - 工具：清屏、下载日志、复制、暂停自动滚动、按级别过滤（`virtual-list` 承载海量行）。
     - 指令历史（上下键）、常见指令快捷按钮（`/player list`、`/time`、`/stop`）。
   - **配置**：表单化 `ServerSetting.json`（世界名/种子/人数/**游戏模式下拉**/PVP/季节/昼夜速度）+ 高级 XML/JSON 编辑器。用 `form`、`input`、`input-number`、`select`、`switch`、`slider`。
   - **存档**：列表、导入导出、备份还原、切换启用。用 `table` + `upload` + `popconfirm`。
   - **文件**：树形浏览、上传下载、解压。用 `tree` + `virtual-list`。
   - **监控**：时序图（CPU/内存/在线数）用 `uplot`。
3. **备份中心**、**任务计划**、**用户与权限**（单用户下收敛为一个"账户"页，但路由与组件已按多用户预留）、**审计日志**、**系统设置**（运行时路径、模板包、通知、备份策略）。

**主题**：默认深色（`.dark`），贴合服务器控制台场景；`fuxsto-design` 的 monochrome zinc 风格天然契合。

交互要点：危险操作二次确认（含"输入实例名确认"）；配置保存后提示生效方式；崩溃时控制台自动跳转并高亮错误行。

---

## 9. 部署方案

### 9.1 裸机（推荐起步）

```bash
# 1) 安装 .NET 10 运行时
curl -sSL https://dot.net/v1/dotnet-install.sh | bash -s -- --channel 10.0 --runtime dotnet

# 2) 放置服务端模板包
mkdir -p /srv/scnetm/templates/server-x26.07.01.01
unzip 服务端X26.07.01.01.zip -d /srv/scnetm/templates/server-x26.07.01.01

# 3) 运行面板
./scnetm --config /etc/scnetm/config.yaml
```

`config.yaml`（面板自身，含路径、监听、端口池、备份策略、通知）：

```yaml
listen: "127.0.0.1:8080"
data_dir: "/srv/scnetm"
instances_dir: "/srv/scnetm/instances"
template_dir: "/srv/scnetm/templates/server-x26.07.01.01"
dotnet_path: "/root/.dotnet/dotnet"
port_pool: [28887, 28900]
term: "xterm-256color"          # 【D1】PTY 终端类型，影响服务端配色
color_mode: "enhanced"          # enhanced(PTY 彩色) | basic(降级无色)
defaults:
  stop_timeout_sec: 30
  auto_backup: { enabled: true, cron: "0 4 * * *", keep: 7 }
```

systemd unit 要点：`Restart=on-failure`、`KillMode=control-group`、`LimitNOFILE`、独立用户、`WorkingDirectory`。**注意**：面板需要以能创建进程组、创建 PTY、写实例目录的用户运行。

> **PTY 与 systemd**：面板自身不需要 `TTYPath`（它用 `openpty` 自建 PTY，不继承 systemd 的终端）。但需确保 `PrivateDevices=no`（默认），否则 `/dev/ptmx` 不可用 —— 这会直接导致彩色终端失效。

### 9.2 Docker

```dockerfile
FROM mcr.microsoft.com/dotnet/runtime:10.0 AS runtime
RUN apt-get update && apt-get install -y --no-install-recommends tini && rm -rf /var/lib/apt/lists/*
COPY scnetm /usr/local/bin/scnetm
COPY web/dist /app/web
VOLUME /srv/scnetm
EXPOSE 8080
ENTRYPOINT ["/usr/bin/tini", "--", "/usr/local/bin/scnetm"]
```

要点：
- 基于 **`dotnet/runtime`**（**不是** `aspnet`，更不是 Windows 镜像）即可，因为服务端只需 .NET 运行时；
- **PTY 在容器内需要设备访问（D1 关键）**：必须确保 `/dev/ptmx` 与 `/dev/pts` 可用。推荐：
  - `docker run --tty ...`（分配 tty，最省事）；或
  - 显式挂载 `--device /dev/ptmx` 并保证 `/dev/pts` 已挂载（默认 devpts 通常可用）。
  - **若二者都不可用，容器内将降级为无色模式** —— 因此 §10 的 V0-1/V0-3 必须**在容器内验证**，并把所需参数写进 `docker-compose.yml`。
- 实例目录挂卷；用 `tini` 处理 PID 1 信号与僵尸进程回收（面板要拉起多个子进程，这点很重要）；
- 多实例可"一容器一实例"隔离，也可"一容器多实例"省资源，建议前者更安全。
- **构建视角补充**：前端构建需 Node 阶段（多阶段构建），利用 `fuxsto-design` 的样式自包含特性，`web` 阶段无需额外 Tailwind 扫描配置。

### 9.3 升级流程

- 面板与**服务端模板包**解耦：升级服务端 = 替换 `template_dir`，新实例用新版，旧实例不动（避免存档不兼容）。
- 面板升级：替换二进制 + 自动迁移 SQLite。
- 存档兼容：跨大版本前**强制备份**并在 UI 提示版本变化。

---

## 10. 里程碑与验收

> 采用"先打掉风险，再堆功能"的顺序。每个阶段结束都是**可演示**状态。

### 阶段 0：技术验证（2~3 天）— 最高优先级

| 任务 | 内容 | 验收标准 |
|---|---|---|
| V0-1 | **PTY 彩色终端验证（D1 核心）** | 以 PTY 启动，日志出现 `EnhancedTerminalLogSink initialized With ANSI Support`（**非**"输出被重定向"）；通过 PTY 发 `help` 能收到**带 ANSI 色**的 `[help]: ...`；确认 `TERM` 取值与是否需要去回显；**在 Docker 容器内同样跑通** |
| V0-2 | **停止行为验证** | 确定 PTY 下 `Ctrl+C`(`\x03`)、`/stop`、`SIGTERM`、`SIGKILL` 各自效果；确认哪种能保证存档落盘；产出停止 SOP |
| V0-3 | **运行时与部署验证** | 干净 Linux 上仅装 .NET runtime 后，`start.sh` 路径可跑通；**Docker 内 PTY 可用**（`--tty` 或需何种 `docker run` 参数） |
| V0-4 | **日志锚点完备性** | 触发玩家加入/离开、崩溃，采集真实日志文案（**含 ANSI 原文样本**），更新 §5.2.1 规则表 |
| V0-5 | **配置生效与优先级矩阵** | 逐项确认 `ServerSetting.json` 各字段是否需重启；**关键**：验证 `ServerSetting.json` 与存档 `GameInfo` 重叠字段（`WorldName`/`GameMode`/`Password`/`DaySpeed`/`MaxOnlinePlayerCount`/`PVPEnabled` 等）**谁覆盖谁**，据此冻结写入策略（§6.3.1） |
| V0-6 | **GameMode 实测校验（D5）** | 逐个取值启动，比对 `Loaded world, GameMode=XXX`，锁定真实映射并更新 §6.2.1；同时确认存档内 `GameMode` 字符串与 `ServerSetting.json` 数字的双向映射 |
| V0-7 | **玩家列表与存档写盘时机** | 实测 `/player list 0` 的输出格式（含中文名）以设计解析规则；观察 `Project.json`/`.bak` 的写盘时机（启动时/周期/退出时），据此定备份与停止 SOP |

**阶段 0 交付物**：`docs/验证报告.md`（含结论、**含 ANSI 的原始日志样本**、决策），并据此**冻结终端通道与停止策略的实现方案**。

### 阶段 1：MVP（1.5~2 周）

- 实例 CRUD + 模板化创建 + 端口分配
- **PTY 启动/停止/重启/强杀 + 状态机 + 就绪判据 + 尺寸联动**
- **日志采集（保色原文 + 无色纯文本双写）、环形缓冲、WebSocket 实时控制台（xterm.js）、历史日志检索/下载**
- **指令下发（PTY 写 stdin）+ 命令提示**
- `ServerSetting.json` / `Settings.xml` 图形化配置 + 校验（含 GameMode 下拉）
- 单管理员登录（JWT；数据模型按 D3 预留多用户）
- 前端：总览 + 实例详情（控制台 + 配置），接入 `fuxsto-design`

**验收**：从零创建一个实例 → 图形化配置世界 → 一键启动 → **控制台显示彩色输出**且看到 `开启服务器成功，端口 x` 与 `Entered screen "Game"` → 发 `help` 有彩色回复 → 用游戏客户端成功进入 → 一键停止且存档保留。

### 阶段 2：可用性（2 周）

- 存档管理（列表/导入/导出/切换/删除）
- 备份与还原（三级策略）+ 备份中心 UI
- 文件管理（浏览/上传/下载/解压，含安全防护）
- 监控指标与图表（`uplot`）
- 审计日志（**多用户 RBAC 接口已就位，UI 暂只暴露账户页**）
- 指令增强（FIFO 双通道若 V0-1 证实可用）

**验收**：删除实例存档可从备份完整还原并正常开服；文件 API 通过路径穿越测试；终端在断线重连后能回放历史。

### 阶段 3：生产化（2 周）

- 定时任务（备份/重启/广播）、通知渠道
- 崩溃自动重启 + 退避
- 配置模板与批量建服
- Docker/systemd 部署产物、安装脚本
- 日志轮转、配额告警、性能压测（10 实例并发）

**验收**：10 实例同时运行，面板内存 <300MB，日志不丢行；崩溃后 60s 内自动恢复；备份任务连续 7 天无失败。

### 阶段 4：进阶（按需）

- 自研 `ServerPlugin` 暴露 RCON-like 接口（精准拿在线玩家、广播、踢人、查时间）
- 一键更新服务端版本（含存档兼容检查）
- S3/rclone 异地备份
- 多机 Agent 模式（面板 + 被控节点）

---

## 11. 风险与对策

| 风险 | 影响 | 概率 | 对策 |
|---|---|---|---|
| **PTY 不可用**（容器/沙箱禁用 `/dev/ptmx`） | **彩色输出失效**（违背 D1），退化为无色 | 中 | 阶段 0 在裸机+容器双环境验证；`--tty` 或挂 `/dev/ptmx`；UI 显式告警降级；`--enhanced` 兜底尝试 |
| **指令通道不可用**（PTY 写入无效且 FIFO 不存在） | 无法实时踢人/广播/查玩家 | 低-中 | PTY 写 stdin 是主方案，可靠性高；阶段 0 验证；兜底走"配置热改+重启"；长期自研插件 |
| **`/stop` 空实现**（源码事实） | 无法优雅停服，可能丢存档 | **高** | 停止走 PTY `Ctrl+C` → 进程组信号；**启动前强制备份**；验证各停止路径行为 |
| **服务端无官方优雅停止/保存接口** | 数据一致性风险 | 中 | 用 `Project.json` mtime 判断落盘；缩短备份周期 |
| **PTY 回显导致日志重复** | 控制台出现重复行，影响可读性 | 中 | 区分发送回显与真实输出；启动时尝试 `stty -echo`；必要时前端按来源标记 |
| **ANSI 序列跨读取块被截断** | 解析异常、颜色错乱 | 中 | 字节流分帧而非按行切；剥离正则容忍不完整序列；保留原始流不改写 |
| **UI 库较新（1.0.x）** | 组件缺陷/破坏性变更 | 中 | 锁定 `~1.0.5`；封装 `components/ui/*` 薄适配层便于替换与打补丁 |
| **缺终端/图表组件** | 无法满足控制台与监控需求 | 低 | 终端用 xterm.js（本就必需）；图表用 uplot（§4.4） |
| 服务端崩溃/内存泄漏 | 实例不可用 | 中 | 自动重启 + cgroup 内存限制 + 崩溃告警 |
| 存档格式变动（类型标注 JSON） | 面板定点读写失效 | 中 | **只做定点读写与整包备份**，不整体反序列化；加版本探测 |
| .NET 运行时缺失/版本不符 | 服务端起不来 | 中 | 启动前 `dotnet --list-runtimes` 校验 + 引导安装 |
| 端口冲突（UDP 特性易被忽略） | 实例起不来 | 中 | 端口池 + UDP 探测 + 冲突提示 |
| 中文/编码问题 | 控制台乱码、日志解析失败 | 中 | 强制 `LANG=C.UTF-8`；解析器容错；提供编码设置项 |
| 面板被攻击（文件管理是重灾区） | 宿主被入侵 | 中 | 路径穿越/zip-slip 防护、限流、最小监听面、反代 TLS |
| 服务端版本升级导致存档不兼容 | 存档损坏 | 中 | 版本解耦 + 升级前强制备份 + 兼容提示 |
| 多实例资源竞争 | 宿主过载 | 中 | 实例配额、并发上限、资源监控告警 |
| **GameMode 映射推断错误** | 配置页选错难度 | 低 | V0-6 实测锁定；校验前 UI 标注"推断值" |
| **`WorldPath`/`WorldName` 混用** | 切换存档失败、目录与预期不符 | **高（已实测踩中）** | 明确以 `WorldPath` 为目录唯一来源（§2.7）；建实例时生成 `<slug>` 目录；校验器禁非法目录名 |
| **双配置源覆盖冲突**（`ServerSetting.json` vs 存档 `GameInfo`） | "改了不生效/重启回退" | **高** | 以 `ServerSetting.json` 为唯一写入口；存档仅只读展示+停机高级编辑；V0-5 实测覆盖关系 |
| **写配置抹掉服务端新增字段** | 服务端升级后配置损坏 | 中 | 读写保留未知字段（`map[string]any`/`RawMessage`）；写前备份 |
| **`Configs/*.json` 由闭源插件管理** | 字段变动导致解析失败 | 中 | 以透传编辑为主，不急于强类型；写前备份；解析失败不阻断 |
| **`.bak` 仅在退出/重启时产生** | 运行中崩溃可能丢最近改动 | 中 | 启动前强制备份；不依赖 `.bak`；V0-7 确认写盘时机 |

---

## 12. 开发与测试

- **单测**：`supervisor`（状态机、日志解析规则、信号处理）、**`ansi`（ANSI 剥离/分帧，含被截断序列的边界用例）**、`config`（读写与校验、保留未知字段）、`files`（路径穿越/zip-slip 用例集）、`world`（BOM 处理、扫描）。
- **PTY 集成测试**：在 CI 中真实 `openpty` 拉起服务端，断言**日志含 ANSI 序列**（`\x1b[` 存在）—— 这是 D1 的自动化守护。
- **集成测试**：用真实服务端包在 CI 中跑"创建→启动→就绪→发指令→停止"闭环（标记为 `slow`，nightly 执行）。**注意**：CI 需要 .NET 10 运行时；已实测可在 Linux 无头跑通，无需 X11。
- **契约测试**：日志解析规则对**真实日志样本（含 ANSI）**做快照测试，服务端升级导致文案变化时立即报警。
- **E2E**：Playwright 覆盖关键路径（建实例、启动、改配置、备份还原），并断言终端区**渲染出彩色**（canvas 像素或有色 span）。
- **测试数据**：把阶段 0 采集的真实日志样本（**原始含色 + 纯文本两份**）纳入 `testdata/`，防止规则退化。
- **UI 库隔离**：`web/src/components/ui/*` 做薄再导出，便于替换/打补丁，且单测聚焦此层。

---

## 13. 已确认决策 与 剩余待确认

### 13.1 已确认（不再询问）

| # | 项 | 结论 |
|---|---|---|
| D1 | 终端形态 | **优先仿真终端（PTY），保证彩色输出** |
| D2 | 平台范围 | **仅 Linux** |
| D3 | 用户模型 | **暂单人使用**，表结构与中间件预留多用户扩展 |
| D4 | UI 库 | **`fuxsto-design`**（Vue 3 + Tailwind v4），终端配 xterm.js，图表配 uplot |
| D5 | GameMode | 暂用推断映射（§6.2.1），V0-6 实测校验 |
| D6 | 交付范围 | 完整 MVP 到可上线 |

### 13.2 剩余待确认（不阻塞开工，可边做边定）

1. **部署形态**：单机多实例即可，还是**将来**要多机 Agent？（决定现在是否抽 `Agent` 接口；建议先抽一个极薄的接口，成本很低）
2. **目标规模**：单机预期实例数与同时在线人数上限？（决定资源限制与压测目标，建议按 10 实例设计）
3. **服务端版本升级**：是否需要面板内一键升级服务端（涉及存档兼容校验）？还是手工替换模板包即可？
4. **通知渠道**：需要接哪些（QQ Bot / 钉钉 / 企微 / Discord / 通用 Webhook）？
5. **反代与域名**：是否已有 Nginx/Caddy 与 TLS 证书？面板默认只监听 `127.0.0.1`。

---

## 附录 A：验证证据

以下为本次调研在本机实际执行的验证（环境：Linux x86_64，Go 1.27.1，Docker 29.8.1，临时安装 .NET Runtime 10.0.12）。

### A.1 服务端包结构

```
$ unzip -l 服务端X26.07.01.01.zip
     19246636  net10.0/Content.scpak
       347136  net10.0/Engine.dll
      3456512  net10.0/Survivalcraft.dll
       222208  net10.0/Survivalcraft.exe
          346  net10.0/start.sh
          ...
```

### A.2 确认无 WindowsDesktop 依赖（"纯服务端"的关键）

```json
// net10.0/Survivalcraft.runtimeconfig.json
{"runtimeOptions":{"tfm":"net10.0",
  "framework":{"name":"Microsoft.NETCore.App","version":"10.0.0"},
  "configProperties":{"System.Runtime.Serialization.EnableUnsafeBinaryFormatterSerialization":false}}}
```

对比：客户端包 `[电脑版]SCNETx26.02.05.zip` 的 runtimeconfig 含
`"frameworks":[{"name":"Microsoft.NETCore.App","version":"9.0.0"},{"name":"Microsoft.WindowsDesktop.App","version":"9.0.0"}]`
—— 证明**客户端需要 Windows 桌面栈，服务端不需要**。

### A.3 Linux 无头自动开服实测（决定性证据）

配置 `ServerSetting.json`：`Autorun=true`、`AutoGenerateWorld=true`、`WorldName=PlanTest`、`WorldSeed=12345`；执行 `dotnet Survivalcraft.dll`；关键输出：

```
使用游客模式启动，清理SCKey服务端绑定信息
[自动检测] 输出被重定向，使用基础终端模式
[提示] 如需强制增强模式，请添加 --enhanced 参数
10:16:39.885 INFO: [StartServer]开启服务器成功，端口 28887
10:16:40.518 INFO: Loaded world, GameMode=Harmless, StartingPosition=Easy, WorldName=PlanTest, VisibilityRange=128, Resolution=High
10:16:40.544 INFO: Entered screen "Game"
```

→ 结论：**无需 X11/Wine，Linux 可无人值守开服**，且产生稳定可解析的日志锚点。

### A.4 存档产物

```
Worlds/World/
├─ Project.json        # 3104 B，UTF-8 BOM + 类型标注 JSON
├─ Project.json.bak    # 服务端自带备份
└─ Regions/            # 区块数据
```

`Project.json` 片段：
```json
{"Version":["string","2.4"],"Guid":["System.Guid","9e9a67f8-..."],
 "Name":["string","GameProject"],
 "Subsystems":{"Players":{"BlackPlayerGuidList":{},"NoMsgPlayerGuidList":{},...}}}
```

### A.5 控制通道与彩色终端线索（程序集字符串）

```
CommandFifoPath
FifoCommandMonitor
command-fifo          # 与 'ServerSetting.json' 在常量区相邻
EnhancedTerminalLogSink initialized With ANSI Support: 增强终端安全退出
[自动检测] 输出被重定向，使用基础终端模式      # 实测运行时输出
[提示] 如需强制增强模式，请添加 --enhanced 参数 # 实测运行时输出
```

→ 两条互斥路径确认：**管道/重定向 = 基础模式（无色）**；**TTY 或 `--enhanced` = 增强模式（ANSI 彩色）**。这是 D1 选择 PTY 的直接依据。

### A.5.1 `WorldPath` vs `WorldName` 对照实验（决定性）

**背景**：首轮测试中 `WorldName=PlanTest` 但目录是 `World`，暴露出两者可能不是同一概念 —— 遂设计对照实验。

```jsonc
// 刻意把两者设为不同值
"WorldPath": "app:/Worlds/MyWorldA",
"WorldName": "ShowNameX"
```

实测输出与文件系统结果：

```
INFO: 已自动生成存档目录 app:/Worlds/MyWorldA      ← 目录来自 WorldPath
INFO: Loaded world, ..., WorldName=ShowNameX       ← 显示名来自 WorldName

$ ls Worlds/
MyWorldA                                           ← 目录名 = WorldPath 末段
```

存档内 `GameInfo` 亦相应记录：

```json
"WorldName":          ["string", "ShowNameX"],
"WorldDirectoryName": ["string", "app:/Worlds/MyWorldA"]
```

→ **结论**：目录由 `WorldPath` 决定，`WorldName` 仅显示用。**切换存档必须改 `WorldPath`**。此结论已写入 §2.7 与 §6.3。

### A.5.2 存档 `GameInfo` 字段全量（实测一次性 dump）

见 §6.3.1 的完整表格。以下为原始提取命令与关键片段：

```bash
$ python3 -c "import json;d=json.load(open('Worlds/MyWorldA/Project.json',encoding='utf-8-sig'));
              gi=d['Subsystems']['GameInfo'];
              [print(f'{k:34}', json.dumps(v,ensure_ascii=False)) for k,v in gi.items()]"
```

关键发现：
- **`GameMode` 在存档内是字符串** `["Game.GameMode","Harmless"]`，而 `ServerSetting.json` 中是数字 `1` → 需双向映射。
- **`WorldSeedString="999"` 与 `WorldSeed=5130` 并存** → 后者是派生值，面板只应写前者。
- `Project.json` 带 **UTF-8 BOM**；`.bak` 仅在退出/重启时生成（观测到 `Project.json` 10:20 而 `.bak` 10:18）。

### A.5.3 `.bak` 写盘时机观测

```
/tmp/scnetsrv/net10.0/Worlds/World/
  Project.json      10:20   ← 第二次启动后
  Project.json.bak  10:18   ← 第一次会话结束时
```

→ `.bak` 在**会话结束/退出**时产生，说明运行中的改动未必即时落盘。这支撑了"启动前强制备份"与"停止走优雅路径"的设计（§5.4、§6.5），并派生任务 V0-7。

### A.6 UI 库核实（fuxsto-design）

```bash
$ curl -s https://registry.npmmirror.com/fuxsto-design | jq '{name,description,license,latest:."dist-tags"}'
{
  "name": "fuxsto-design",
  "description": "Monochrome zinc, Tailwind CSS v4 component library for Vue 3 (fuxsto-ui)",
  "license": "MIT",
  "latest": { "latest": "1.0.5" }        # 发布时间 2026-10-02
}
# peerDependencies: vue ^3.5.0, tailwindcss ^4.0.0, lucide-vue-next ^0.577.0
# dependencies: clsx, tailwind-merge, @floating-ui/vue
# 导出子路径数量: 82（button/table/form/tree/upload/virtual-list/streaming-text 等）
```

**结论**：Vue 3 + Tailwind v4、MIT、样式自包含、暗色模式、按需引入；**无终端组件、无图表组件**（需补 xterm.js 与 uplot）。

### A.7 GameMode 枚举证据

```
实测：ServerSetting.json "GameMode": 1  →  日志 "Loaded world, GameMode=Harmless"
源码：Subsystem/SubsystemMatchBlockBehavior.cs:65
      if (m_subsystemGameInfo.WorldSettings.GameMode < GameMode.Challenging || ...)
      → 枚举支持大小比较，证明按数值有序
程序集字符串确认存在：Creative / Harmless / Survival / Challenging / Cruel
```

→ 推断映射 `0=Creative, 1=Harmless, 2=Survival, 3=Challenging, 4=Cruel, 5=Adventure, 6=Mirror`（见 §6.2.1，待 V0-6 实测校验）。

### A.8 未能验证的项（本机沙箱限制）

以下项**仅为推断或源码结论，尚无运行时证据**，均已在阶段 0 安排实测：

| 项 | 现状 | 依据强度 | 验证任务 |
|---|---|---|---|
| **PTY 彩色输出** | 仅程序集字符串 `EnhancedTerminalLogSink initialized With ANSI Support` | 中 | V0-1 |
| **PTY 指令下发与 `isTerminal` 免权限** | 仅源码阅读（`CmdManager`/`AbstractProcessCmd`）；**闭源 `Program` 如何置位 `isTerminal` 未知** | 中 | V0-1 |
| **`command-fifo` 的路径与启用条件** | 仅程序集字符串（`CommandFifoPath`/`FifoCommandMonitor`） | 低-中 | V0-1 |
| **`--enhanced` 在管道下是否生效** | 仅日志提示文案 | 低 | V0-1 |
| **双配置源覆盖关系** | 未验证 | 低 | V0-5 |
| **玩家列表输出格式** | 仅源码逻辑（需 `/player list <页>`） | 中 | V0-7 |
| **存档写盘时机** | 仅观测到 `.bak` 在会话结束时产生 | 中 | V0-7 |

环境限制说明：
- **PTY 无法在本机分配**：`pty.openpty()` → `OSError: out of pty devices`；`script` → `failed to create pseudo-terminal: Permission denied`。沙箱禁止伪终端，故凡涉及 PTY 的结论均未取得运行时证据。
- 未能连入真实游戏客户端验证联机体验（需客户端环境）。
- 未能验证容器内 PTY 可用性（同因）。

### A.9 主要参考文件

- 服务端发行包：`服务端X26.07.01.01.zip`（源码仓库 `历史版本/` 亦含客户端包）
- 源码仓库：<https://gitee.com/SC-SPM/SurvivalcraftNet>
- 关键源码：`Server/EssentialCmd/*.cs`（含 `CmdPlayer.cs`/`CmdStop.cs`/`CmdHelp.cs`）、`Server/CmdManager.cs`、`Server/AbstractProcessCmd.cs`、`NetWork/NetNode.cs`、`NetWork/CommonLib.cs`、`Subsystem/SubsystemGameInfo.cs`、`ModsManager/ModsManager.cs`（`app:` 前缀映射）
- UI 库：`fuxsto-design@1.0.5`（MIT）— <https://npmmirror.com/package/fuxsto-design>

---

## 附录 B：本轮推敲修正记录

第二轮复核针对"**声称已确认但实际未验证**"的内容逐条排查，修正如下（均为实质性问题，非文字润色）：

| # | 原计划的错误/不足 | 修正 | 影响 |
|---|---|---|---|
| 1 | 把 `WorldName` 当作存档目录名（首轮实测中 `WorldName=PlanTest` 却出现 `World` 目录，被我忽略） | 对照实验证明**目录由 `WorldPath` 决定**，新增 §2.7 与 A.5.1；明确"切换存档必须改 `WorldPath`" | **高** — 否则多存档功能直接失效 |
| 2 | 称 `CmdManager` 免权限行为为"实测" | 改标注为**源码阅读**，并指出闭源 `Program` 如何置位 `isTerminal` 未知 | 中 — 避免误导实现 |
| 3 | 猜测"存档内 `GameMode` 与配置同为数字" | 实测为**字符串** `"Harmless"`，需双向映射 | 中 — 影响配置读写 |
| 4 | 未发现 `WorldSeed`/`WorldSeedString` 并存 | 明确**只写 `WorldSeedString`**，`WorldSeed` 为派生只读 | 中 — 否则种子不一致 |
| 5 | 严重低估存档可配置字段（只列了 `Name`/`Guid`） | 新增 §6.3.1 **34 个 `GameInfo` 字段全表**（地形模式、岛屿尺寸、海平面、生物群系等） | 高 — 直接扩大面板能力边界 |
| 6 | 未发现 `ServerSetting.json` 与存档 `GameInfo` **字段重叠** | 明确写入优先级策略（以 `ServerSetting.json` 为唯一写入口），新增 V0-5 验证 | **高** — 否则出现"改了不生效/重启回退" |
| 7 | 称 `/player list` 可查玩家 | 实测源码要求 **`/player list <页码>`**，裸写只回帮助 | 中 — 影响在线玩家采集 |
| 8 | 称 `Configs/*.json` 可强类型 | 指出这些文件由**闭源插件**产生，字段可能变动 → 以透传编辑为主 | 中 |
| 9 | 未注意写配置可能抹掉服务端新增字段 | 要求保留未知字段（`map[string]any`/`RawMessage`） | 中 |
| 10 | 未发现 `.bak` 仅在会话结束时产生 | 补充写盘时机观测（A.5.3）并新增 V0-7 | 中 — 强化备份策略依据 |

**仍未解决、且无法在当前环境解决的核心项**：PTY 彩色输出与指令通道（沙箱禁止分配伪终端）。这是整个 D1 决策的**唯一未验证基石**，已固化为阶段 0 第一任务 V0-1，其验收标准要求拿到运行时证据（含 Docker 环境）。

---

## 附录 C：阶段 1 实现落地记录（实测证据，2026-10）

本附录记录**实际编码落地**过程中取得的证据与对前述计划的修正。与附录 B 同样的规矩：**只写实测到的，推断明确标注为推断**。

### C.1 交付状态

| 包 | 覆盖 | 状态 |
|---|---|---|
| `internal/ansi` | 92.7% | ✅ 218 测试通过；含分块拆分的 ANSI 序列、`\r` 覆写、OSC/CSI |
| `internal/supervisor` | 90.4% | ✅ 101 测试通过，**0 skip**；PTY 降级用「断言降级契约」而非跳过 |
| `internal/store` | 83.9% | ✅ 95 测试通过 |
| `internal/config` | 85.2% | ✅ 81 测试通过 |
| `internal/files` | 84.2% | ✅ `Resolve` 达 **100%** 语句覆盖 |
| `internal/world` | 79.3% | ✅ 含 §A.5.1 陷阱（目录名≠显示名）回归测试 |
| `internal/api` + `internal/auth` | — | ✅ 全路由实现；未接线功能返回 501 `not_implemented` |
| `internal/runtime` | 96.3% | ✅ .NET 10 探测；dotnet 缺失时优雅降级 |
| `internal/notify` | 91.7% | ✅ 5 通道 + 去重 + 退避 |
| `internal/scheduler` | 93.8% | ✅ 重叠保护 + panic 恢复 + 优雅停止 |
| `internal/backup` | 83.7% | ✅ 三级保留 + sha256 校验 + 损坏归档不破坏原存档 |
| `web/` | — | ✅ `type-check` 零错，`build` 成功 |

**整体**：`go build ./...` = 0，`go vet ./...` = 0，`go test ./...` **13 个包全绿**。

### C.2 修正：`fuxsto-design` 假设已实测确认（§4.4 / A.6 结论成立）

计划对 `fuxsto-design` 的描述**经实际安装 1.0.5 后逐项核对，全部属实**：82 个导出子路径一致、`@import "fuxsto-design/styles"` 确实可解析（`dist/styles.css`，112,447 字节，样式自包含，无需 `@source`/safelist）、无终端组件、无图表组件。

两点计划未覆盖、实现时踩到的细节：
1. **`select` 用 `options` prop（`{label,value}[]`），不是 children** —— 按 shadcn 风格写 `<Select><Option/></Select>` 无法编译。
2. **`statistic` 是动画导向组件**（`from`/`duration`），在密集实例网格里过大 → 需自建紧凑 `UiStatTile`。

### C.3 修正：`ServerPort` 不在 `ServerSetting.json` 里（强化 §2.6）

实现中两处独立地误以为端口在 `ServerSetting.json`，均被纠正：**端口只在 `Settings.xml`**（`ServerPort` 元素）。已加回归测试，若 `ServerPort` 重新出现在 `ServerSetting.json` 夹具或 config `extra` 映射中则测试失败。

### C.4 新增：`config.PathSegment` 的 NUL 字节校验缺口（安全）

实测发现 `config.PathSegment("app:/Worlds/a\x00b")` **返回 `"a\x00b", nil`** —— 未拒绝内嵌 NUL。该值会流入文件系统路径。已修复，并同时发现两个更隐蔽的变体：
- 只有非**末段**组件含 NUL 时（`app:/Worlds/a\x00b/World`）同样泄漏；
- `strings.TrimSpace` 会**静默剥离**首尾 NUL，导致 `"\x00app:/Worlds/World"` 被规整成干净的 `"World"`。

修复采用 `unicode.IsControl` 扫描**规范化后的完整路径**（而非仅末段、也非 TrimSpace 之后的副本）。Windows 保留名（CON/PRN/NUL…）**故意不拒绝** —— 在 Linux 上无意义，拒绝反而会误伤合法目录名。

### C.5 新增：实例无法启动的集成缺陷（已修复）

**症状**：`CreateInstanceRequest` 无 `server_jar`/`dotnet_path` 字段 → 客户端传的值被**静默丢弃** → DB 行落空 → 每次启动都 `500 internal_error`（真实原因 `instance has no server dll configured`）。**实例能建但永远起不来，且事后无法配置。**

**修复**：DTO 增加两个字段，创建时持久化，`ServerJar` 缺省为 `"Survivalcraft.dll"`（§2.1）。

**顺带修掉**：`internal/api` 存在未使用的 `sync.Once` 导致该包**根本无法编译**；`main.go` 里 API 挂载点始终是 `TODO`，导致**所有 `/api/v1/*` 请求都返回 SPA 的 HTML 且状态码 200**（客户端看起来像成功）。现已挂载 `/api/v1/` 与 `/ws/`。

### C.6 实测：完整链路可用（真实 HTTP + 真实 SQLite）

```
setup-required → POST /auth/setup → POST /auth/login → GET /auth/me
→ POST /instances (201，server_jar/dotnet_path 已落库)
→ 目录自动provision：ServerSetting.json / Settings.xml / Configs/ / Worlds/
→ POST /instances/1/start → 状态机 Starting → Failed
   last_error = "process exited before readiness (code 0)"   ← 对 /bin/echo 而言完全正确
→ logs/<date>.log 与 <date>.plain.log 双写已生成
```
鉴权与错误语义均符合 §5.6/§5.7：未认证 → `401 unauthorized`；未接线功能 → `501 not_implemented` 且带 `details.reason`（**不是空 200**，符合设计意图）。

### C.7 PTY（D1）结论强化：**沙箱人为限制，非主机限制**（推断，中高置信）

独立复验设备写入行为，得到决定性证据：

| 设备 | RDONLY | WRONLY | RDWR |
|---|---|---|---|
| `/dev/null` | ✅ | ✅ | ✅ |
| `/dev/zero` | ✅ | ❌ EACCES(13) | ❌ EACCES(13) |
| `/dev/ptmx` | ✅ | ❌ EACCES(13) | ❌ EACCES(13) |

`/dev/null` 与 `/dev/zero` 的 DAC 权限位、rdev 类别、挂载点**完全相同**，却只有 `/dev/null` 可写 —— 任何基于权限/挂载的策略都无法区分二者，**只能是"仅放行 `/dev/null` 的按 inode 写入白名单"**，典型沙箱特征。PTY 并未被特别针对。

**诚实标注**：机制**未被完全解释** —— Landlock ABI 报告为 1，而 ABI 1–3 只限制文件系统访问，设备 open 管控需 ABI≥4（内核 5.15 不满足）；`Seccomp: 0`、AppArmor `unconfined`。因此这是一条**推断**，不是已证结论。

**一次性判定实验**：在**非沙箱**会话（普通 SSH）中以同一用户执行 `python3 -c 'import pty; pty.openpty()'`。成功 ⇒ 沙箱人为限制 ⇒ D1 在真实目标上可行。

另注：Python 报的 `out of pty devices` 是**硬编码文案，与真实原因（EACCES）不符**，会误导后来者 —— `scripts/verify-env.sh` 已在旁打印 errno 以避免误判。

### C.8 V0-1 ~ V0-7 在本环境的可执行性

**V0-1/V0-2/V0-4/V0-6/V0-7 阻塞，V0-3/V0-5 部分可执行**（详见 `docs/验证报告.md`）。三重阻塞，任一即足：无游戏服务端发行包、无 .NET 10 运行时、无 PTY。Docker 亦不可用（用户不在 `docker` 组，daemon socket 拒绝访问），故容器内验证同样阻塞。

**投入产出最高的两件事**：(1) 把执行用户加入 `docker` 组 —— 一次性解锁最大验证面，且容器控制台大概率不受本机设备沙箱约束，可能顺带终结 D1 悬案；(2) 取得服务端发行包 —— 唯一无法自行解决的依赖。

### C.9 待人工确认的假设（**未验证，勿当结论用**）

1. **各通知渠道的 webhook 载荷格式**：钉钉/企业微信/Discord/QQ Bot 的 JSON 结构依据厂商文档编写，但**从未对真实端点联调**。测试断言的是"我预期的结构"。每家隔离在单个函数内，便于一行修正；仅通用 Webhook 可直接使用。
2. **GameMode 映射仍为推断**（§6.2.1/D5）：除 `1=Harmless` 外均未经实测，代码中以 `gamemode_inferred` **警告**（非错误）暴露，V0-6 负责锁定。
3. **`deployments/Dockerfile` 未经机器验证**（无 daemon 访问）；同目录的 compose 与 systemd 单元已通过 `docker compose config` 与 `systemd-analyze verify` 校验，两个 config yaml 也已用真实二进制启动验证。

---

## 附录 D：阶段 0 运行时验证突破（首次取得真实服务端证据）

**背景变更**：实现到收尾阶段时发现本机**已存在 .NET 10 运行时与真实服务端发行物**，此前"三重阻塞"中的两项解除：

| 资源 | 位置 | 状态 |
|---|---|---|
| .NET 运行时 | `/tmp/dotnet10/dotnet` | ✅ `Microsoft.NETCore.App 10.0.12` |
| 游戏服务端 | `/tmp/scnetsrv/net10.0/` | ✅ 含 `Survivalcraft.dll`、`Content.scpak`、`Configs/` 等 |
| PTY | — | ❌ 仍不可用（`EACCES 13`，设备类级沙箱限制，见 C.7） |

这是本项目**第一次拿到服务端真实运行时证据**，以下均为实测。

### D.1 §2.1「纯服务端、无 WindowsDesktop 依赖」——**独立证实**

服务端 `Survivalcraft.runtimeconfig.json` 实际内容：

```json
{ "runtimeOptions": { "tfm": "net10.0",
    "framework": { "name": "Microsoft.NETCore.App", "version": "10.0.0" } } }
```

**只有 `Microsoft.NETCore.App`，无 `Microsoft.WindowsDesktop.App`** → 计划 §2.1/§A.2 "能在 Linux 无头运行"的根因判断成立，且这次是从发行物本体而非程序集推断得来。

### D.2 §5.1 三锚点就绪判据——**实测成立**

面板接管真实服务端后，完整启动序列被正确解析，状态机到达 `running` 且 `ready=True`：

```
[自动检测] 输出被重定向，使用基础终端模式
[提示] 如需强制增强模式，请添加 --enhanced 参数
12:52:59 INFO: Entered screen "Loading"
12:53:00 INFO: Entered screen "Play"
12:53:01 INFO: [StartServer]开启服务器成功，端口 28887
12:53:02 INFO: Loaded world, GameMode=Harmless, StartingPosition=Easy, WorldName=PlanTest, VisibilityRange=128, Resolution=High
12:53:02 INFO: Entered screen "Game"
```

三个锚点（存活 ∧ `开启服务器成功，端口 N` ∧ `Entered screen "Game"`）全部命中，§5.2.1 规则表**在真实输出上验证通过**。另注意 `Entered screen` 序列比计划预想的多出 `Loading`/`Play`/`GameLoading` 三态 —— 规则表应对"最后一个 Game 才算就绪"保持宽松匹配（当前实现已如此）。

### D.3 §5.1 D1 核心分歧——**运行时证实「彩色必须 PTY」**

服务端自己打出了 `[自动检测] 输出被重定向，使用基础终端模式`，并且**原始日志中 ESC 字节数为 0**（10044 字节全无色）。这从运行时正面证实了：

1. basic / enhanced 两条路径**确实互斥存在**（此前仅有程序集字符串证据）；
2. **管道模式确实拿不到颜色** —— D1 "优先 PTY 保彩色"的决策前提**成立**；
3. 服务端还自曝了兜底参数：`如需强制增强模式，请添加 --enhanced 参数` → 该参数**确实存在**（计划 A.8 列为"仅日志提示文案，低置信"），其在管道下的实际效果待测。

### D.4 §5.4 停止行为——**首次实测数据（V0-2 部分）**

对真实服务端执行停止，结果为：

```json
{"instance_id":1,"forced":true,"exited":true,"exit_code":143,"duration_ms":30132,"state":"Stopped"}
```

即：**优雅路径（`/stop` 指令 + Ctrl+C）未能在 30s 预算内终止进程，梯子升级到 SIGTERM（143 = 128+15）后才退出**。这实测印证了计划把 `/stop` 空实现（`CmdStop.ProcessCmd()` 为空）列为**高风险**的判断。

**存档安全性正面结果**：停止后 `Worlds/World/Project.json` 的 mtime（12:53）**晚于** `Project.json.bak`（12:52），说明 SIGTERM 路径下**存档确实落盘**。这是计划 A.5.3 "`.bak` 仅在会话结束时产生"的补充证据，也支持 §6.5 "启动前强制备份"策略。

### D.5 指令通道——**管道模式下不可用（重要负面结论）**

在管道模式下经 WebSocket 控制台发送 `help`，**服务端无任何回应**（日志无对应记录，无回复帧）。结合 D.3 的 `输出被重定向` 与 `command-fifo` 仍未证实，**计划 §5.3 的主方案（PTY 写 stdin）在非 PTY 环境下不成立**，备选 FIFO 通道仍待验证。这正是计划列为"最大技术风险"的第 1 项，现有了运行时（否定性）证据。

> 结论：**指令通道的可用性与彩色输出绑定在同一前提上 —— 必须有 PTY。** 这使 V0-1 的优先级进一步升高：它不只是"配色好不好看"的问题，而是**玩家管理、广播、踢人等一整套功能的存亡前提**。

### D.6 定位：这三条证据不改变实现，但改变风险排序

- **不需要回退代码**：面板已按"PTY 优先 + 管道降级 + 降级显式告警"实现，在无 PTY 环境下自动落到 basic 模式并把 `Degraded()` 原因暴露给 UI —— 这正是实测所走通的路径。
- **需要调整的是优先级**：计划把 V0-1 排在第一是对的，但理由应升级为"**指令通道与彩色输出共用同一前提**"，而非仅"保证彩色输出"。若目标环境无法提供 PTY，则 §5.3 的兜底方案 C（配置热改 + 重启、直写 `BanConfig.json`、自研插件暴露 RCON-like 接口）应从"长期正解"提升为"近期必需"。

---

## 附录 E：交接说明（写给下一位接手者）

> 本附录是**交接文档**，不是技术结论。技术结论见附录 A–D 与 `docs/`。本附录只回答四件事：**现在什么状态、怎么跑起来、哪些结论可信、接下来该做什么。**

### E.1 一句话现状

**阶段 1（MVP 全量实现）已完成并可运行；阶段 0（V0-1~V0-7 运行时验证）完成度约 30%，未完成部分已明确标注。**

前 12 个任务（T1–T10）全部交付，13 个包测试全绿，生产二进制实测跑通完整链路。V0 验证因**中途才发现本机已有 .NET 10 与真实服务端**，只跑出了 V0-1/V0-2 的实测数据，其余 5 项的结构已就绪但结论未填。

### E.2 如何跑起来（三步，已实测）

```bash
cd /home/xgp2012/SCNETM

# 0) 必须：本机 ~/go/pkg/mod 不可写，Go 环境需重定向
export GOFLAGS=-mod=mod GOSUMDB=off GOPRIVATE='*' GOTOOLCHAIN=local \
       GOMODCACHE=$PWD/.gomodcache GOCACHE=$PWD/.gocache

# 1) 构建（注意：必须用 make，不能用裸 go build）
make build            # 会先把 web/dist 拷进 internal/webui/dist 再编译
                      # 裸 go build 会静默内嵌一个占位页，这不是 bug 是设计

# 2) 启动（8080 被无关应用 Licode 占用，务必换端口）
./build/scnetm --listen 127.0.0.1:34550 --data-dir /tmp/scnetm-data
```

**首次使用流程**（实测通过）：
```
GET  /auth/setup-required      → setup_required: true
POST /api/v1/auth/setup        → 设管理员密码，返回可用 token
POST /api/v1/auth/login        → token 在 .data.token
POST /api/v1/instances         → 建实例（记得传 server_jar / dotnet_path，见 E.5）
POST /api/v1/instances/:id/start
```
**指令走 WebSocket，没有 REST 命令路由**：`/ws/instances/:id/console?token=...`。

### E.3 环境关键事实（踩过的坑，别再踩一次）

| 事实 | 说明 |
|---|---|
| **本机有 .NET 10 与真实服务端** | `/tmp/dotnet10/dotnet`（`Microsoft.NETCore.App 10.0.12`）、`/tmp/scnetsrv/net10.0/`（含 `Survivalcraft.dll`）。**不要**再说"没有运行时/没有服务端" |
| **PTY 不可用** | `os.openpty()` → `EACCES 13`；`/dev/null` 可写而 `/dev/zero`、`/dev/ptmx` 不可写 → **设备类级沙箱限制，非主机限制**（详见 C.7） |
| **8080 被占用** | 无关应用 Licode。用别的端口 |
| **磁盘很紧** | 约 3.7–4.1 GB 可用（93% 已用）。注意 `Content.scpak` 有 19 MB，实例目录别乱拷贝 |
| **Docker 不可用** | 用户不在 `docker` 组，socket 拒绝访问 |
| **不要改 `go.mod`** | 模块路径已定为 `scnetm`（非 `github.com/...`），曾被代理反复改写，现已锁定 |
| **不要跑 `go mod tidy`** | 会因环境无网/模块缓存重定向而破坏依赖 |

**`internal/api` 的特殊注意**：`go test -race ./internal/api/` 需要 **≥500s** 超时（实测 475s 通过）。默认 600s 在 `-race` 下**会误报超时失败，那不是死锁也不是竞态**——普通模式 30s 就过。这一点已被误判过一次，写在这里避免重蹈。

### E.4 各包实测覆盖率（`-cover`，可复核）

```
runtime 96.3% | scheduler 93.8% | ansi 92.7% | notify 91.7% | supervisor 90.3%
auth 85.7% | config 85.2% | files 84.2%（Resolve 100%）| store 83.9%
backup 83.7% | world 79.3% | api 全路由 | web type-check 零错
```

### E.5 本轮修掉的 4 个真实缺陷（**别改回去**）

1. **实例永远起不来**（最严重）：`CreateInstanceRequest` 原本没有 `server_jar`/`dotnet_path` 字段，客户端传的值被**静默丢弃**，DB 落空，每次启动都 500。已在 DTO 加字段并在创建时持久化，`ServerJar` 缺省 `"Survivalcraft.dll"`。
2. **所有 API 返回 HTML**：`main.go` 的 API 挂载点一直是 `TODO`，导致 `/api/v1/*` 全部回落 SPA 且**状态码 200**（客户端看起来像成功）。现已挂载 `/api/v1/` 与 `/ws/`，并在 `cmd/scnetm/api.go` 接入 `supervisor.Manager`。
3. **`config.PathSegment` NUL 字节缺口**（安全）：`"app:/Worlds/a\x00b"` 曾返回 `"a\x00b", nil`。已用 `unicode.IsControl` 扫描规范化后的完整路径修掉。
4. **`internal/api` 编译不过**：未使用的 `sync.Once`（`router.go`）。已补 `sync` 导入。

另：**`LogSubscription.History(n)` 故意返回 nil**（`cmd/scnetm/api.go`）。`supervisor.Subscription` 把回放积压直接灌进 `Lines()` 通道，且没有独立的 replay 访问器；若在此处消费通道去填充缓冲，会与 WebSocket 读取方竞争并**吞掉实时行**。所以读路径保持单一所有者——历史与实时都由 `Lines()` 按序给出。

### E.6 结论可信度分级（**务必按此引用，勿越级**）

**A 级 · 实测已证实**（可放心依赖）
- 完整链路可用：setup → login → 建实例(201) → 启动 → 状态机 → 停止 → 干净退出，无孤儿进程
- `/healthz`、前端内嵌、`401` 鉴权语义、未接线功能返回 `501 not_implemented`（带 reason）
- 服务端 `runtimeconfig.json` 仅依赖 `Microsoft.NETCore.App`，**无** `WindowsDesktop` → Linux 无头可行（§2.1 成立）
- 启动三锚点就绪判据在真实输出上**成立**，状态机到达 `running, ready=True`
- **彩色必须 PTY**：管道模式下服务端自报 `输出被重定向，使用基础终端模式`，原始日志 ESC 字节数 **0**
- 停止方式实测：`/stop` **无效**、Ctrl+C **无效**、**仅 SIGTERM 有效**（rc=143）

**B 级 · 有实测但有重要保留**
- **停止不落盘**：实测 `PROJ advanced: NO / BAK advanced: NO`。但此前另一次运行观察到 `Project.json`(12:53) 晚于 `.bak`(12:52)。**两次观测矛盾，未定论** → 下一位必须专门复核这一点，它直接决定备份策略
- **管道模式指令通道不可用**：发 `help` 无任何回应。但**服务端不会自建 `command-fifo`**（已实测 `RESULT-1`），而"预创建 FIFO 后写入"的实验**未跑完** → 结论尚未封闭

**C 级 · 仅为推断，勿当结论**
- GameMode 映射（`0=Creative…6=Mirror`）：**除 `1=Harmless` 外全部未经实测**。代码中以 `gamemode_inferred` 警告暴露（V0-6 负责锁定）
- 各通知渠道 webhook 载荷（钉钉/企业微信/Discord/QQ Bot）：依厂商文档编写，**从未对真实端点联调**。仅通用 Webhook 可直接用。每家隔离在单个函数内，便于一行修正
- `deployments/Dockerfile` **未经机器验证**（无 daemon）；compose 与 systemd 单元已过 `docker compose config` / `systemd-analyze verify`

**D 级 · 明确未知**
- PTY 环境下的彩色输出、指令下发、`isTerminal` 免权限行为——本机无法测（需非沙箱环境或容器内 PTY）
- 玩家加入/离开/插件的真实日志文本（V0-4 未做）→ 卡在指令通道
- `ServerSetting.json` 与存档 `GameInfo` 的**重叠字段谁赢**（V0-5 未做）→ 影响"改了不生效/重启回退"

### E.7 建议的下一步（按投入产出排序）

1. **复核"停止是否落盘"**（B 级矛盾，最高优先）—— 它决定备份策略是"保险"还是"必需"。
2. **跑完 V0-6（GameMode）** —— 纯枚举、最快、独立，能直接消掉计划里唯一的推断标注。方法见 §10 与 `docs/验证报告.md`。
3. **跑完 V0-5（配置优先级）** —— 影响"改配置不生效"这类最烦人的用户投诉。
4. **封闭指令通道结论** —— 服务端不自建 FIFO 已确认；需判定"预创建 FIFO"与 `--enhanced` 两条路是否可行。**若均不可行，则 §5.3 的兜底方案 C 应从"长期正解"升级为"近期必需"**，因为玩家管理/广播/踢人与彩色绑定在同一前提上。
5. **搞定 PTY**：把执行用户加入 `docker` 组（一次性解锁最大验证面），或在真实目标环境验证。判定实验：非沙箱会话执行 `python3 -c 'import pty; pty.openpty()'`。

### E.8 未完成的验证与遗留物

- `docs/验证报告.md`（488 行）：**结构完整、7 个任务章节齐全，但 8 处 `结论：` 字段为空**——那是脚手架，不是结果。V0-1/V0-2 的实测数据在 `/tmp/v0/`（`v01_fifo.log`、`v02_direct.log`）与探针 `scripts/v0/`。
- `internal/world/config_parity_test.go:143-147` 的注释仍声称 `config.PathSegment` 缺 NUL 防护——**该注释已过时**（测试通过，防护已加）。属他人文件，未改动。
- `files` 包的 `Mkdir`/`Rename`/`Delete` 存在 TOCTOU 残余风险（仅可移植路径分支；读取路径用 `openat2` + `RESOLVE_BENEATH|RESOLVE_NO_MAGICLINKS` 已安全）。
- 计划 §13.2 的"10 实例"是**默认假设**，无真实硬件规格支撑。

### E.9 纪律提醒

本项目**已被"把未验证的事说成已确认"坑过两次**（附录 B、C 记录了修正）。因此：
- 引用结论前先看 E.6 的分级，**B/C 级不得当 A 级用**；
- 新取得的证据请追加到 `docs/验证报告.md` 对应章节，并**填掉那些空的 `结论：` 字段**；
- 若发现本附录某条与实际不符，**直接改这里并注明日期**，不要另起一份文档——单一事实来源比文档数量重要。
