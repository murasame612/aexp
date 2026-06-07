# aexp — Agent Experiment Control Plane

## 这是什么？

Agent 和人共享的科研实验运行中间层。
Agent 不再直接 SSH 进机器乱跑命令，而是通过 `aexp` 结构化接口提交、监控、记录实验。

```
Agent (MCP/CLI) / 人 (CLI/Web)
            │
   ┌────────▼────────┐
   │   aexp server    │   ← 统一控制平面
   └────────┬─────────┘
            │ SSH
   ┌────────▼─────────┐
   │ 异构计算资源       │   ← 自己服务器、导师服务器、容器、云算力、Slurm
   │ run via tmux      │
   └──────────────────┘
```

## 为什么不用纯 SSH？

| 问题 | 纯 SSH | aexp |
|---|---|---|
| Agent 不知道哪个资源在跑什么 | 是 | 否 — 所有 run 注册在册 |
| 日志要手动 tail | 是 | 否 — 自动 tail，WebSocket 流 |
| 无法从外部看 GPU/内存 | 是 | 否 — 集中资源视图 |
| Run 历史丢失 | 是 | 否 — SQLite 持久化 |
| Agent 换会话就忘 | 是 | 否 — run_id 是唯一句柄 |
| 不可复现 | 是 | 否 — 命令 + 环境 + 目录全记录 |

## 架构

```
┌─────────────────────────────────────────────────────┐
│                    aexp server                       │
│                                                     │
│  ┌──────────┐  ┌──────────┐  ┌──────────────────┐  │
│  │ REST API │  │ WebSocket│  │  Static Web UI    │  │
│  │ /api/v1/*│  │ /ws/*    │  │  /                │  │
│  └────┬─────┘  └────┬─────┘  └──────────────────┘  │
│       │              │                               │
│  ┌────┴──────────────┴───────┐                       │
│  │       Run Controller      │                       │
│  │  submit / cancel / tail   │                       │
│  └────────────┬──────────────┘                       │
│               │                                      │
│  ┌────────────┴──────────────┐  ┌────────────────┐  │
│  │     SSH Executor          │  │  Resource       │  │
│  │  connect / exec / tmux    │  │  Monitor        │  │
│  └────────────┬──────────────┘  │  cpu/gpu/mem    │  │
│               │                 └────────────────┘  │
│  ┌────────────┴──────────────┐                      │
│  │     Store (SQLite)        │                      │
│  │  resources / runs / logs  │                      │
│  └───────────────────────────┘                      │
└──────────────────────┬──────────────────────────────┘
                       │ SSH
          ┌────────────┼────────────┐
          ▼            ▼            ▼
    ┌──────────┐ ┌──────────┐ ┌──────────┐
    │ Resource1│ │ Resource2│ │ Resource3│
    │ mu:tslib │ │ szu:a200 │ │ mu:llm4ts│
    └──────────┘ └──────────┘ └──────────┘
```

## 核心概念

| 概念 | 说明 |
|---|---|
| **Resource** | 一台可用的计算资源（SSH 服务器、Docker 容器、Slurm 节点等） |
| **Run** | 一次实验执行，绑定到某个 resource，有完整生命周期状态机 |
| **Snapshot** | 某个 resource 在某时刻的 CPU/GPU/内存状态 |
| **Agent Event** | Agent 每一步操作的审计日志（谁、为什么、做了什么） |

详细定义见 [concepts.md](concepts.md)。

## MVP 范围

- [ ] `aexp resource add` — 注册计算资源
- [ ] `aexp resource list` — 显示所有资源 + 实时状态
- [ ] `aexp resource status <name>` — 详细资源视图
- [ ] `aexp run submit <resource> -- <command>` — 通过 tmux 执行实验
- [ ] `aexp run list` — 显示所有 run
- [ ] `aexp run logs <run_id>` — 实时日志
- [ ] `aexp run cancel <run_id>` — 终止 run
- [ ] Web 仪表盘 — run 列表、详情、实时日志、资源条
- [ ] 资源轮询 — CPU/GPU/内存每 10s

**不包括**：MCP server、自动创建容器、MLflow 集成、调度、多用户认证。

## 技术栈

| 组件 | 选择 | 原因 |
|---|---|---|
| 语言 | Go 1.22+ | SSH 客户端、并发、单二进制 |
| HTTP | chi | 轻量 router |
| WebSocket | gorilla/websocket | 成熟、日志流 |
| 数据库 | SQLite (modernc.org/sqlite) | 零部署，纯 Go |
| SSH | golang.org/x/crypto/ssh | 标准 Go SSH |
| CLI | cobra | 标准 CLI 框架 |
| Web UI | 嵌入式 HTML/JS + Tailwind CDN | 无构建步骤 |

## 文件结构

```
aexp/
├── docs/
├── cmd/aexp/           # CLI 入口
├── internal/
│   ├── api/            # HTTP + WebSocket
│   ├── executor/       # SSH 连接池 + tmux 执行
│   ├── monitor/        # 资源轮询
│   └── store/          # SQLite 持久化
├── web/                # 嵌入式 HTML 仪表盘
├── go.mod
└── go.sum
```

## 实现顺序

1. `store` — 数据层
2. `executor` — SSH 连接池 + tmux 执行
3. `api` — HTTP + WebSocket
4. `web` — 仪表盘
5. `monitor` — 资源监控
6. `cmd/aexp/` — CLI 入口
