# SC-Panel 文档

本目录汇总 SC-Panel 的架构、接口、开发与部署文档。开发计划与测试门禁见仓库根
[`plants.md`](../plants.md)。

## 目录

| 文档 | 内容 |
|------|------|
| [architecture.md](./architecture.md) | 系统架构、模块划分、数据模型、关键设计决策 |
| [api.md](./api.md) | REST 接口与 WebSocket 事件协议完整参考 |
| [development.md](./development.md) | 本地开发、目录约定、编码规范、测试与调试 |
| [deployment.md](./deployment.md) | 环境变量、生产部署、Docker/systemd、Linux 二进制发布 |
| [test-reports/](./test-reports/) | M0–M7 各里程碑门禁证据（[索引](./test-reports/README.md)） |

## 快速导航

- 想**部署运行** → [deployment.md](./deployment.md)
- 想**调用接口/对接前端** → [api.md](./api.md)
- 想**参与开发** → [development.md](./development.md)
- 想**理解设计** → [architecture.md](./architecture.md)
- 想**了解开发历程与验收** → [`plants.md`](../plants.md)

## 项目一句话

SC-Panel 是参考 MCSManager 形态、为
[SurvivalcraftNet 服务端](https://gitee.com/SC-SPM/SurvivalcraftNet) 制作的全栈 Node.js
开服面板：Nuxt 4 SPA 前端 + NestJS 后端 + **node-pty 伪终端**，支持单节点多实例托管、
实时终端、状态探测、配置/存档/备份管理、服务端版本下载安装。
