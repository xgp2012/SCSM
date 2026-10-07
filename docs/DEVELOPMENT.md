# scnetm 开发文档（贡献者指南）

> 面向**改这个代码库的人**。构建与运行见 [`README.md`](./README.md)，
> 接口契约见 [`API.md`](./API.md)，实现计划见
> [`../SCNETM-开服面板实现计划.md`](../SCNETM-开服面板实现计划.md)，
> 运行时验证结论见 [`验证报告.md`](./验证报告.md)。

---

## 1. 这个项目在做什么

一个 **Go 单体二进制**，用来管理 SurvivalcraftNet（生存战争2 联机版）**纯服务端**实例：
模板化建实例、PTY 下启停、把彩色控制台流到浏览器、改配置、备份存档。

三层结构：

```
浏览器 (Vue 3 SPA, 经 go:embed 内嵌)
        │  REST /api/v1 + WebSocket /ws
Go 面板 (gin + SQLite，单二进制)
        │  os/exec + PTY
游戏服务端 (dotnet Survivalcraft.dll，**进程外**，每实例一个)
```

**关键边界**：面板**从不加载**服务端的程序集。它不依赖 .NET，没有 .NET 也能正常启动、
建库、服务 UI——只有"启动实例"会失败并给出明确原因。不要为了"修好"一个缺失的运行时
而给 Go 侧加 .NET 依赖。

---

## 2. 包职责

依赖方向是**单向**的：`cmd → api → {supervisor, store, config, ...}`，底层包互不反向依赖。

| 包 | 职责 | 覆盖率 |
|---|---|---|
| `cmd/scnetm` | 入口：flag、启动序列、HTTP、优雅退出 | — |
| `internal/api` | REST + WS 处理器、DTO、认证/鉴权中间件 | 77.5% |
| `internal/supervisor` | 进程生命周期：PTY、状态机、日志管道、指令通道 | 90.2% |
| `internal/ansi` | 字节流分帧 + ANSI 解码（**被 supervisor 依赖**） | 92.7% |
| `internal/store` | SQLite：DDL、迁移、仓储 | 84.0% |
| `internal/config` | 面板 config.yaml + `ServerSetting.json`/`Settings.xml`/`Project.json` | 85.6% |
| `internal/auth` | JWT、bcrypt、RBAC | 85.7% |
| `internal/files` | 路径校验、上传、解压（**风险最高**） | 84.0% |
| `internal/world` | 存档扫描/导入导出/管理 | 79.3% |
| `internal/backup` | 三级备份 + 保留策略 | 83.1% |
| `internal/scheduler` | cron 任务 | 93.8% |
| `internal/notify` | Webhook 渠道（钉钉/企微/QQ/自定义） | 91.7% |
| `internal/runtime` | .NET 探测 + 实例模板供给 | 93.6% |
| `internal/webui` | `go:embed` 前端产物 | — |
| `internal/version` | 版本串（ldflags 注入） | — |

> 覆盖率用 `go test -cover ./internal/...` 复核。上面是 2026-10-07 的实测值。

### 2.1 `internal/ansi` 与 `internal/supervisor` 的契约

`supervisor` 通过**类型别名** + `OnLineFn func(ansi.Line)` 消费 `ansi`：

```go
type ansiLine = ansi.Line   // 别名，不是定义类型
```

这意味着 `ansi.Line` 的字段一旦改名，`supervisor` **编译失败**而不是行为悄悄变化。
`internal/supervisor/ANSI_CONTRACT.md` 记录了 supervisor 依赖的 7 条行为假设
（分帧、`\r` 处理、`Raw` 字节保真、截断上界、非法 UTF-8 容忍等）。**改 `ansi` 前先读它。**

---

## 3. 必须知道的陷阱

这些每一条都踩过，写在代码注释里，这里汇总。

### 3.1 `go build` 会静默产出占位页二进制

`go:embed` **不能**引用包目录之外的路径，所以 `../../web/dist` 是编译错误。布局是：

```
web/dist/                ← Vite 产物
internal/webui/dist/     ← 它的副本，这才是被 embed 的
```

**裸 `go build` 退出码 0，却嵌入了占位页。** 必须用 `make build`（它先做拷贝），
或在脚本里手动 `cp -a web/dist/. internal/webui/dist/`。

CI 里用的是**行为校验**——启动二进制并请求 `/`，断言不是占位页。**不要**改成 grep
二进制里的标记：占位页文案被编译进**每个**构建，grep 区分不了两种产物。

### 3.2 `WorldPath` 决定目录，`WorldName` 只是显示名

```
WorldPath=app:/Worlds/MyWorldA, WorldName=ShowNameX
  → 实建目录 MyWorldA，日志显示 WorldName=ShowNameX
```

**切换存档必须改 `WorldPath`**，只改 `WorldName` 不会生效。存档列表要扫
`Worlds/*/` 读各自的 `Project.json`，**不要**用 `WorldName` 反推目录名。

### 3.3 端口只在 `Settings.xml` 里，而且是 UDP

`ServerPort` 只在 `Settings.xml`（默认 28887，LiteNetLib/UDP）。它**不在**
`ServerSetting.json` 里。有回归测试盯着这一点——若 `ServerPort` 重新出现在
`ServerSetting.json` 夹具或 config 的 `extra` 映射中，测试会失败。

端口占用检测要按 **UDP** 判断，不是 TCP。

### 3.4 `/stop` 是空实现

`Server/EssentialCmd/CmdStop.cs` 的 `ProcessCmd()` 是空的。实测：`/stop` 与 Ctrl+C
在 30s 预算内**都无效**，只有 `SIGTERM` 能终止（rc=143）。所以停止梯子不能依赖它。

### 3.5 停止时存档是否落盘 —— **尚无定论**

两次观测相互矛盾（一次显示 `Project.json` 晚于 `.bak`，一次显示两者都没推进）。
**不要**据此写成"安全"或"会丢档"。「启动前强制备份」策略不依赖这个结论，应保留。

### 3.6 `internal/api` 的 `-race` 测试很慢

```
go test -race ./internal/api/    # 需要 ≥500s（实测 475s 通过）
```

默认 600s 超时在 `-race` 下会**误报失败**——那不是死锁也不是竞态，普通模式 30s 就过。
这一点被误判过一次。

### 3.7 `LogSubscription.History(n)` 故意返回 `nil`

`supervisor.Subscription` 把回放积压直接灌进 `Lines()` 通道，没有独立的 replay 访问器。
若在 `cmd/scnetm/api.go` 里消费该通道去填缓冲，会与 WebSocket 读取方竞争并**吞掉实时行**。
所以读路径保持单一所有者：历史与实时都由 `Lines()` 按序给出。**别"修"它。**

---

## 4. 开发流程

### 4.1 本机构建环境

```bash
cd /home/xgp2012/SCSM
export GOFLAGS=-mod=mod GOSUMDB=off GOPRIVATE='*' GOTOOLCHAIN=local \
       GOMODCACHE=$PWD/.gomodcache GOCACHE=$PWD/.gocache
```

`GOTOOLCHAIN=local` 阻止 Go 去下载别的工具链（沙箱无网）。`.goenv.sh` 已不存在，
需显式设置。**首次编译很慢**（数分钟，模块缓存为空），给足超时，别当死锁。

### 4.2 提交前必做

```bash
gofmt -l ./cmd ./internal     # 必须无输出
go vet ./...
go test ./...
make build                    # 不要用裸 go build
```

### 4.3 前端

```bash
cd web
export XDG_DATA_HOME=$PWD/.xdg     # 沙箱内必需：全局 pnpm store 在沙箱外不可写
pnpm install --frozen-lockfile
pnpm type-check                    # vue-tsc + tsc
pnpm build                         # = type-check && vite build
pnpm dev                           # 开发服务器，代理到面板
```

`pnpm dev` 的代理目标取自 `SCNETM_API` 环境变量，默认 `http://127.0.0.1:7000`
（与面板默认 `listen` 一致）。面板跑在别处时：

```bash
SCNETM_API=http://127.0.0.1:9000 pnpm dev
```

> 这里原先硬编码 `8080`。那是面板的**旧**默认值，且在参考主机上被一个无关应用占用
> （它还返回 HTML）——代理指错时会**看起来像在工作**，而每个 API 调用拿到的都是网页。

### 4.4 前端对 UI 库的隔离

**没有页面直接 import `fuxsto-design/*`**，全部经 `web/src/components/ui/*` 薄适配层。
这样替换库或打补丁只需改一处。细节见 [`../web/FUXSTO-DESIGN-VERIFIED.md`](../web/FUXSTO-DESIGN-VERIFIED.md)。

两个实测踩到的坑：
- `select` 用 `options` prop（`{label,value}[]`），**不是** children。
  照 shadcn 风格写 `<Select><Option/></Select>` 无法编译。
- 库**没有**终端组件（用 `xterm.js`）也**没有**图表组件（用 `uplot`）。

---

## 5. 数据库

SQLite（`modernc.org/sqlite`，纯 Go，所以 `CGO_ENABLED=0` 可用）。DSN 设
`busy_timeout(5000)`、`journal_mode(WAL)`、`foreign_keys(1)`，**连接池固定为 1**——
SQLite 只允许一个写者，池子更宽会把并发写变成 `SQLITE_BUSY` 而不是排队。

### 5.1 迁移

迁移在 `internal/store/migrate.go` 的有序切片里，记入 `schema_migrations`，每条与它的
账本插入**在同一事务**内。

**只能追加，绝不能编辑或重编号已发布的迁移。**

### 5.2 管理员账户

空 `users` 表会种入 `id=1, username=admin`，`password_hash = store.FirstRunPasswordMarker`
——一个**故意无效的 bcrypt 值**，登录永远匹配不上，API 必须检测它
（`store.IsFirstRunHash`）并在服务任何东西之前强制设密。

`configs/config.example.yaml` 里 `default_admin_password: "adfmin"` **默认启用**，
会跳过这个流程。

> **这是一个已知的、刻意的削弱。** 该值提交在公开仓库里，而面板默认监听
> `0.0.0.0:7000`。为容纳它，`auth.MinPasswordLength` 从 **8 降到 6**，这适用于
> **所有**密码（含面板内改密），不只是这一个。若把初始密码改长，请把
> `auth.MinPasswordLength` 与 `config.MinAdminPasswordLength` 一起调回 8——
> `TestDefaultAdminPasswordLengthsMatchAuthPolicy` 会在只改一个时失败。

---

## 6. 写代码的约定

### 6.1 语言（zh-CN）

**没有 i18n 框架**，字符串用服务时所用的语言内联书写。这是单语言产品的刻意选择，
不要在没有第二个 locale 实际需求时引入 i18n 层。

| 必须是中文 | 必须保持英文 |
|---|---|
| API `message` 字段 | `code` 值（`not_implemented`、`conflict`…） |
| 运维指引（`InstallHint`） | `details.reason` 槽位（`path_traversal`…） |
| 玩家可见广播 | 事件名（`term.redirected`…） |
| 占位页 | JSON 字段名、YAML 键、CLI flag、URL 路径 |
| 运维日志 | `internal/files` 的 `Reason*` 常量 |
| 前端 `web/src/**` | 字体名（`Cascadia Mono` 等，要匹配真实已装字体） |

**加字符串时的规则**：
1. 用户可见文案直接写中文，不要加翻译键。
2. **绝不**因为文案改动而去改 `code`、`reason` 或事件名。
3. 保留 `%s`/`%q`/`%d` 动词——多处消息插值了实例名、端口与状态。
4. 测试若断言文案，**断言意图**（`strings.Contains(msg, "停止")`），不要断言整句。
5. 提交前跑 `gofmt -l ./cmd ./internal` 与 `go test ./...`。

### 6.2 安全

- 面板默认 `0.0.0.0:7000` 且**自身无 TLS**。公网暴露前必须在前面加反代终止 TLS。
- **文件处理是全项目风险最高的区域**：每个来自 HTTP/WS 的路径都要过
  `internal/files` 的校验，**永远不要拼接路径**。
- **绝不构造 shell 命令字符串**。只用带参数向量的 `exec.Command`；往控制台注入指令
  等于提权。
- 密钥（`default_admin_password`）不得出现在日志、JSON 或 `String()` 里。
  `Panel.Redacted()` 是唯一的脱敏入口。

---

## 7. 测试策略

单测集中在：`supervisor`（状态机、日志规则、信号）、`ansi`（被截断的 ANSI 序列边界）、
`config`（读写、保留未知字段）、`files`（路径穿越/zip-slip 用例集）、`world`（BOM、扫描）。

CI 侧建议（计划 §12，**尚未全部落地**——目前没有 PTY 集成测试，`testdata/` 也只在
`internal/runtime` 与 `internal/config` 下存在）：
- **PTY 集成测试**：CI 里真实 `openpty` 拉起服务端，断言日志含 `\x1b[`——这是 D1 的
  自动化守护。
- **契约测试**：日志解析规则对真实日志样本做快照，服务端升级改文案时立即报警。
- 测试数据把真实日志样本（原始含色 + 纯文本两份）纳入 `testdata/`。

---

## 8. 引用结论的纪律

这个项目**被"把未验证的说成已确认"坑过两次**（计划附录 B、C 记录了修正）。因此：

- 引用任何结论前先看 [`验证报告.md`](./验证报告.md) 的**可信度分级**：
  **A=实测**（完整链路跑通）· **B=实测但有保留** · **C=仅推断**（读码/样本）· **D=未知**。
  **B/C 不得当 A 引用。**
- 新证据追加到 `验证报告.md` 对应章节，并**填掉空的 `结论：` 字段**。
- 发现文档与实际不符时，**直接改那处并注明日期**，不要另起文档——单一事实来源
  比文档数量重要。
- 未验证的一律写 `未验证` 并说明原因，**不要**从相邻结论外推。

当前**唯一**的硬阻塞是 **PTY 不可分配**（`open("/dev/ptmx", O_RDWR)` → `EACCES 13`，
设备类级）。它同时挡住彩色输出与指令通道。注意区分"没测"与"不能测"：
V0-5/V0-6/写盘时机这些**不需要 PTY**，属"能测而未测"。
