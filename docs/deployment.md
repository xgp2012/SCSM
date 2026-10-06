# 部署与发布

## 1. 环境变量

配置来源 `.env`（参考 [`.env.example`](../.env.example)）。后端工作目录为 `apps/api`。

| 变量 | 说明 | 默认 |
|------|------|------|
| `PANEL_HOST` / `PANEL_PORT` | 后端监听地址/端口 | `127.0.0.1` / `3001` |
| `PANEL_USER` | 管理员用户名 | `admin` |
| `PANEL_PASSWORD` | 管理员密码（首启生成哈希；亦可直接给 `PANEL_PASSWORD_HASH`） | — |
| `JWT_SECRET` / `JWT_EXPIRES_IN` | JWT 密钥与有效期 | 开发默认 / `12h` |
| `PANEL_CORS_ORIGIN` | 允许的前端来源 | `http://localhost:3000` |
| `PANEL_DATA_DIR` | 面板数据目录（实例元数据 / 日志 / 审计 / 设置） | `./data` |
| `SERVER_TEMPLATE_DIR` | 服务端本体模板目录（相对 `apps/api`） | `../../../SurvivalcraftNet/服务端本体` |
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

> `BACKUP_*` / `GITEE_MIRROR` / `VERSION_CACHE_MS` / `VERSION_MAX_BYTES` 等可由面板
> `/settings` 运行期覆盖（持久化 `data/panel-settings.json`），无需重启即时生效。

## 2. 生产部署（源码方式）

### 2.1 构建

```bash
pnpm install --frozen-lockfile
pnpm build            # shared → api → web
```

- 后端产物：`apps/api/dist/`
- 前端产物：`apps/web/.output/`（SPA，`ssr:false`）

### 2.2 运行后端

```bash
# 在 apps/api 下准备 .env（含 PANEL_DATA_DIR / SERVER_TEMPLATE_DIR / JWT_SECRET 等）
node apps/api/dist/main.js
```

后端会在 `dist/public` 存在时托管前端静态资源（构建流程会把前端产物复制到
`apps/api/public`，随后端一起分发）。

### 2.3 运行前端

开发/独立部署：

```bash
node apps/web/.output/server/index.mjs   # nuxt preview 产物（端口默认 3000）
```

或使用任意静态服务器托管 `apps/web/.output/public/`（`ssr:false`，纯静态）。

前端通过 `NUXT_PUBLIC_API_BASE` 指定后端 origin；WS 终端优先直连后端。

## 3. 反向代理注意事项

- **Cookie 作用域**：`sc_panel_token` 按 host 作用域，跨端口仍会携带。
- **WebSocket 升级**：若经 Nginx 代理 `/ws`，必须开启升级头：

```nginx
location /ws/ {
    proxy_pass http://127.0.0.1:3001;
    proxy_http_version 1.1;
    proxy_set_header Upgrade $http_upgrade;
    proxy_set_header Connection "upgrade";
    proxy_set_header Host $host;
}
location /api/ {
    proxy_pass http://127.0.0.1:3001;
    proxy_set_header Host $host;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
}
```

- 或设置 `NUXT_PUBLIC_API_BASE` 让前端直连后端 origin（当前实现优先此路径）。
- `X-Forwarded-For` 用于审计记录真实 IP 与限流。

## 4. 生产安全清单

- [ ] `JWT_SECRET` 设为长随机串。
- [ ] 修改默认管理员密码（首启生成或经 `/settings` 改密）。
- [ ] `PANEL_HOST` 默认 `127.0.0.1`；如需外部访问，置于反代之后并启用 HTTPS。
- [ ] 生产环境 `NODE_ENV=production`（Cookie 自动加 `Secure`）。
- [ ] `PANEL_DATA_DIR` 与实例目录做好权限隔离与备份。

## 5. systemd 示例（Linux）

```ini
[Unit]
Description=SC-Panel API
After=network.target

[Service]
Type=simple
WorkingDirectory=/opt/sc-panel/apps/api
EnvironmentFile=/opt/sc-panel/apps/api/.env
ExecStart=/usr/bin/node /opt/sc-panel/apps/api/dist/main.js
Restart=on-failure
RestartSec=5

[Install]
WantedBy=multi-user.target
```

## 6. Docker 部署（简要）

```dockerfile
FROM node:24-bookworm AS build
WORKDIR /app
RUN corepack enable
COPY . .
RUN pnpm install --frozen-lockfile && pnpm build

FROM node:24-bookworm
WORKDIR /app
RUN apt-get update && apt-get install -y --no-install-recommends python3 make g++ \
    && rm -rf /var/lib/apt/lists/*
COPY --from=build /app/apps/api/dist ./apps/api/dist
COPY --from=build /app/apps/api/node_modules ./apps/api/node_modules
COPY --from=build /app/apps/web/.output ./apps/web/.output
WORKDIR /app/apps/api
ENV PANEL_HOST=0.0.0.0 PANEL_PORT=3001 NODE_ENV=production
EXPOSE 3001
CMD ["node", "dist/main.js"]
```

> 服务端运行需要 .NET SDK 10；若容器内要托管实例，请一并安装 dotnet 运行时。

## 7. Linux x64 二进制发布

仓库内置 GitHub Actions 工作流 [`.github/workflows/release.yml`](../.github/workflows/release.yml)：
推送 `v*` 标签时自动构建并发布 **Linux x64 单文件二进制**。

### 7.1 触发

```bash
git tag v0.1.4
git push origin v0.1.4
```

### 7.2 工作流步骤

1. Checkout → 安装 pnpm / Node 24（缓存在 `pnpm-lock.yaml`）。
2. `pnpm install --frozen-lockfile`。
3. 构建 shared → web → 复制前端产物到 `apps/api/public` → 构建 api。
4. `pnpm deploy --prod --legacy --config.node-linker=hoisted` 生成无符号链接的自包含目录。
5. `@yao-pkg/pkg@6.23.0` 打包 `node24-linux-x64`（GZip）→ `dist/scsm-linux-x64`。
6. `softprops/action-gh-release` 创建 Release 并上传二进制。

### 7.3 运行发布的二进制

```bash
chmod +x scsm-linux-x64
PANEL_DATA_DIR=./data SERVER_TEMPLATE_DIR=/path/to/服务端本体 ./scsm-linux-x64
```

### 7.4 pkg 打包要点（踩坑记录）

- 使用维护版 `@yao-pkg/pkg`（旧版 `pkg` 不支持 Node 24）。
- 必须用 `pnpm deploy --legacy --config.node-linker=hoisted`（pkg 无法处理 pnpm 的符号链接布局）。
- `main.ts` 中**显式传 `new ExpressAdapter()`**，否则 NestJS 动态 `require`
  `@nestjs/platform-express`，pkg 静态分析不到，运行时报
  `No driver (HTTP) has been selected`。
- **动态依赖必须显式静态 `require`**：仅靠 `pkg.scripts` 里的 `node_modules/**/*.js`
  通配**不足以**保证被收录（该通配只遍历 pkg 已解析到的模块）。`main.ts` 中已显式
  `require('class-validator')` / `require('class-transformer')`——`ValidationPipe`
  经 `loadPackage()` 动态加载它们，否则运行时启动即报
  `ERROR [PackageLoader] The "class-validator" package is missing`。
  新增此类「运行时动态加载」的依赖时，需同样补一条显式 `require`。
- `node-pty` 为原生模块，通过 `pkg.assets` 纳入；目标机首次运行会解压原生 `.node`。
- 发布流程含 **Smoke test**：启动打包产物并对 `GET /api/health` 探活，同时断言日志中
  不含 `class-validator` 缺失错误，防止该缺陷再次流入 Release。

> 若目标环境对单文件二进制有兼容问题，可退回「源码部署」或 tarball 分发。

## 8. 备份与还原面板数据

面板自身数据（`PANEL_DATA_DIR`）与各实例工作目录需分别备份：

- 面板数据：`data/instances/`（元数据）、`data/audit/`、`data/panel-settings.json`、
  `data/admin-password.json`。
- 实例数据：可直接使用面板内的「存档与备份」功能（世界 + 配置 tar.gz）。

## 9. 升级流程

```bash
git pull
pnpm install --frozen-lockfile
pnpm build
# 重启后端进程（systemd / pm2 / 容器）
```
