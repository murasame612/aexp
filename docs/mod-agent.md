# mod-agent.md — MCP 工具定义

## 设计原则

aexp 限制 Agent 直接使用 shell。Agent 通过结构化工具操作实验，而不是 `ssh_exec`。

这保证了：
- 每次操作都有审计记录（agent_events）
- 命令在受控环境执行（wrapper script + tmux）
- Agent 无法绕过权限检查

## MCP 工具列表

当前可运行实现通过 `aexp mcp` 启动 stdio MCP server。第一版采用“二进制包装”：
MCP 工具调用会转成同一个本地 `aexp` 二进制的 CLI 子命令，因此行为与 CLI 保持一致。
其中 `aexp_exec` 仍会先尝试本地 `aexp serve` API fast path，以复用常驻进程里的 SSH pool。

用户侧安装可以走：

```bash
aexp mcp install --target all
aexp mcp uninstall --target all
```

安装器是 Codex/Claude Code/Hermes Agent MCP CLI 的薄包装：

- Codex: `codex mcp remove/add aexp ...`
- Claude Code: `claude mcp remove/add --scope user aexp ...`
- Hermes Agent: `hermes mcp remove/add aexp --command /usr/bin/env --args ...`
- 默认只管理名为 `aexp` 的 MCP server。
- 默认把 `AEXP_API_URL` 设置为 `http://127.0.0.1:8080/api/v1`，让 MCP 工具能复用本地 `aexp serve` 的 API fast path。

默认 `research` profile 只暴露 12 个高频工具：

- `aexp_agent_card`
- `aexp_project_list`
- `aexp_get_project_research_context`
- `aexp_create_project_journal_entry`
- `aexp_get_project_journal_entry`
- `aexp_list_resources`
- `aexp_submit_run`
- `aexp_list_runs`
- `aexp_get_run_snapshot`
- `aexp_tail_run_logs`
- `aexp_create_evidence_proposal`
- `aexp_plan_evidence_graph_proposal`

默认工作流先调用 `aexp_get_project_research_context`，只按返回的 `next_reads` 下钻。它不会返回 Journal 正文、完整 Run command/log/metric/artifact、完整图或 revision，因此不能把它当作新的事实库。Run 历史由 `aexp_list_runs` 继续提供筛选和分页，单个 Run 再由 snapshot/log 工具读取。

需要整理 Topic、审计 Evidence、rebase、branch 或 reorganization 时，将 MCP server 的 `AEXP_MCP_TOOL_PROFILE` 设为 `advanced`。项目修复和维护会话使用 `admin`；迁移期调试可用 `all`。profile 只控制工具发现和上下文噪声，不替代授权。兼容期内，已知的旧工具名仍可精确调用。

历史兼容工具示例包括（实际清单以 `AEXP_MCP_TOOL_PROFILE=all` 的 `tools/list` 为准）：

- `aexp_agent_card`
- `aexp_init`
- `aexp_project_init`
- `aexp_list_resources`
- `aexp_doctor`
- `aexp_resource_explore`
- `aexp_resource_add`
- `aexp_resource_add_local`
- `aexp_resource_update`
- `aexp_resource_remove`
- `aexp_exec`
- `aexp_submit_run`
- `aexp_assign_run_project`
- `aexp_list_runs`
- `aexp_refresh_runs`
- `aexp_get_run_status`
- `aexp_get_run_snapshot`
- `aexp_tail_run_events`
- `aexp_get_run_metrics`
- `aexp_latest_run_metrics`（`aexp_get_run_metrics` 别名）
- `aexp_tail_run_logs`
- `aexp_cancel_run`
- `aexp_archive_run`
- `aexp_restore_run`
- `aexp_delete_run`
- `aexp_create_project_journal_entry`
- `aexp_list_project_journal`
- `aexp_get_project_journal_entry`
- `aexp_update_project_journal_next_action`
- `aexp_audit_evidence_map`
- `aexp_exec_history`
- `aexp_exec_show`
- `aexp_project_detect`
- `aexp_project_doctor`
- `aexp_project_run`
- `aexp_project_sync`
- `aexp_sync_doctor`
- `aexp_sync_push`
- `aexp_sync_pull`
- `aexp_sync_remote_pull`
- `aexp_cli`（通用 CLI 兼容口；不允许手写 `event` telemetry，也不允许用 `evidence add-node/add-edge/save` 绕过 proposal 审核）

### Evidence Proposal 中的 Topic / Research Thread / Stage Column

字段与门禁以 `docs/prd-research-evidence-graph.md` 的 `research-thread-v2` 契约及服务端 plan 结果为准；本节只说明 Agent 工作流，不另建一套规则源。

术语固定如下：Topic 是一个稳定的研究决策问题；Research Thread（假设链）是 Topic 中从明确假设开始的一条因果路径；Stage Column 是五个只读展示列，不是节点或容器。Agent 提交的是有界语义修改，而不是一袋互不相干的节点。推荐顺序为：

```text
假设 → 实验设计 → 实验结果 → 解释与判断 → 正式结论
                                      └────→ 中途问题
正式结论 / 中途问题 ──next_step──→ 新假设（子线程）
```

- 新假设使用 `type: "claim"` 与 `data_json.claimKind: "hypothesis"`；新结果使用 `claimKind: "result"`；正式结论使用 `type: "conclusion"`。旧 `hypothesis`、`run`、`plan` 等兼容类型仍可读，但不再作为新写法；
- “解释与判断”是 Result 真实去向关系的只读投影，不是第六种持久节点。每个新建或被修改的 Result 必须声明 `data_json.resultDisposition`：`conclusion`、`issue`、`mixed` 或 `pending`；`issue`、`mixed`、`pending` 还必须在 `dispositionReason`（或 `reveals_issue.rationale`）解释为什么暂时不能形成稳定结论；
- `conclusion` 通过 `supports` / `weakens` / `does_not_prove` 指向正式 Conclusion；`issue` 通过 `reveals_issue` 指向 Issue；`mixed` 同时具有两类出口；`pending` 暂无出口。门禁检查声明与真实边是否一致，但不会为了通过门禁伪造结论；
- 每个结果节点必须用节点级 `source_run_ids` / `source_snapshot_ids` 绑定实际来源。只有一个结果节点时可自动继承 proposal 的来源；一个 proposal 包含多个结果时必须逐个明确映射，不能让 proposal 总来源替代结果 provenance；
- 优先延续已有 Topic 中匹配的线程；只有问题确实改变研究问题时才建立子线程；
- `related_to` 只是背景关系，不能用来伪装一条已经形成的研究主线；
- proposal plan 会返回 `warnings`、`blockers` 与候选叠加后的 `projected_research`。warning 提醒缺假设、孤立节点、Run 无结论、问题无下一步等写作缺口，不阻止审核；blocker 才阻止接受；
- 接受前调用 `aexp_plan_evidence_graph_proposal` 检查候选 patch，并同时确认：`eligible=true`、本次触碰的语义节点没有进入 `projected_research.unassigned`、目标假设 Thread 中出现预期 Result（例如 `result_count=1`）。用户接受后再调用 `aexp_audit_evidence_map` 检查 accepted graph。新 Result 缺少 `Design --next_step--> Result`、缺失/失效 Run 或 Snapshot 引用都属于 blocker；legacy Run 节点和历史分支旁路只产生兼容性 warning。Audit 只读，不替 Agent 自动修图；
- 从已接受的 Conclusion 或 Issue 开启后续研究时，优先调用 `aexp_branch_from_outcome`。它会在同一 pending proposal 中生成 `Conclusion/Issue --next_step--> child Hypothesis`，并可选生成该假设的实验设计；它不会接受 proposal，也不会直接修改 accepted graph；
- UI 的五个 Stage Column 是 accepted graph 的只读投影，不写入坐标、revision 或 graph hash。无法可靠归入假设链的旧节点进入“待整理”，不做破坏性迁移。
- 普通会话先读 `aexp_get_project_research_context`；只有明确进入 Evidence 策展/治理时才切换 `advanced` profile 并调用 `aexp_get_evidence_thread_map`。修改时仍以服务端返回的 Stage Column、Research Thread 归属、跨 Thread 关系、`structural_health` 和 `unassigned.reason` 为准；UI 与 Agent 不再各自猜一次。
- 一张 Topic 只回答一个稳定的研究决策问题，但不设 4 条 Research Thread 或 32 个节点的语义上限。单条 Thread 达到 12 个语义节点时提示复核，16 个时提示强复核；它们都不是 blocker。`capacity` 只描述展示负载和可确定的 `thread_families`，`structural_health` 才描述 provenance、Result 去向、未归属比例与 Thread 复杂度。
- `unassigned` 是临时收件箱。整理时只能归入已有 Thread、创建有真实研究含义的 Hypothesis、留在 Project Journal，或通过允许的审核路径归档/删除；禁止为了清零而制造空 Hypothesis。
- 分开读取三种状态：`legacy_readable` 表示历史可读，`v2_compliant` 表示当前语义契约合规，`publication_ready` 表示正式 Result 的 provenance/release 门禁通过。结构健康是 advisory，只有 audit blocker 是硬门禁。
- 分叉不是“新 Run 自动开一条线”。稳定负面结果仍形成正式 Conclusion，使用 `result → weakens/does_not_prove → conclusion`；只有数据、设置、实现或证据强度使稳定判断暂不可得时，才使用 `result → reveals_issue → issue`。Conclusion 与 Issue 都能通过 `next_step` 产生新的研究假设；只有 conclusion/issue 指向 hypothesis 的最后一条 `next_step` 建立父子线程。改变任务、协议或独立报告问题时应新建 Topic，而不是无限向原 Topic 分叉。
- Research Thread 的执行脊柱使用 `hypothesis → next_step → experiment design → next_step → result`。不要用 `related_to` 代替设计到结果的正式关系；`related_to/custom` 只保留非因果背景，不参与 Thread 归属。
- 这里的限制有方向：每个新建或触碰的 Result **必须接收**一条 `Experiment Design --next_step--> Result` 入边；Result **不得发出**回到 Hypothesis、实验设计或另一个 Result 的语义出边。中性的 `related_to/custom` 只提供背景，不参与研究主干；旧图中的跳级边保持可读，但 audit 会标记为 `LEGACY_RESULT_BYPASS`，新 proposal 则由 `RESULT_OUTCOME_BYPASS` 阻止。
- 整理历史噪声时，每次只触碰约 8–12 个语义节点作为 reviewability budget，而不是数据模型容量：`aexp_plan_evidence_reorganization` → 检查 before/after 与 blocker → `aexp_create_evidence_reorganization_proposal`。涉及 Result 归属、provenance 或结论含义时应触碰更少。整理仍需用户审核，不能直接写 accepted graph。
- revision 仅因无关修改前进时使用 `aexp_rebase_evidence_proposal`；只有 touched node/edge 未变化才允许安全重基。旧 Topic 只通过 `aexp_archive_evidence_map` 软归档，不由 Agent 永久删除。

### Evidence Proposal 中的实验设计与 provenance

Evidence Map 的写模型只保存假设、实验设计、结果、正式结论或问题；UI/MCP 在 Result 与去向之间投影“解释与判断”，不增加新的写模型。
协议不是额外的集合层，而是实验设计的一部分：

- 新设计优先使用 `type: "experiment"`；旧 `plan` 和 `protocol` 节点继续映射到“实验设计”栏；
- 设计正文或 `data_json` 记录比较对象、dataset/split、预处理、seed、指标、判定规则和预算；
- Run、dataset manifest、config hash、Git 与 Freeze 是结果的 provenance，不需要在图上组成集合；
- typed edge 表达“验证、支持、削弱、暴露问题、下一步”等研究关系。

新 proposal 示例：

```json
{
  "nodes": [
    {
      "id": "design_clean810_matched",
      "type": "experiment",
      "title": "Clean-810 matched VIS/CAF comparison",
      "body": "固定 paired split、seed 41/42/43，以 mAP50 与 localization gate 判定；除输入模态外保持预算一致。",
      "data_json": "{\"dataset\":\"private-facade-good810-context2x@v1\",\"seeds\":[41,42,43],\"metrics\":[\"mAP50\",\"localization_gate\"]}"
    }
  ],
  "edges": []
}
```

旧 `groupKind=protocol` 数据不会删除，旧版 Overview 仍能读取；五个 Stage Column 不再把它投影为一张大卡，
新 Agent 也不得创建新的 protocol group。需要比较多个 Run 时，在一个实验设计中声明可比性，并在结果详情引用不可变 Run provenance。

### Evidence Proposal 中的旧版画布编排

旧版 Overview 仍兼容 `layout_intent`，但新 Agent 不再靠它整理默认阅读视图，也不能提交像素坐标：

```json
{
  "chain_id": "chain_...",
  "layout_intent": {
    "flow": "left_to_right",
    "ranks": [
      ["hypothesis_core", "issue_scope"],
      ["design_clean810_matched", "plan_baseline"],
      ["claim_current", "plan_next"]
    ],
    "rationale": "核心研究路线居中，协议与问题作为同阶段上下文。"
  },
  "nodes": [],
  "edges": []
}
```

- 外层数组从左到右表示阶段，内层数组从上到下表示同列顺序；
- 默认假设链由服务端确定性投影到五个 Stage Column，纯可读性调整无需 Proposal，也不改变 revision/hash；
- 每个节点只能出现一次，未知节点会被 plan 阻断；
- 旧协议容器仅为兼容数据；新编排意图直接写语义节点 ID；
- UI 先预览，再在接受 Proposal 后将意图转换为确定性坐标；
- 提案明确列出的卡片会在接受后移动，即使它此前被固定；未列出的卡片保持原位；
- 编排意图不是 Evidence Map 的科学语义，也不进入 graph hash。

下面的 `list_resources/create_run/...` 是目标语义草案；当前实现使用上面的 `aexp_*`
名称，以避免和其他 MCP server 的通用工具名冲突。正常 agent 流程不应该退回裸 CLI；
`aexp_cli` 只作为兼容口。它允许 `run cancel`、`run submit`、`sync push`、
`resource update` 等修改类命令，只拦截会挂住 MCP 进程的 `serve`、`mcp` 和
无限 `--follow`，并拒绝 `evidence add-node/add-edge/save` 这类绕过审核的 accepted-graph 写入。常见操作仍优先使用专用 MCP tool，因为参数 schema 更稳定。

### list_resources

列出所有可用计算资源及其当前状态。

```json
{
  "name": "list_resources",
  "input": {
    "tags": ["timeseries"],    // 可选，按标签过滤
    "status": "idle"           // 可选，按状态过滤
  }
}
```

```json
{
  "resources": [
    {
      "id": "rsrc_muTslib001",
      "name": "mu-tslib",
      "type": "ssh",
      "status": "idle",
      "gpu": [{"index": 0, "name": "RTX 4090", "mem_total": 24564}],
      "conda_env": "tslib",
      "tags": ["4090", "timeseries"],
      "current_run": null,
      "snapshot": {"cpu_percent": 23, "mem_used_mb": 28800, "mem_total_mb": 64000}
    }
  ]
}
```

### create_run

提交一个新实验。

```json
{
  "name": "create_run",
  "input": {
    "resource_id": "rsrc_muTslib001",
    "name": "ECL-iTransformer-run1",
    "command": "python train.py --data ECL --model iTransformer",
    "cwd": "/workspace/Time-Series-Library",
    "conda_env": "tslib",
    "log_paths": ["logs/*.log"],
    "artifact_paths": ["results/*.json"],
    "metric_paths": ["results/*.json"]
  }
}
```

```json
{
  "run_id": "run_Yn7pL2wE",
  "status": "running",
  "tmux_session": "aexp_run_Yn7pL2wE",
  "resource": "mu-tslib",
  "started_at": "2026-06-07T14:30:05Z"
}
```

### get_run_status

查询 run 的当前状态。

```json
{
  "name": "get_run_status",
  "input": {
    "run_id": "run_Yn7pL2wE"
  }
}
```

```json
{
  "run_id": "run_Yn7pL2wE",
  "name": "ECL-iTransformer-run1",
  "status": "running",
  "resource": "mu-tslib",
  "command": "python train.py --data ECL --model iTransformer",
  "cwd": "/workspace/Time-Series-Library",
  "exit_code": null,
  "started_at": "2026-06-07T14:30:05Z",
  "elapsed_seconds": 754,
  "snapshot": {"cpu_percent": 67, "gpu_util": 45, "gpu_mem_used": 8192}
}
```

### get_run_snapshot

获取低噪声 run 快照。默认不刷新远端 tmux 状态，只读取本地 run 记录和结构化
UI events tail，适合作为训练过程的主监控入口。
建议 agent 每 30-60 秒查询一次；如果 epoch/step 没变化，按指数退避到最多
120 秒。不要用高频 `get_run_status` 或 `tail_run_logs` 作为正常训练监控。

```json
{
  "name": "aexp_get_run_snapshot",
  "input": {
    "run_id": "run_Yn7pL2wE",
    "last": 500,
    "refresh": false
  }
}
```

```json
{
  "run": {"id": "run_Yn7pL2wE", "status": "running", "kind": "formal"},
  "events": {"path": ".aexp/events/run_Yn7pL2wE.jsonl", "total_lines": 116},
  "progress": {"epoch": {"current": 8, "total": 100, "percent": 8}},
  "metrics": {
    "val/mAP50-95(B)": {"value": 0.545, "epoch": 8}
  },
  "params": {"model": "yolov8s"}
}
```

### tail_run_events

读取结构化 UI event JSONL tail。用于查看原始事件流，而不是原始 stdout/stderr。

```json
{
  "name": "aexp_tail_run_events",
  "input": {
    "run_id": "run_Yn7pL2wE",
    "last": 50
  }
}
```

### get_run_metrics

从 UI events 中聚合最新指标。

```json
{
  "name": "aexp_get_run_metrics",
  "input": {
    "run_id": "run_Yn7pL2wE",
    "last": 500
  }
}
```

`aexp_latest_run_metrics` 是同等语义的别名，方便 agent 按自然命名调用。

### tail_run_logs

获取 run 的最近 stdout/stderr。这个工具应该用于失败、卡住、OOM、缺少 events
时诊断，不应该作为正常训练进度的高频监控方式。

### list_run_artifacts

列出 run 产出的文件。

```json
{
  "name": "list_run_artifacts",
  "input": {
    "run_id": "run_Yn7pL2wE"
  }
}
```

```json
{
  "run_id": "run_Yn7pL2wE",
  "artifacts": [
    {"path": "results/ECL_iTransformer_metrics.json", "size": 4096, "type": "file"},
    {"path": "checkpoints/model_best.pt", "size": 104857600, "type": "file"}
  ]
}
```

### read_run_artifact

读取某个 artifact 文件的内容（文本类）。

```json
{
  "name": "read_run_artifact",
  "input": {
    "run_id": "run_Yn7pL2wE",
    "path": "results/ECL_iTransformer_metrics.json"
  }
}
```

### stop_run

取消正在运行的实验。

```json
{
  "name": "stop_run",
  "input": {
    "run_id": "run_Yn7pL2wE",
    "reason": "loss diverged, restarting with lower lr"
  }
}
```

## Agent 调用规范

### 典型流程

```
Agent 想跑一个实验：

1. aexp_get_project_research_context(project_id="project_archDam")
   → 读取当前研究判断、开放下一步、Run 摘要和按需 next_reads

2. list_resources(tags=["timeseries"], status="idle")
   → 找到 mu-tslib 空闲

3. create_run(resource_id="rsrc_muTslib001", command="python train.py ...")
   → 得到 run_id="run_Yn7pL2wE"

4. (等待一段时间)

5. aexp_get_run_snapshot(run_id="run_Yn7pL2wE")
   → status=running, epoch=8/100, val/mAP50-95(B)=0.545

6. aexp_tail_run_logs(run_id="run_Yn7pL2wE", tail=100)
   → 仅在 snapshot 不足或需要精确 stderr/stdout 时读取

7. aexp_create_project_journal_entry(..., run_ids=["run_Yn7pL2wE"])
   → 保存解释、决定和下一步；只有 durable judgment 改变时才进入 proposal
```

### 错误处理

所有工具返回统一错误格式：

```json
{
  "error": "resource not found",
  "code": "RESOURCE_NOT_FOUND",
  "details": "no resource with id rsrc_invalid"
}
```

错误码：
- `RESOURCE_NOT_FOUND`
- `RUN_NOT_FOUND`
- `RESOURCE_UNREACHABLE`
- `RUN_ALREADY_FINISHED`
- `COMMAND_REJECTED`（安全模块拦截）
- `INTERNAL_ERROR`

### Agent Event 自动记录

每次 MCP 工具调用，自动写入 agent_events 表：

```json
{
  "run_id": "run_Yn7pL2wE",
  "actor": "agent_thread_abc123",   // 从 MCP session 获取
  "tool_name": "create_run",
  "input_json": "{...}",            // 工具入参
  "output_json": "{...}",           // 工具返回
  "timestamp": "2026-06-07T14:30:00Z"
}
```
