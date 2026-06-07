# aexp — Agent Experiment Control Plane

> 面向人-Agent 协作的科研实验运行中间层。
> 用统一的 CLI / API / Web / MCP 接口管理异构计算资源上的实验运行、日志、状态、指标和产物。

## 为什么需要 aexp？

Agent 跑科研实验的现状：SSH 进容器，手动 `nohup`，tmux 里乱跑命令，日志要自己 tail，GPU 要自己查，跑完结果没人记录。Agent 换个会话就忘了上一次跑了什么。

aexp 解决这个问题：**给 Agent 和人一个结构化的实验操作系统，而不是裸 shell。**

```
Agent (MCP)  /  人 (CLI / Web UI)
            │
   ┌────────▼────────┐
   │   aexp server    │   ← 统一控制平面
   └────────┬─────────┘
            │ SSH / local
   ┌────────▼─────────┐
   │ 异构计算资源       │   ← 自己服务器、导师服务器、容器、云算力、Slurm
   │ run via tmux      │
   └──────────────────┘
```

## 核心概念

| 概念 | 说明 |
|---|---|
| **Resource** | 一台可用的计算资源（SSH 服务器、Docker 容器、Slurm 节点等） |
| **Run** | 一次实验执行，绑定到某个 resource，有完整的生命周期 |
| **Snapshot** | 某个 resource 在某时刻的 CPU/GPU/内存状态 |
| **Agent Event** | Agent 每一步操作的审计日志（谁、为什么、做了什么） |

完整概念定义见 [concepts.md](docs/concepts.md)。

## 快速开始

```bash
# 初始化
aexp init

# 注册计算资源
aexp resource add \
  --name mu-tslib \
  --type ssh \
  --host 192.168.1.100 \
  --user root \
  --root-dir /workspace \
  --tags "4090,timeseries"

# 提交实验
aexp run submit \
  --resource mu-tslib \
  --name "ECL-iTransformer" \
  --cwd /workspace/Time-Series-Library \
  -- python train.py --data ECL --model iTransformer

# 查看日志
aexp run logs r_Yn7pL2wE

# 启动 Web 仪表盘
aexp serve --port 8080
```

## 文档

| 文档 | 内容 |
|---|---|
| [USAGE.md](USAGE.md) | **使用指南 — 所有命令和参数说明** |
| [testing.md](docs/testing.md) | 编译二进制、临时 smoke 测试环境、如何查看测试 DB |
| [blueprint.md](docs/blueprint.md) | 架构总览、MVP 范围、技术栈、文件结构 |
| [concepts.md](docs/concepts.md) | Resource / Run / Snapshot / Artifact / Agent Event 定义 |
| [mod-store.md](docs/mod-store.md) | SQLite schema、migration、repository 接口 |
| [mod-resource.md](docs/mod-resource.md) | 异构资源注册、认证、健康检查 |
| [mod-executor.md](docs/mod-executor.md) | tmux 执行、wrapper script、生命周期状态机 |
| [mod-logger.md](docs/mod-logger.md) | stdout/stderr 捕获、日志 tail、日志游标 |
| [mod-monitor.md](docs/mod-monitor.md) | CPU/GPU/内存轮询、WebSocket 推送、健康度 |
| [mod-api.md](docs/mod-api.md) | REST 端点、WebSocket 协议 |
| [mod-web.md](docs/mod-web.md) | 仪表盘页面线框图 |
| [mod-cli.md](docs/mod-cli.md) | CLI 命令、JSON 输出、配置文件 |
| [mod-agent.md](docs/mod-agent.md) | MCP 工具定义、Agent 调用规范 |
| [mod-security.md](docs/mod-security.md) | SSH key 策略、命令沙箱、路径限制 |
| [deployment.md](docs/deployment.md) | 部署方式、systemd、数据备份 |

## 技术栈

- **Go 1.22+** — 单二进制、并发 SSH、WebSocket
- **SQLite** (modernc.org/sqlite) — 零部署，单文件
- **cobra** — CLI 框架
- **chi** — 轻量 HTTP router
- **gorilla/websocket** — 实时日志流
- **嵌入式 HTML** — 无构建步骤，编译进二进制

## MVP 范围

Phase 1（当前）：
- [x] 注册 SSH 资源
- [x] tmux 执行实验 + wrapper script 捕获 exit code
- [x] 实时日志 tail（CLI + WebSocket）
- [x] CPU/GPU/内存监控
- [x] Web 仪表盘
- [x] Agent 审计日志（agent_events）

不包括（Phase 2+）：MCP server、自动创建容器、MLflow 集成、调度器、多用户认证。
