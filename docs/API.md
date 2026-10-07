# scnetm API 参考（/api/v1）

> 本文是**接口契约**，写给调用方（前端开发者、脚本、将来的 CLI）。
> 架构与构建说明见 [`README.md`](./README.md)；实现计划见
> [`../SCNETM-开服面板实现计划.md`](../SCNETM-开服面板实现计划.md)。

所有路由挂在 **`/api/v1`** 前缀下。请求与响应体均为 JSON（文件上传/下载除外）。

---

## 1. 认证

### 1.1 获取 token

```http
POST /api/v1/auth/login
Content-Type: application/json

{"username": "admin", "password": "adfmin"}
```

成功返回 `200`：

```json
{"data": {
  "token": "<HS256 JWT>",
  "user": {"id": 1, "username": "admin", "role": "admin", ...}
}}
```

`RememberMe`（`remember_me`）只延长 access token 的有效期，**不会**绕过 jti 黑名单。

### 1.2 携带 token

```http
Authorization: Bearer <token>
```

**WebSocket 是例外**：浏览器的 `WebSocket` 构造函数无法设置请求头，因此控制台与事件
socket 额外接受 `?token=<jwt>`（`internal/api/ws_console.go` 有详细说明）。两条路径走
**完全相同的校验**（签名、有效期、jti 黑名单），唯一区别是审计日志记录 `ViaQueryToken`
以标记较弱的通道。

### 1.3 首次启动

空 `users` 表会种入 `id=1, username=admin`，其 `password_hash` 是
`store.FirstRunPasswordMarker`——一个**故意无效的 bcrypt 值**，登录永远失败。

```http
GET /api/v1/auth/setup-required      # 未认证
```

```json
{"data": {"setup_required": true, "username": "admin", "min_password_length": 6}}
```

此时用 `POST /api/v1/auth/setup` 设置密码。该接口**只能用一次**：启用后永久返回
`409 conflict`，因此不构成后门。

> 若 `config.yaml` 配置了 `default_admin_password`（示例配置里默认就是 `adfmin`），
> 这一步会被跳过，`setup_required` 直接是 `false`。见 `README.md` 的「Database」一节。

### 1.4 自助改密

```http
POST /api/v1/auth/password      # 注意：不是 /auth/change-password
{"current_password": "...", "new_password": "..."}
```

改密会**吊销该用户的全部会话**（响应里 `sessions_revoked: true`），需要重新登录。

---

## 2. 通用约定

### 2.1 成功响应

统一包在 `data` 里：

```json
{"data": { ... }}
```

### 2.2 错误响应

```json
{"error": {
  "code": "conflict",
  "message": "面板初始化已完成",
  "details": {"reason": "..."},
  "request_id": "..."
}}
```

| `code` | HTTP | 含义 |
|---|---|---|
| `bad_request` | 400 | 请求格式错误 |
| `validation_failed` | 422 | 字段校验失败，`details.issues[]` 逐条说明 |
| `unauthorized` | 401 | 缺少或无效 token |
| `invalid_credentials` | 401 | 用户名或密码错误 |
| `setup_required` | 403 | 尚未初始化，需先走 `/auth/setup` |
| `forbidden` | 403 | 权限不足 |
| `not_found` | 404 | 资源不存在 |
| `conflict` | 409 | 状态冲突（如对 Running 实例再 start） |
| `rate_limited` | 429 | 触发限流 |
| `payload_too_large` | 413 | 上传超限 |
| `operation_timeout` | 504 | 操作超时 |
| `not_implemented` | 501 | 本构建中该功能的后端是 `nop`，**不会**静默返回空结果 |
| `unavailable` | 503 | 依赖不可用 |
| `internal_error` | 500 | 未预期的内部错误 |

**`code` 是稳定契约，`message` 不是。** 客户端应基于 `code` 分支，不要匹配中文文案
（见 `README.md` 的「Localization」）。

### 2.3 权限

每个路由要求一个权限位。角色与权限的映射见 `internal/auth/rbac.go`。

| 权限 | 说明 |
|---|---|
| `instance:read` / `instance:write` | 查看 / 增删实例 |
| `instance:control` | 启停重启 |
| `instance:config` / `instance:config:read` | 读写实例配置 |
| `instance:file` / `instance:file:read` | 文件管理 |
| `instance:backup` / `instance:backup:read` | 备份与还原 |
| `instance:log:read` | 日志读取 |
| `job:manage` | 定时任务 |
| `user:manage` | 用户管理 |
| `audit:read` | 审计日志 |
| `system:read` | 宿主信息 |
| `panel:admin` | 面板级管理 |

---

## 3. 路由清单

### 3.1 认证与自身

| 方法 | 路径 | 权限 | 说明 |
|---|---|---|---|
| GET | `/healthz` | — | 存活探针（**不在 `/api/v1` 下**） |
| GET | `/auth/setup-required` | — | 是否仍需初始化 |
| POST | `/auth/setup` | — | 一次性初始化 |
| POST | `/auth/login` | — | 登录 |
| POST | `/auth/logout` | 已认证 | 登出（吊销当前 token） |
| GET | `/auth/me` | 已认证 | 当前用户 |
| POST | `/auth/password` | 已认证 | 修改自己的密码 |

### 3.2 系统

| 方法 | 路径 | 权限 |
|---|---|---|
| GET | `/system/info` | `system:read` |

### 3.3 实例

| 方法 | 路径 | 权限 | 说明 |
|---|---|---|---|
| GET | `/instances` | `instance:read` | 列表（含实时状态） |
| POST | `/instances` | `instance:write` | 创建 |
| GET | `/instances/:id` | `instance:read` | 详情 |
| PATCH | `/instances/:id` | `instance:write` | 局部更新 |
| DELETE | `/instances/:id` | `instance:write` | 删除 |
| POST | `/instances/:id/start` | `instance:control` | 启动 |
| POST | `/instances/:id/stop` | `instance:control` | 停止，body 可空 |
| POST | `/instances/:id/restart` | `instance:control` | 重启 |
| GET | `/instances/:id/stats` | `instance:read` | CPU / 内存 / 运行时长 |
| GET | `/instances/:id/players` | `instance:read` | 在线玩家（**依赖指令通道**，见 §6） |

停止请求体：

```json
{"force": false, "timeout_sec": 30}
```

`force: true` 跳过优雅路径直接杀进程树。停止梯子：优雅路径 → 超时 → `SIGTERM` →
`SIGKILL`，见计划 §5.4。

### 3.4 实例配置

| 方法 | 路径 | 权限 |
|---|---|---|
| GET | `/instances/:id/config` | `instance:config:read` |
| PUT | `/instances/:id/config` | `instance:config` |
| POST | `/instances/:id/config/validate` | `instance:config:read` |

`validate` **只校验不写入**，用于前端表单的预检。

> **端口不在 `ServerSetting.json` 里。** `ServerPort` 只存在于 `Settings.xml`
> （且是 **UDP**）。改端口不能只改 `ServerSetting.json`——见计划 §2.6 与 C.3。

### 3.5 存档（世界）

| 方法 | 路径 | 权限 |
|---|---|---|
| GET | `/instances/:id/worlds` | `instance:read` |
| POST | `/instances/:id/worlds/import` | `instance:config` |
| GET | `/instances/:id/worlds/:w/export` | `instance:read` |
| POST | `/instances/:id/worlds/:w/backup` | `instance:backup` |
| POST | `/instances/:id/worlds/:w/restore` | `instance:config` |
| POST | `/instances/:id/worlds/:w/activate` | `instance:config` |
| DELETE | `/instances/:id/worlds/:w` | `instance:write` |

> **切换存档必须改 `WorldPath`，不是 `WorldName`。** 目录由 `WorldPath` 决定，
> `WorldName` 只是显示名。只改后者不会切存档——这是计划 §2.7 用对照实验确认的结论。

### 3.6 文件

| 方法 | 路径 | 权限 |
|---|---|---|
| GET | `/instances/:id/files` | `instance:file:read` |
| GET | `/instances/:id/files/download` | `instance:file:read` |
| POST | `/instances/:id/files/upload` | `instance:file` |
| POST | `/instances/:id/files/mkdir` | `instance:file` |
| POST | `/instances/:id/files/rename` | `instance:file` |
| POST | `/instances/:id/files/delete` | `instance:file` |
| POST | `/instances/:id/files/unzip` | `instance:file` |

所有 `path` 参数都要经过 `internal/files` 的路径校验（`filepath.Clean` +
`EvalSymlinks` + 前缀检查）。**永远不要拼接路径**——这是全项目风险最高的区域。

### 3.7 日志

| 方法 | 路径 | 权限 |
|---|---|---|
| GET | `/instances/:id/logs` | `instance:log:read` |
| GET | `/instances/:id/logs/download` | `instance:log:read` |

支持 `tail` 与 `grep` 查询参数。

### 3.8 备份

| 方法 | 路径 | 权限 |
|---|---|---|
| GET | `/backups` | `instance:backup:read` |
| POST | `/instances/:id/backups` | `instance:backup` |
| POST | `/backups/:id/restore` | `instance:backup` |
| DELETE | `/backups/:id` | `instance:backup` |

### 3.9 任务 / 用户 / 审计

| 方法 | 路径 | 权限 |
|---|---|---|
| GET POST PUT DELETE | `/jobs[/:id]` | `job:manage` |
| GET POST PUT DELETE | `/users[/:id]` | `user:manage` |
| GET | `/audit` | `audit:read` |

---

## 4. WebSocket

两个端点各注册两次（`/api/v1/ws/...` 与 `/ws/...`），**行为完全一致**：

```
/ws/instances/:id/console     控制台：日志推送 + 指令下发 + 尺寸同步
/ws/events                    全局事件
```

### 4.1 消息格式

`WSMessage`（`internal/api/dto.go`）：

```json
{
  "type": "log | status | error | pong | hello",
  "instance_id": 1,
  "seq": 42,
  "ts": "2026-10-07T12:00:00Z",
  "text": "...",
  "stream": "stdout | stderr",
  "state": "running",
  "code": "not_implemented",
  "message": "..."
}
```

**客户端 → 服务端**：`{"type":"command|ping|resize", ...}`。
**裸文本帧**（非 JSON）按指令处理——终端 UI 自然想直接发一行。

### 4.2 指令只走 WebSocket

**没有 REST 指令端点，也不要加。** 指令通过控制台 socket 的 `command` 帧发送；
结果不单独解析，统一回到日志流里（计划 §5.3）。

---

## 5. 用 curl 跑一遍完整流程

```bash
B=http://127.0.0.1:7000/api/v1

# 1) 是否要初始化
curl -sS $B/auth/setup-required

# 2) 登录，取 token
TOKEN=$(curl -sS -X POST $B/auth/login \
  -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"adfmin"}' \
  | python3 -c 'import json,sys; print(json.load(sys.stdin)["data"]["token"])')

# 3) 建实例（server_jar / dotnet_path 是必需的，见计划 C.5）
curl -sS -X POST $B/instances -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"name":"survival-1","world_name":"我的世界","port":28887,
       "server_jar":"Survivalcraft.dll","dotnet_path":"dotnet"}'

# 4) 启动
curl -sS -X POST $B/instances/1/start -H "Authorization: Bearer $TOKEN"

# 5) 状态
curl -sS $B/instances/1 -H "Authorization: Bearer $TOKEN"
```

---

## 6. 本构建中不可用的功能

### 6.1 `501 not_implemented`

当某功能的**后端未接线**（`nop`）时，路由返回 `501 not_implemented`，并带
`details.reason`——**不会**返回空 `200`。这是刻意设计：客户端"看起来成功了"比直接报错
更难排查。

### 6.2 `200` + `available: false`（玩家列表）

`GET /instances/:id/players` **不走 501**，而是返回 `200`，用 `available` 与 `note`
说明为什么拿不到：

```json
{"data": {
  "instance_id": 1,
  "players": [],
  "count": 0,
  "available": false,
  "note": "the online player list requires the command channel (V0-1), which is not available in this build"
}}
```

**`available: false` 不等于"没有玩家在线"。** 调用方必须先看 `available`：

- 实例未运行 → `note` 说明"仅在运行时可用"
- 指令通道不可用 → `note` 指向 V0-1

把空列表当成"服务器没人"是这里最容易犯的错误。

### 6.3 受 PTY 影响的能力

管道模式下（无 PTY）实测的结果，见 [`验证报告.md`](./验证报告.md) 的结论分级表：

| 能力 | 状态 |
|---|---|
| 控制台彩色输出 | **需真实 PTY**；管道模式 ESC 字节数为 0（已实测） |
| 指令通道（`command` 帧） | 管道模式下**实测无响应** |
| 在线玩家列表 | 依赖指令通道 → 与上一条同阻塞 |
| 踢人 / 广播 | 同上 |

面板在无 PTY 时**自动降级为无色模式并在 UI 告警**，不会静默降级、也不会启动失败。
彩色与指令通道**共用同一前提**，所以这不是"配色好不好看"的问题，而是玩家管理这一
整块能力的前提。
