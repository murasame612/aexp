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

安装器是 Codex/Claude Code MCP CLI 的薄包装：

- Codex: `codex mcp remove/add aexp ...`
- Claude Code: `claude mcp remove/add --scope user aexp ...`
- 默认只管理名为 `aexp` 的 MCP server。
- 默认把 `AEXP_API_URL` 设置为 `http://127.0.0.1:8080/api/v1`，让 MCP 工具能复用本地 `aexp serve` 的 API fast path。

当前工具名：

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
- `aexp_mark_run`
- `aexp_list_run_marks`
- `aexp_exec_history`
- `aexp_exec_show`
- `aexp_event_metric`
- `aexp_event_progress`
- `aexp_event_param`
- `aexp_event_note`
- `aexp_project_detect`
- `aexp_project_doctor`
- `aexp_project_run`
- `aexp_project_sync`
- `aexp_sync_doctor`
- `aexp_sync_push`
- `aexp_sync_pull`
- `aexp_sync_remote_pull`
- `aexp_cli`（只允许 read-only CLI 子命令）

下面的 `list_resources/create_run/...` 是目标语义草案；当前实现使用上面的 `aexp_*`
名称，以避免和其他 MCP server 的通用工具名冲突。正常 agent 流程不应该退回裸 CLI；
`aexp_cli` 只作为 read-only 兼容口。
允许 `run snapshot`、`run events`、`run metrics` 这类只读事件命令；提交、
取消、同步、标注等操作仍应使用专用 MCP tool。

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

1. list_resources(tags=["timeseries"], status="idle")
   → 找到 mu-tslib 空闲

2. create_run(resource_id="rsrc_muTslib001", command="python train.py ...")
   → 得到 run_id="run_Yn7pL2wE"

3. (等待一段时间)

4. aexp_get_run_snapshot(run_id="run_Yn7pL2wE")
   → status=running, epoch=8/100, val/mAP50-95(B)=0.545

5. aexp_tail_run_events(run_id="run_Yn7pL2wE", last=50)
   → 查看原始结构化事件

6. (再等待)

7. aexp_get_run_status(run_id="run_Yn7pL2wE")
   → status=succeeded, exit_code=0

8. aexp_get_run_metrics(run_id="run_Yn7pL2wE")
   → latest formal metrics

9. list_run_artifacts(run_id="run_Yn7pL2wE")
   → checkpoints/model_best.pt
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
