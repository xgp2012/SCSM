# 开发指南

## 1. 环境要求

| 工具 | 版本 | 说明 |
|------|------|------|
| Node.js | ≥ 24 | 后端与构建 |
| pnpm | 11 | `packageManager` 已声明 `pnpm@11.24.0` |
| .NET SDK | 10 | 运行服务端 `dotnet Survivalcraft.dll` |
| SurvivalcraftNet 服务端本体 | — | 含 `Survivalcraft.dll` / `Content.scpak` / `Settings.xml` / `ServerSetting.json` |

## 2. 首次启动

```bash
pnpm install
cp .env.example .env      # Windows: copy .env.example .env
pnpm dev                  # 并行启动 api(:3001) 与 web(:3000)
```

打开 http://localhost:3000 ，使用 `.env` 中的管理员账号登录。

`SERVER_TEMPLATE_DIR` 指向服务端本体目录（相对后端进程工作目录 `apps/api`，
默认 `../../../SurvivalcraftNet/服务端本体`）。若不便使用模板，可在创建实例时改用「上传 zip」。

## 3. 常用脚本

```bash
pnpm dev          # 并行启动（api + web + shared watch）
pnpm dev:api      # 仅后端
pnpm dev:web      # 仅前端
pnpm build        # 构建 shared → api → web
pnpm typecheck    # 类型检查（三包）
pnpm lint         # ESLint（三包）
pnpm test         # Vitest（api / web / shared）
pnpm test:e2e     # Playwright 真实全链路（需先 build）
pnpm clean        # 清理各包 node_modules
```

## 4. 目录约定

```
apps/web/app/                 # 前端：pages / components / composables / stores / middleware
apps/api/src/<module>/        # 后端：每模块 controller + service + module（+ dto/gateway）
packages/shared/src/<domain>.ts# 共享类型：按域拆分，index.ts 汇总导出
apps/api/test/                # 后端单元 + 集成测试
apps/web/test/                # 前端组件/组合式函数测试
apps/web/e2e/                 # Playwright E2E
apps/api/scripts/mN-gate.cjs  # 真实门禁脚本
docs/                         # 本文档集
```

## 5. 编码规范

- TypeScript strict；后端 `emitDecoratorMetadata` + `experimentalDecorators`（NestJS DI）。
- 后端模块遵循 `controller / service / module` 分层，DTO 用 `class-validator`。
- 共享类型统一放 `packages/shared`，**前后端共用**，避免接口漂移。
- **不添加无关注释**；注释聚焦「为什么」与实测结论。
- 文件写入遵循「保留未知字段 + 原子写」原则（配置类操作）。
- 敏感字段回显必须脱敏（`SECRET_PLACEHOLDER = '********'`）。

> 注意：本项目当前**未移植任何 MCSManager 代码**。如后续移植，须在文件顶部标注：
> ```ts
> // Adapted from MCSManager (MIT) - https://github.com/MCSManager/MCSManager
> // Contributors: ... ; Modifications: ...
> ```
> 并同步更新 `THIRD_PARTY_LICENSES/` 与 README「第三方许可」段落。

## 6. 关键工程注意事项

### 6.1 Windows ConPTY
`node-pty.spawn` 不接受裸命令名，须先经 `resolveExecutable` 解析为绝对路径（如
`dotnet.EXE`），否则报 "File not found"。

### 6.2 NestJS 依赖注入与测试
Vitest 使用 **SWC 转换器**（`unplugin-swc`）产出 `emitDecoratorMetadata`，否则
NestJS DI 在测试中无法解析依赖。

### 6.3 共享包双产物
`@sc-panel/shared` 输出 **ESM + CJS 双产物**（CJS 供 NestJS 运行时 `require`）。
修改共享类型后需 `pnpm --filter @sc-panel/shared build`。

### 6.4 模块注册顺序
新增涉及 `/instances/:id/*` 子路由的模块时，须在 `app.module.ts` 中**先于
`InstanceModule`** 注册，否则会被 `InstanceController` 的 `@Post(':id/:action')` 吞噬。

### 6.5 无人值守开服
面板托管实例须 `ServerSetting.json.Autorun=true`，否则服务端停在 `Entered screen "Play"`，
不打印启动成功特征行，状态机无法进入 `running`。

### 6.6 pnpm 构建脚本审批（v11）
v11 中 `onlyBuiltDependencies` 已移除，改用 `pnpm-workspace.yaml` 的 `allowBuilds` 映射：

```yaml
allowBuilds:
  "@swc/core": true
  "@tailwindcss/oxide": true
  better-sqlite3: true
  esbuild: true
  node-pty: true
  unrs-resolver: true
```

### 6.7 Nuxt 代理
开发期经 Nitro `routeRules.proxy` 将 `/api` 与 `/ws` 指向后端。注意 Nitro 不处理 WS 升级，
前端终端优先直连后端 origin（`NUXT_PUBLIC_API_BASE`，同源回退）。

### 6.8 fuxsto-design Dialog 缺陷
`fuxsto-design@1.0.4` 的 `Dialog` 默认插槽/正文恒不渲染，项目改用自研 `AppDialog`/`dialog.ts`
（API 对齐）。新组件弹窗请用 `AppDialog`。

## 7. 测试与门禁

项目遵循 [`plants.md` §9.5](../plants.md) 的**测试门禁**：每个里程碑必须通过可复现的端到端
验证并留存 `docs/test-reports/M<n>.md`。

### 7.1 分层

| 层 | 手段 | 范围 |
|----|------|------|
| 单元测试 | Vitest | 纯函数（解析、端口分配、配置读写、探测包编解码） |
| 集成测试 | Supertest + Nest `Test` | REST 接口、鉴权、错误路径 |
| E2E | Playwright + 真实服务端进程 | 关键用户旅程 |
| 真实冒烟 | `mN-gate.cjs` 脚本 | 真实启服、PTY、命令回执、探测、文件落盘 |

### 7.2 运行

```bash
pnpm test                                   # 全部单测
pnpm build && node apps/api/dist/main.js    # 先启动后端（.env 就绪）

node apps/api/scripts/m2-gate.cjs           # WS 终端门禁
node apps/api/scripts/m3-gate.cjs           # UDP 探测 + 指标
node apps/api/scripts/m4-gate.cjs           # 配置读写 + 重启核实
node apps/api/scripts/m5-gate.cjs           # 备份 + 恢复 + 真实启服
node apps/api/scripts/m6-gate.cjs           # Gitee 下载 + 解压安装 + 真实启服
node apps/api/scripts/m7-gate.cjs           # 限流 / 审计 / 设置
pnpm test:e2e                               # Playwright 真实全链路
```

## 8. 调试

- **日志**：pino 结构化日志；`LOG_LEVEL` / `LOG_PRETTY` 控制。实例日志同时经 PTY stdout 与
  `tail Bugs/Game.log` 双通道采集。
- **状态机**：`InstanceSupervisor` 输出 `status → starting/running/stopping/stopped` 及崩溃/重启日志。
- **前端 WS**：浏览器 DevTools Network → WS 帧；`useConsoleSocket` 统一分发事件。
- **探测**：`GET /api/instances/:id/probe` 即时验证；或用 `m3-gate.cjs` 独立复现协议。

## 9. 新增一个后端模块的步骤

1. 在 `packages/shared/src/<domain>.ts` 定义类型，并在 `index.ts` 导出。
2. `apps/api/src/<module>/` 建 `*.service.ts` / `*.controller.ts` / `*.module.ts`。
3. 在 `app.module.ts` 注册（注意与 `InstanceModule` 的顺序）。
4. 如涉及敏感操作，接入 `AuditService.log()` 与 `@Throttle`。
5. 补 `apps/api/test/<module>.test.ts`（单元 + 集成）。
6. 如属里程碑范围，补 `apps/api/scripts/mN-gate.cjs` 与 `docs/test-reports/M<n>.md`。
7. 运行 `pnpm lint && pnpm typecheck && pnpm test`。
