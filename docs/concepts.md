# concepts.md — 核心概念定义

## Resource

一台可执行实验的计算资源。不限于 Docker 容器。

```yaml
resource:
  id: rsrc_muTslib001
  name: mu-tslib
  type: ssh          # local | ssh | docker | slurm | k8s
  host: 192.168.1.100
  port: 22
  user: root
  auth_ref: ~/.aexp/id_ed25519
  root_dir: /workspace
  conda_env: tslib   # 默认 conda 环境
  gpu_indices: "0"   # 可见 GPU
  tags: [4090, timeseries, dam]
  status: idle       # idle | busy | error | unreachable
  created_at: 2026-06-07T14:00:00Z
  updated_at: 2026-06-07T14:00:00Z
```

**status 的含义**：resource 的健康状态，由 monitor 模块判断，与 run 状态无关。

- `idle`: SSH 可达，GPU 利用率 < 5%，无活跃 run
- `busy`: 有活跃 run 或 GPU 利用率 ≥ 5%
- `error`: SSH 可达但 nvidia-smi 失败 / 某项检查异常
- `unreachable`: SSH 连接失败

## Run

一次实验执行。绑定到某个 resource，有独立的生命周期状态机。

```yaml
run:
  id: run_Yn7pL2wE
  resource_id: rsrc_muTslib001
  name: ECL-iTransformer-run1
  status: running
  cwd: /workspace/Time-Series-Library
  command: python train.py --data ECL --model iTransformer
  conda_env: tslib
  env_json: "{}"
  log_paths_json: '["logs/*.log"]'
  artifact_paths_json: '["results/*.json", "checkpoints/*"]'
  metric_paths_json: '["results/*.json"]'
  tmux_session: aexp_run_Yn7pL2wE
  remote_run_dir: /workspace/.aexp/runs/run_Yn7pL2wE
  exit_code: 0
  created_by: agent_thread_abc123
  created_at: 2026-06-07T14:30:00Z
  started_at: 2026-06-07T14:30:05Z
  finished_at: null
```

### Run 状态机

```
created
  │
  ▼
queued ──────────────┐
  │                   │
  ▼                   │
starting              │
  │                   │
  ▼                   │
running ──┬──► succeeded
          │
          ├──► failed (exit_code != 0)
          │
          ├──► cancelled (用户取消)
          │
          └──► lost (SSH 断开且无法恢复)
```

**关键设计原则：run 的真实状态来自 executor，不是 monitor。**

- executor 通过 wrapper script 写入 `exit_code` 文件来判断 success/fail
- executor 通过 tmux session 是否存在来判断 running/terminated
- monitor 只负责 resource 层面的健康度，不干预 run 状态

## Snapshot

resource 在某个时刻的资源使用快照。由 monitor 模块定时采集。

```yaml
snapshot:
  id: 1
  resource_id: rsrc_muTslib001
  run_id: run_Yn7pL2wE   # 可选，关联当前运行
  cpu_percent: 67.3
  mem_used_mb: 12800
  mem_total_mb: 64000
  gpu_json: |
    [{"index":0,"name":"RTX 4090","util":45,"mem_used":8192,"mem_total":24564}]
  disk_json: |
    {"workspace_used_gb":120,"workspace_total_gb":500}
  load_1m: 3.2
  load_5m: 2.8
  load_15m: 2.1
  timestamp: 2026-06-07T14:30:10Z
```

## Artifact

run 产出的文件，由 executor 在 run 结束后收集。

```yaml
artifact:
  id: art_Km3qPx2w
  run_id: run_Yn7pL2wE
  path: results/ECL_iTransformer_metrics.json
  type: file          # file | dir
  size: 4096
  modified_at: 2026-06-07T15:15:00Z
```

## Agent Event

Agent 的每一次操作审计。这是 aexp 与普通实验面板的核心差异。

```yaml
agent_event:
  id: evt_Px2wN7mk
  run_id: run_Yn7pL2wE   # 可选，有些操作不关联 run
  actor: agent_thread_abc123
  tool_name: create_run
  input_json: |
    {"resource":"mu-tslib","command":"python train.py ..."}
  output_json: |
    {"run_id":"run_Yn7pL2wE","status":"running"}
  timestamp: 2026-06-07T14:30:00Z
```

**为什么需要 agent_events？**

普通实验面板记录"跑了什么"。aexp 还要记录"Agent 为什么跑"。
这让人可以审计 Agent 的决策链，也让 Agent 在多轮会话中保持实验上下文。

## Log Line

一条结构化的日志记录。

```yaml
log_line:
  run_id: run_Yn7pL2wE
  source: stdout      # stdout | stderr
  line_no: 3021
  content: "Epoch 10 | loss=0.421 | val_mae=0.318"
  timestamp: 2026-06-07T14:45:00Z
```

日志存在两个地方：
- **远程文件**：`<remote_run_dir>/stdout.log` 和 `stderr.log`（wrapper script 写入）
- **本地 DB**：可选缓存最近 N 行，用于快速查询和搜索

## 概念关系图

```
Resource (mu-tslib)
  │
  ├── Run (ECL-iTransformer-run1)
  │     ├── Log Lines (stdout, stderr)
  │     ├── Artifacts (metrics.json, checkpoints/)
  │     ├── Agent Events (why created, what params)
  │     └── Snapshots (during run, linked)
  │
  ├── Run (ECL-iTransformer-run2)
  │     └── ...
  │
  └── Snapshots (idle history)
```
