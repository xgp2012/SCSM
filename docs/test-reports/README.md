# 测试门禁报告（M0–M7）

本目录保存 SC-Panel 各里程碑的**真实端到端门禁证据**，依据
[`plants.md` §9.5](../../plants.md) 的测试门禁要求产出。每份报告列出执行命令、
预期/实际结果与关键日志片段。

| 里程碑 | 内容 | 状态 | 报告 |
|--------|------|------|------|
| M0 | 脚手架（workspace / 前后端跑通 / 测试接入） | ✅ 2026-09-30 | [M0.md](./M0.md) |
| M1 | 认证 + 实例 CRUD + PTY 启停（真实启服/双实例/崩溃重启） | ✅ 2026-10-01 | [M1.md](./M1.md) |
| M2 | WebSocket 终端 + 状态推送（真实 PTY + `/help` 回执） | ✅ 2026-10-01 | [M2.md](./M2.md) |
| M3 | UDP 探测 + 指标（单播 ServerInfo + 进程/系统采样） | ✅ 2026-10-01 | [M3.md](./M3.md) |
| M4 | 配置管理（分区读写/未知字段保留/脱敏/写保护） | ✅ 2026-10-01 | [M4.md](./M4.md) |
| M5 | 存档与备份（tar.gz 备份/恢复+预快照/定时） | ✅ 2026-10-01 | [M5.md](./M5.md) |
| M6 | 版本下载与安装（Gitee 下载/解压安装/上传兜底） | ✅ 2026-10-01 | [M6.md](./M6.md) |
| M7 | 打磨与安全（限流/审计/设置/E2E/断点续传） | ✅ 2026-10-01 | [M7.md](./M7.md) |

## 汇总

| 阶段 | 单测/集成 | 真实门禁 |
|------|-----------|----------|
| M1 | 38 passed | 真实启服 / 双实例并行 / 崩溃自动重启 |
| M2 | 52 passed | 15/15 PASS（真实 PTY + `/help` 回执） |
| M3 | 62 passed | 12/12 PASS（真实 UDP 探测 + 指标） |
| M4 | 80 passed | 14/14 PASS（配置落盘 + 写保护 + 非法输入拒绝） |
| M5 | 94 passed | 10/10 PASS（备份/恢复/启服/定时） |
| M6 | 124 passed | 13/13 PASS（下载/安装/启服/上传/覆盖保留） |
| M7 | 134 passed | 12/12 PASS（限流/审计/设置）+ Playwright E2E 2/2 PASS |

## 门禁脚本

| 脚本 | 覆盖 |
|------|------|
| `apps/api/scripts/m2-gate.cjs` | WS 终端：真实启服、raw/line、`/help` 回执、多观察者、resize、历史回放 |
| `apps/api/scripts/m3-gate.cjs` | 独立 UDP 探测、REST/WS 指标推送 |
| `apps/api/scripts/m4-gate.cjs` | 各配置分区读写、重启核实、未知字段保留、非法输入拒绝 |
| `apps/api/scripts/m5-gate.cjs` | 列存档、备份落盘、篡改→恢复→还原、恢复后启服、运行中写保护 |
| `apps/api/scripts/m6-gate.cjs` | Gitee releases、真实联网下载、解压安装、真实启服、上传兜底 |
| `apps/api/scripts/m7-gate.cjs` | 限流 429、审计查询、面板设置、改密 |
| `apps/web/e2e/m7-journey.spec.ts` | Playwright 真实全链路（登录→建实例→启服→终端→备份→审计） |

运行方式见 [`docs/development.md` §7](../development.md#7-测试与门禁)。
