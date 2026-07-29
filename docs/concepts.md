# concepts.md — 核心概念定义

普通研究工作流只使用下面这些一等概念：

```text
Project
├── Assets
├── Runs
├── Journal
└── Evidence Maps
    ├── Primary
    └── Topic graphs
```

Resource、Storage、Transfer、Placement 和 Binding 是系统实现与管理员诊断概念。
Snapshot 和 Release 是 Run 输出进入 Evidence 的生命周期动作，不是独立工作区。

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
  status_source: remote_tmux
  status_freshness: fresh
  status_observed_at: 2026-06-07T14:30:10Z
  project_id: project_ecl
  target_id: target_ecl_mu
  recipe_name: train
  task_role: train
  evidence_grade: formal
  experiment_role: treatment
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
preflighting          │
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

**关键设计原则：状态必须同时携带来源和新鲜度。** `running` 只在
`status_source`、`status_observed_at` 和 `status_freshness` 一起解释时才是完整信息。

- executor 通过 wrapper script 写入 `exit_code` 文件来判断 success/fail
- executor 通过 tmux session 是否存在来判断 running/terminated
- active-run reconciler 定期按 resource 批量探测；探测失败保留缓存状态，但写入
  `status_check_error` 并标记 stale，不把缓存伪装成实时事实
- `lifecycle_status` 表示最后已知的任务生命周期；`observation_state` 表示当前能否
  观察远端权威状态。活跃任务遇到 SSH/Proxy 超时会显示黄色“状态未知”，而不是把
  `running` 改成失败或继续用绿色伪装为实时状态
- API 提交先写入 `queued` 和持久 `run_launch_jobs`，环境检测进入 `preflighting`；
  控制面重启会重新领取未完成的启动任务，CLI/MCP 同样先让 Run 可见再继续启动

Run 不保存研究推理。训练代码中的 `note()` 属于运行期遥测；Agent 或人对结果的解释、
问题、决定和下一步写入 Project Journal，并可选关联零个、一个或多个 Run。历史
RunMark 只作为兼容数据保留，不再是新增入口。

## Project Journal

Project 级、append-only 的工作日志，是日常研究推理的默认写入面。每条日志包含标题、
Markdown 正文、可选的下一步动作，以及可选的 Run 引用。日志不要求连成图，也不经过
Evidence proposal 审核。

Run Detail 只显示关联日志的回链和“写工作日志”入口；未归属 Project 的 Run 必须先完成
归属。真正需要进入稳定研究叙事的 claim、issue 或 plan，再从 Journal 晋升到 Evidence
Map。

新 Run 从创建开始就必须绑定一个已注册的 canonical Project，包括 setup、smoke 和
pilot；不再根据 cwd、仓库名或命令文本猜测归属。历史未归属记录保持原样，用户或 Agent
可在 Run Detail 或通过 `aexp_assign_run_project` 显式修正 terminal Run。改绑采用
expected-project CAS 并写入 Agent audit，只改变当前组织归属，不重写 launch-time
RunManifest、Snapshot、Release、Freeze 或既有 Journal/Evidence 历史。

## Project

`ProjectDefinition.id` 是唯一规范 Project 身份。每个 Project 至多有一张
`active + primary` Evidence Map，同时可以有多张 `active + secondary` 专题图。创建
Project 时在同一事务中创建主图。主图是默认收件箱，不是唯一证据图。

Agent 在提交 proposal 前先列出该 Project 的 active graphs。只有专题图的用途
（`description`）或 `routing_hints.recipes/keywords` 明确匹配时，Agent 才显式选择它并
记录 `routing_reason`；无法唯一判断时回到主图。aexp 不做隐藏的自动评分或关键词猜测。

`ProjectTarget`、`project_profiles`、旧 `/projects` 聚合和 manual category 保留为执行
绑定、探测缓存或迁移兼容视图，不是第二个 Project 事实源。旧 evidence-card 中唯一且明确
的 project 归属会无损提升为 canonical Project；旧记录不删除。

## Resource observation

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

## Asset

Asset 是不可变、完整校验的文件或目录 revision，包括 Dataset、配置、checkpoint、
metrics、predictions、mask、表和图。`asset-name@revision` 表示内容身份，
`storage://...` 只表示副本位置。路径、mtime 或调用者填写的 hash 不能单独满足 formal
provenance。

DatasetVersion 是 Dataset Asset 的兼容实现；Run output revision 使用同一身份语义。
内容变化必须产生新 revision，不能覆盖旧 manifest。

## Artifact inventory

`artifact_paths_json` 只是声明的 glob，不等于已经存在的产物。executor 在 run
结束后执行只读 discovery，建立远端 inventory 和 checksum；默认不下载文件。
collection 状态明确区分 `declared`、`discovering`、`indexed`、`partial`、`failed`。

```yaml
artifact:
  id: art_Km3qPx2w
  run_id: run_Yn7pL2wE
  path: results/ECL_iTransformer_metrics.json
  relative_path: results/ECL_iTransformer_metrics.json
  source_uri: ssh://rsrc_muTslib001/workspace/Time-Series-Library/results/ECL_iTransformer_metrics.json
  type: file          # file | dir
  role: metric
  size: 4096
  sha256: sha256:...
  collection_state: indexed
  modified_at: 2026-06-07T15:15:00Z
```

## Evidence Snapshot 与 Release

### NAS readiness

Storage Target 的 `healthy` 不能只表示 Mac 能连上 NAS。数据中心把可用性拆成：

- `control_plane`：Mac → NAS 的 SSH、rsync、根目录读写和容量检查；
- `data_plane`：每个 GPU 训练节点按真实 materialize/freeze 路径直连 NAS；
- `usable`：控制面通过且至少一条训练节点数据面通过。

编辑连接配置后旧健康状态立即失效；超过十分钟未检查时 UI-v2 显示为过期。删除
Storage Target 只删除 aexp 控制面登记，不删除 NAS 文件或底层 SSH Resource；存在
DatasetVersion 或 Run Freeze 引用时返回冲突并拒绝删除。

Artifact inventory 是远端当前状态，可以重新索引；Evidence Snapshot 只引用已经完成
发布和校验的 Run output revisions，不负责重新发现、选择或运输文件。

```text
Run + final RunManifest + verified output revisions
  → immutable Evidence Snapshot
  → Project aggregate command
  → Project release gate
  → released | blocked
```

相同 Run 与 output revision set 幂等返回同一 Snapshot；输出变化产生新 Snapshot。
Release 是 append-only 判定事件：规则不通过是 `blocked`，命令故障是 `failed`，两者都
不会修改 Snapshot。formal claim 必须引用 `released` Snapshot；草稿 proposal 可以提前
存在但必须显示尚未通过门禁。

旧 `run freeze`/RunFreeze 数据保留可读兼容，但不再扩展为第二套传输、聚合或审批系统。

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
