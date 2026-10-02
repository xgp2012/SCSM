# API 参考

后端默认监听 `http://127.0.0.1:3001`，所有 REST 路由前缀 `/api`。

## 1. 通用约定

### 1.1 响应包络

成功：

```json
{ "ok": true, "data": { } }
```

失败（由全局 `AllExceptionsFilter` 统一输出）：

```json
{ "ok": false, "statusCode": 400, "message": "可读错误信息", "code": "可选稳定码" }
```

### 1.2 认证

- 登录成功后服务端下发 **HttpOnly Cookie** `sc_panel_token`（`SameSite=Lax`，生产为 `Secure`，12h）。
- 除标注 `@Public` 的接口外，全部 REST 与 WS 握手均需该 Cookie。
- WS 亦兼容 `Authorization: Bearer <jwt>`。

### 1.3 限流

| 范围 | 限制 |
|------|------|
| 全局 | 每 IP 每分 300 次 |
| `POST /api/auth/login` | 每 IP 每分 5 次 |
| `POST /api/versions/download`、`POST /api/versions/upload` | 每 IP 每分 10 次 |
| `POST /api/instances/:id/:action` | 每 IP 每分 30 次 |
| WS `console.command`/`console.write` | 每连接 10s 内 40 条 |

超限返回 `429`（REST）或 `error` 帧 `code: rate_limited`（WS）。

---

## 2. 认证 `auth`

### `POST /api/auth/login` （Public，限流 5/分）

请求：

```json
{ "username": "admin", "password": "******" }
```

响应 `201`：

```json
{ "ok": true, "data": { "user": { "username": "admin", "role": "admin" } } }
```

失败 `401`。同时写审计 `auth.login` / `auth.login.failed`。

### `POST /api/auth/logout`

清除 Cookie，返回 `{ "ok": true, "data": { "user": null } }`。

### `GET /api/auth/me`

返回当前登录用户 `{ "ok": true, "data": { "user": { "username", "role" } } }`；未登录 `401`。

---

## 3. 健康检查

### `GET /api/health` （Public）

```json
{ "status": "ok", "name": "sc-panel-api", "version": "0.0.0", "uptimeSec": 17, "timestamp": "..." }
```

---

## 4. 实例管理 `instances`

### `GET /api/instances`

返回概览（实例列表 + 运行时聚合）。

```json
{
  "ok": true,
  "data": {
    "instances": [ Instance, ... ],
    "system": { /* SystemStats */ }
  }
}
```

### `GET /api/instances/template`

返回模板目录探测信息 `TemplateInfo`（是否存在、是否有二进制、版本）。

### `GET /api/instances/:id`

返回单个 `Instance`（含 `runtime` 聚合：`online/maxOnline/gameMode/cpu/memMB/probeOnline/lastProbeAt/timeOfDay`）。

### `POST /api/instances`

创建实例。请求体 `CreateInstanceDto`：

| 字段 | 类型 | 说明 |
|------|------|------|
| `name` | string | 必填，显示名 |
| `source` | `template`\|`upload`\|`import`\|`version` | 建实例来源，默认 `template` |
| `dir` | string | `import` 时的既有目录 |
| `serverPort` | number(1–65535) | 缺省自动分配空闲端口 |
| `broadcastPort` | number | 保留字段 |
| `version` | string | 版本标识 |
| `launchArgs` | string[] | 启动参数 |
| `worldName` | string | 世界名 |
| `maxPlayers` | number(1–1024) | 世界人数上限 |
| `autoRestart` / `autoStart` / `autoRun` | boolean | `autoRun` 默认 `true`（无人值守开服） |
| `templateDir` | string | 覆盖模板目录 |

响应 `201`：`{ "ok": true, "data": Instance }`。写审计 `instance.create`。

### `PATCH /api/instances/:id`

更新实例（`UpdateInstanceDto`）。除元数据外，支持世界参数直写 `ServerSetting.json`：
`worldPath/worldName/worldSeed/maxPlayers/gameMode/pvpEnabled/seasonChanging/autoGenerateWorld/worldPassword`。

`gameMode`：`0=Creative 1=Cruel 2=Survival 3=Adventure`。写审计 `instance.update`。

### `DELETE /api/instances/:id?deleteFiles=true`

删除实例。`deleteFiles=true` 时同时删除工作目录。写审计 `instance.remove`。

### `POST /api/instances/:id/:action`

生命周期操作，`action ∈ {start, stop, restart, kill}`（限流 30/分）。
未知 action 返回 `400`。写审计 `instance.<action>`。

### `POST /api/instances/:id/upload`

`multipart/form-data`，字段 `file`（zip）。解压到实例目录并校验二进制。写审计无（随创建）。

---

## 5. 配置管理 `config`

### `GET /api/instances/:id/config`

返回 `ConfigBundle`：

```json
{
  "instanceId": "...",
  "running": true,
  "sections": {
    "settings": { "ServerPort": "28887", ... },
    "serverSetting": { "WorldName": "...", "WorldPassword": "********", ... },
    "level": { "Alice": 100 },
    "ban": { "BanUserList": [], "BanUserIpList": [], "BanIpList": [] },
    "limit": { ... } | null,
    "password": { ... } | null
  },
  "meta": [ { "key": "settings", "label": "...", "file": "Settings.xml", "effective": true, "writableWhileRunning": true } ]
}
```

敏感字段以 `********`（`SECRET_PLACEHOLDER`）脱敏回显。

### `PUT /api/instances/:id/config/:section`

`section ∈ {settings, serverSetting, level, ban, limit, password}`。请求体为该分区对象。
不支持的 section → `400`。写入保留未知字段/条目（原子写）。写审计 `instance.config.update`。

运行中限制（返回 `409`）：

| 分区 | 运行中可写 |
|------|-----------|
| `settings` | 是（可热改 `ServerPort` 等） |
| `serverSetting` / `level` / `ban` / `limit` / `password` | 否 |

提交 `WorldPassword`/`DefaultPassword`/`PlayerPassword` 为 `********` 表示保持原值。

---

## 6. 终端 `console`

### `GET /api/instances/:id/history`

终端回滚快照（首屏/WS 回退）：

```json
{
  "ok": true,
  "data": {
    "type": "console.history",
    "instanceId": "...",
    "lines": ["纯文本历史行", ...],
    "ansi": "\u001b[...ANSI 快照"
  }
}
```

缓冲容量：5000 行纯文本 + 64KB ANSI 快照。

---

## 7. 探测与指标 `probe` / `system`

### `GET /api/instances/:id/probe`

即时 UDP 探测单个实例。

```json
{
  "ok": true,
  "data": {
    "online": true,
    "version": "x26.06.19",
    "playerCount": 0,
    "maxCount": 20,
    "gameMode": "Survival",
    "needPassword": false,
    "timeOfDay": 0.35,
    "pingMs": 38
  }
}
```

### `GET /api/system/stats`

主机系统指标 `SystemStats`：

```json
{
  "cpu": 75.95, "memPercent": 42.21, "memUsedMB": 3407, "memTotalMB": 8073,
  "diskPercent": 37.52, "diskUsedMB": 0, "diskTotalMB": 0,
  "cpuCount": 4, "platform": "win32", "uptimeSec": 0, "loadAvg": [..], "ts": 0
}
```

---

## 8. 存档与备份 `backup`

### `GET /api/instances/:id/worlds`

列出世界存档 `WorldInfo[]`：`{ name, path, size, fileCount, modifiedAt, isCurrent, hasProject }`。

### `GET /api/instances/:id/backups`

列出备份 `BackupMeta[]`：`{ id, instanceId, file, size, createdAt, type, world, includes, note }`。
`type ∈ {manual, auto, pre-restore, pre-install}`。

### `POST /api/instances/:id/backups`

创建备份。请求 `CreateBackupInput`：

```json
{ "world": "World", "includeConfig": true, "note": "可选" }
```

运行中返回 `409`。落盘 `backups/<uuid>.tar.gz` + `<uuid>.json`。写审计 `backup.create`。

### `POST /api/instances/:id/backups/:backupId/restore`

恢复备份。请求 `RestoreBackupInput`：

```json
{ "snapshot": true }
```

流程：停止实例 → 生成 `pre-restore` 快照 → 解压覆盖。写审计 `backup.restore`。

### `GET /api/instances/:id/backups/:backupId/download`

下载 tar.gz（`Content-Type: application/gzip`）。写审计 `backup.download`。

### `DELETE /api/instances/:id/backups/:backupId`

删除归档与元数据。写审计 `backup.remove`。

---

## 9. 版本下载与安装 `versions`

### `GET /api/versions/sources`

返回版本来源列表 `VersionSourceInfo[]`（默认 `gitee`）。

### `GET /api/versions/releases?source=&refresh=true`

拉取 releases 列表（缓存默认 5min；`refresh=true` 绕过缓存）。
拉取失败降级到本地缓存并带 `notice`；无缓存则 `fallback=true`。

### `POST /api/versions/releases/refresh`

清缓存并强制刷新。

### `POST /api/versions/download` （限流 10/分）

请求 `CreateDownloadInput`：

```json
{ "tag": "x26.06.19", "asset": "[服务端]SCNETx26.06.19z1.zip", "mode": "new", "name": "可选", "instanceId": "覆盖时必填" }
```

立即返回 `DownloadTask`（`taskId`）。写审计 `version.download`。

### `GET /api/versions/tasks` / `GET /api/versions/tasks/:id`

查询任务列表 / 单任务进度。任务阶段：
`queued → downloading → extracting → installing → done | failed`，
含 `receivedBytes/totalBytes/percent/speedBps/error`。

### `POST /api/versions/upload` （限流 10/分，multipart）

字段 `file`（zip/tar.gz）+ `mode/name/instanceId/preserve`。手动上传兜底。写审计 `version.upload`。

---

## 10. 审计 `audit`

### `GET /api/audit`

查询参数：`from`、`to`、`action`、`instanceId`、`outcome`(`ok`\|`failed`)、`q`、`limit`。
返回 `AuditQueryResult`。

### `GET /api/audit/days`

返回有审计记录的日期列表 `string[]`。

审计动作（`AuditAction`）覆盖：`auth.login(.failed)`、`auth.logout`、`auth.password.change`、
`instance.create/update/remove/start/stop/restart/kill`、`instance.config.update`、
`console.command`、`backup.create/restore/remove/download`、`version.download/install/upload`、
`settings.update`。

存储：`<PANEL_DATA_DIR>/audit/YYYY-MM-DD.jsonl`，每行一条记录（含 `ip`/时间戳）。

---

## 11. 面板设置 `settings`

### `GET /api/settings`

返回 `PanelSettings`：

```json
{
  "backup": { "intervalMs": 0, "keep": 10 },
  "download": { "mirror": "", "cacheMs": 300000, "maxBytes": 2147483648 },
  "ui": { "theme": "dark" }
}
```

### `PUT /api/settings`

更新设置（运行期热更新）。写审计 `settings.update`。

### `POST /api/settings/password`

修改管理员密码，请求 `{ "currentPassword": "...", "newPassword": "..." }`。
当前密码错误返回 `401`。写审计 `auth.password.change`。

---

## 12. WebSocket 协议

**端点**：`ws://<host>/ws/console?instanceId=<id>`

**握手**：读取 HttpOnly Cookie `sc_panel_token`（或 `Authorization: Bearer`）校验 JWT。
未登录以 `4401` 关闭；实例不存在以 `4404` 关闭。

### 12.1 服务端 → 客户端

| 事件 `type` | 载荷 | 说明 |
|-------------|------|------|
| `console.history` | `{ instanceId, lines[], ansi }` | 连接时回放 |
| `console.hello` | `{ instanceId, runtime }` | 握手确认 + 运行时快照 |
| `console.raw` | `{ instanceId, data }` | 原始 ANSI 字节流（xterm 渲染） |
| `console.line` | `{ instanceId, line, ts }` | 清洗后纯文本行 |
| `instance.status` | `{ instanceId, status, pid?, startedAt?, uptimeMs?, serverPort? }` | 状态推送 |
| `instance.stats` | `{ instanceId, cpu, memMB, ts }` | 进程指标周期推送 |
| `player.list` | `{ instanceId, online, maxOnline, gameMode, hasPassword, probeOnline, version?, timeOfDay?, pingMs? }` | 探测结果 |
| `system.stats` | `SystemStats & { type }` | 主机指标（广播全部连接） |
| `error` | `{ instanceId?, message, code? }` | 错误（如 `rate_limited`） |
| `pong` | `{}` | 心跳回应 |

### 12.2 客户端 → 服务端

| 事件 `type` | 载荷 | 说明 |
|-------------|------|------|
| `console.write` | `{ data }` | 原始输入（含 Ctrl+C `\x03`） |
| `console.command` | `{ command }` | 命令，自动补 `/` 前缀 + `\r` |
| `terminal.resize` | `{ cols, rows }` | 调整 PTY 尺寸 |
| `ping` | `{}` | 心跳 |

### 12.3 行为要点

- 同一实例允许多个 WS 观察并写入；命令经每连接队列 + 16ms 合帧，避免交叉错乱。
- 单连接异常隔离：一个连接出错不影响其他订阅者。
- 输出合帧间隔 `16ms`；单连接命令队列上限 `200`。

---

## 13. 前端调用示例

```ts
// 登录（携带 Cookie）
await $fetch('/api/auth/login', {
  method: 'POST',
  body: { username, password },
  credentials: 'include',
});

// 列出实例
const { data } = await $fetch<{ data: OverviewResponse }>('/api/instances', {
  credentials: 'include',
});

// 启动实例
await $fetch(`/api/instances/${id}/start`, { method: 'POST', credentials: 'include' });

// WebSocket 终端（直连后端 origin）
const ws = new WebSocket(`ws://127.0.0.1:3001/ws/console?instanceId=${id}`);
ws.onmessage = (e) => {
  const evt = JSON.parse(e.data);
  if (evt.type === 'console.raw') term.write(evt.data);
};
```

> 开发期也可经 Nuxt Nitro 代理（`/api`、`/ws`），但 WS 升级建议直连后端 origin
> （`NUXT_PUBLIC_API_BASE`）。
