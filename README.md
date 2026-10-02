# SC-Panel · SurvivalcraftNet 开服面板

参考 MCSManager 形态，为 [**SurvivalcraftNet 服务端**](https://gitee.com/SC-SPM/SurvivalcraftNet)
制作的全栈 Node.js 开服面板。

- **前端**：Nuxt 4（SPA，`ssr:false`）+ fuxsto-design + Tailwind CSS v4 + Pinia + xterm.js
- **后端**：NestJS + **node-pty**（伪终端 PTY，硬性要求）+ WebSocket 实时控制台
- **能力**：单管理员 JWT 认证、单节点多实例托管、实时终端与状态推送、存档/配置/备份管理、
  Gitee 版本下载与安装

> 📖 **完整文档见 [`docs/`](./docs/README.md)**；开发计划与门禁见 [`plants.md`](./plants.md)。

---

## 文档导航

| 文档 | 内容 |
|------|------|
| [docs/architecture.md](./docs/architecture.md) | 系统架构、模块划分、数据模型、关键决策 |
| [docs/api.md](./docs/api.md) | REST 接口与 WebSocket 事件协议 |
| [docs/development.md](./docs/development.md) | 本地开发、目录约定、测试与调试 |
| [docs/deployment.md](./docs/deployment.md) | 环境变量、生产部署、Linux 二进制发布 |
| [docs/test-reports/](./docs/test-reports/README.md) | M0–M7 门禁证据 |
| [plants.md](./plants.md) | 开发计划、里程碑与测试门禁 |

---

## 里程碑状态

| 阶段 | 内容 | 状态 |
|------|------|------|
| M0 | 脚手架（workspace / 前后端跑通 / 测试接入） | ✅ 2026-09-30 |
| M1 | 认证 + 实例 CRUD + PTY 启停 | ✅ 2026-10-01 |
| M2 | WebSocket 终端 + 状态推送 | ✅ 2026-10-01 |
| M3 | UDP 探测 + 指标 | ✅ 2026-10-01 |
| M4 | 配置管理 | ✅ 2026-10-01 |
| M5 | 存档与备份 | ✅ 2026-10-01 |
| M6 | 版本下载与安装 | ✅ 2026-10-01 |
| M7 | 打磨与安全 | ✅ 2026-10-01 |

全部 8 个里程碑已完成，**134 单测通过 + Playwright E2E 2/2 通过**。

---

## 快速开始

### 前置

- Node.js **≥ 24**、pnpm **11**
- .NET SDK **10**（运行服务端 `dotnet Survivalcraft.dll`）
- 一份 SurvivalcraftNet 服务端本体（含 `Survivalcraft.dll` / `Content.scpak`）

### 安装与运行

```bash
pnpm install
cp .env.example .env      # Windows: copy .env.example .env
pnpm dev                  # 并行启动 api(:3001) 与 web(:3000)
```

打开 http://localhost:3000 ，使用 `.env` 中的管理员账号登录。

> `SERVER_TEMPLATE_DIR` 指向服务端本体目录（默认相对 `apps/api` 为
> `../../../SurvivalcraftNet/服务端本体`）。不便使用模板时，可在创建实例时改用「上传 zip」。

### 其它脚本

```bash
pnpm build       # 构建 shared → api → web
pnpm typecheck   # 类型检查（三包）
pnpm lint        # ESLint（三包）
pnpm test        # Vitest（api / web / shared）
pnpm test:e2e    # Playwright 真实全链路（需先 build）
```

更多运行、调试与门禁细节见 [docs/development.md](./docs/development.md)。

---

## 功能概览

- **认证**：单管理员 bcrypt 登录，JWT 经 HttpOnly + SameSite=Lax Cookie 下发；全局
  `JwtAuthGuard`；首启从 `.env` 读取或生成随机密码。
- **实例管理**：独立工作目录模式；`template` / `upload` / `import` / `version` 四种建实例来源；
  端口自动分配；生命周期 `start / stop / restart / kill`，崩溃识别 + 指数退避自动重启。
- **实时终端**：游戏主进程经 `node-pty` 运行，保留真实 TTY（ANSI、增强控制台、热键、`Ctrl+C`）；
  WS 端点 `/ws/console`；raw/line 双通道；5000 行 + 64KB 回滚缓冲与回放；多观察者。
- **状态探测与指标**：UDP ServerInfo 单播探测；系统指标（os/statfs）+ 进程指标；
  WS 推送 `instance.stats` / `player.list` / `system.stats`。
- **配置管理**：`Settings.xml` / `ServerSetting.json` / `Configs/{Level,Ban,Limit}Config.json` /
  `Plugins/Password.json` 6 分区表单读写；保留未知字段、原子写、密码脱敏、运行中写保护。
- **存档与备份**：列存档；tar.gz 流式备份（世界 + 配置）；恢复 + 预快照；定时与保留策略。
- **版本下载与安装**：Gitee releases 拉取（缓存/降级）；流式下载（断点续传/重试）；解压安装；
  手动上传兜底；覆盖安装保留配置。
- **打磨与安全**：限流、审计（JSONL 按天轮转）、面板设置、运行期改密、Playwright E2E。

功能细节与实测口径见 [docs/architecture.md](./docs/architecture.md)。

---

## 目录结构

```
scsm/
├─ apps/
│  ├─ web/        # Nuxt 4 SPA 前端
│  └─ api/        # NestJS 后端
├─ packages/
│  └─ shared/     # 前后端共享类型（ESM + CJS 双产物）
├─ docs/          # 架构 / 接口 / 开发 / 部署 / 测试报告
├─ THIRD_PARTY_LICENSES/
├─ plants.md      # 开发计划与测试门禁
└─ pnpm-workspace.yaml
```

---

## 环境变量

完整列表见 [docs/deployment.md §1](./docs/deployment.md#1-环境变量) 与
[`.env.example`](./.env.example)。常用：

| 变量 | 说明 | 默认 |
|------|------|------|
| `PANEL_HOST` / `PANEL_PORT` | 后端监听 | `127.0.0.1` / `3001` |
| `PANEL_USER` / `PANEL_PASSWORD` | 管理员账号 | `admin` / — |
| `JWT_SECRET` / `JWT_EXPIRES_IN` | JWT 密钥与有效期 | 开发默认 / `12h` |
| `PANEL_DATA_DIR` | 面板数据目录 | `./data` |
| `SERVER_TEMPLATE_DIR` | 服务端本体模板目录 | `../../../SurvivalcraftNet/服务端本体` |
| `PROBE_INTERVAL_MS` / `METRICS_INTERVAL_MS` | 探测/采样周期 | `5000` / `3000` |
| `BACKUP_INTERVAL_MS` / `BACKUP_KEEP` | 定时备份周期 / 保留数 | `0` / `10` |

---

## 测试与门禁

项目遵循 [`plants.md` §9.5](./plants.md) 的**测试门禁**：每个里程碑须通过可复现的端到端验证
并留存 `docs/test-reports/M<n>.md`。

```bash
pnpm test                                   # 全部单测（134 passed）
node apps/api/scripts/m2-gate.cjs           # M2 门禁（需先启动后端）
node apps/api/scripts/m3-gate.cjs           # M3 门禁
node apps/api/scripts/m4-gate.cjs           # M4 门禁
node apps/api/scripts/m5-gate.cjs           # M5 门禁
node apps/api/scripts/m6-gate.cjs           # M6 门禁（需可访问 Gitee）
node apps/api/scripts/m7-gate.cjs           # M7 门禁
pnpm test:e2e                               # Playwright 真实全链路
```

证据汇总见 [docs/test-reports/](./docs/test-reports/README.md)。

---

## 发布 Linux 二进制

推送 `v*` 标签触发 GitHub Actions（[`.github/workflows/release.yml`](./.github/workflows/release.yml)），
自动构建并发布 **Linux x64 单文件二进制**：

```bash
git tag v0.1.4
git push origin v0.1.4
```

打包细节与踩坑记录见 [docs/deployment.md §7](./docs/deployment.md#7-linux-x64-二进制发布)。

---

## 第三方许可

本项目允许参考/移植 [MCSManager](https://github.com/MCSManager/MCSManager)（MIT）的小颗粒工具
函数，凡移植处均在文件顶部**标注来源与贡献**。

> 截至 M7，**仍未移植任何 MCSM 代码**——终端解析、`FileTailer`、端口分配、优雅停止、审计、
> 限流、对话框等均为本项目独立实现。许可框架已就位，后续如发生移植将同步补标注。

许可文本见 [`THIRD_PARTY_LICENSES/`](./THIRD_PARTY_LICENSES/)。
