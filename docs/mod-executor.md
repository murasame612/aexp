# mod-executor.md — 实验执行引擎

## 执行方式

所有实验通过 **tmux session** 在远程资源上执行。

为什么用 tmux：
- SSH 断开后实验继续运行
- Agent / 人可以随时重新 attach 看输出
- 每个 run 有独立的 tmux session，互不干扰
- session 名称确定性：`aexp_<run_id>`

## Wrapper Script

所有实验都通过一个 wrapper script 启动，解决 tmux exit code 不好拿的问题。

脚本会部署到每个 resource 的 `~/.aexp/wrapper.sh`：

```bash
#!/usr/bin/env bash
set -o pipefail

RUN_DIR="$1"
shift

mkdir -p "$RUN_DIR"
mkdir -p "$RUN_DIR/logs"

echo $$ > "$RUN_DIR/pid"
date +%s > "$RUN_DIR/started_at"
echo "running" > "$RUN_DIR/status"

{
  echo "[aexp] ========================================"
  echo "[aexp] Run started at $(date)"
  echo "[aexp] Command: $*"
  echo "[aexp] PID: $$"
  echo "[aexp] ========================================"
  echo ""

  "$@"

  EXIT_CODE=$?
  echo ""
  echo "[aexp] ========================================"
  echo "[aexp] Finished at $(date) with exit code $EXIT_CODE"
  echo "[aexp] ========================================"

  echo "$EXIT_CODE" > "$RUN_DIR/exit_code"
  date +%s > "$RUN_DIR/finished_at"

  if [ "$EXIT_CODE" -eq 0 ]; then
    echo "succeeded" > "$RUN_DIR/status"
  else
    echo "failed" > "$RUN_DIR/status"
  fi

  exit "$EXIT_CODE"

} > >(tee -a "$RUN_DIR/logs/stdout.log") \
  2> >(tee -a "$RUN_DIR/logs/stderr.log" >&2)
```

**关键设计**：

executor 不需要猜命令有没有结束，只要检查：

| 检查项 | 文件 | 含义 |
|---|---|---|
| `RUN_DIR/exit_code` 是否存在 | exit_code | run 已结束 |
| `RUN_DIR/status` 内容 | status | succeeded / failed |
| tmux session 是否存在 | tmux has-session | 进程是否还在 |
| `RUN_DIR/logs/stdout.log` 是否在增长 | file size | 是否卡死 |

## Run 状态机

```
created
  │   executor.CreateRun()
  ▼
starting
  │   SSH 连接 + 部署 wrapper + tmux new-session
  ▼
running
  │   tmux session 存在且 exit_code 文件不存在
  │
  ├─ exit_code == 0 ──► succeeded
  ├─ exit_code != 0 ──► failed
  ├─ user cancel    ──► cancelled
  └─ SSH 断开超时   ──► lost
```

**状态更新逻辑**：

```go
func (e *Executor) CheckRunStatus(ctx context.Context, run *Run) (RunStatus, error) {
    // 1. 检查 remote_run_dir/exit_code 文件
    //    存在 → 读取 exit_code
    //      exit_code == 0 → succeeded
    //      exit_code != 0 → failed
    //    不存在 → 继续检查

    // 2. 检查 tmux session 是否存在
    //    tmux has-session -t aexp_<run_id>
    //    存在 → running
    //    不存在但 exit_code 也不存在 → lost (异常退出)

    // 3. 检查 stdout.log 最后修改时间
    //    超过 10 分钟没更新 → 可能卡死，标记 warning（不改 status）
}
```

## 提交流程

```go
type SubmitRequest struct {
    ResourceID string   `json:"resource_id"`
    Name       string   `json:"name"`
    Command    string   `json:"command"`
    Cwd        string   `json:"cwd"`
    CondaEnv   string   `json:"conda_env"`
    LogPaths   []string `json:"log_paths"`
    ArtifactPaths []string `json:"artifact_paths"`
    MetricPaths   []string `json:"metric_paths"`
    EnvVars    map[string]string `json:"env_vars"`
}

func (e *Executor) Submit(ctx context.Context, req SubmitRequest) (*Run, error) {
    // 1. 从 store 获取 resource 信息
    // 2. 创建 Run 记录 (status=created)
    // 3. SSH 连接到 resource
    // 4. 确保 wrapper.sh 已部署 (首次需要)
    // 5. 计算 remote_run_dir: <root_dir>/.aexp/runs/<run_id>
    // 6. 构建 tmux 命令:
    //    tmux new-session -d -s aexp_<run_id> \
    //      "bash ~/.aexp/wrapper.sh <remote_run_dir> \
    //       bash -c 'source conda.sh && conda activate <env> && cd <cwd> && <command>'"
    // 7. SSH 执行
    // 8. 更新 Run: status=running, tmux_session, started_at
    // 9. 记录 agent_event (如果由 agent 提交)
    // 10. 返回 Run
}
```

## 取消流程

```go
func (e *Executor) Cancel(ctx context.Context, runID string) error {
    // 1. SSH 进入 resource
    // 2. tmux send-keys -t aexp_<run_id> C-c
    // 3. 等待 5 秒
    // 4. 检查 tmux session 是否还在
    //    在 → tmux kill-session -t aexp_<run_id>
    // 5. 更新 Run: status=cancelled, finished_at=now
    // 6. 写入 remote: echo "cancelled" > RUN_DIR/status
}
```

## 日志 Tail

两种方式：

### 远程文件 tail（主）

```go
func (e *Executor) TailLogs(ctx context.Context, runID string, lastN int) (<-chan LogLine, error) {
    // SSH 执行: tail -f -n <lastN> <remote_run_dir>/logs/stdout.log
    // 逐行发送到 channel
    // ctx cancel 时关闭
}
```

### tmux capture-pane（备选）

wrapper script 未部署时的降级方案：

```bash
tmux capture-pane -t aexp_<run_id> -p -S -200
```

## Wrapper Script 部署

首次向某个 resource 提交 run 时，自动部署 wrapper：

```go
func (e *Executor) ensureWrapper(ctx context.Context, client *ssh.Client, rootDir string) error {
    // 1. 检查 ~/.aexp/wrapper.sh 是否存在
    //    存在且 hash 匹配 → 跳过
    // 2. scp 或 ssh "cat > ~/.aexp/wrapper.sh" 写入脚本
    // 3. chmod +x
}
```

## Remote Run Directory 结构

每个 run 在远程资源上有独立目录：

```
<root_dir>/.aexp/
  wrapper.sh
  runs/
    run_Yn7pL2wE/
      pid              # 进程 PID
      status           # running | succeeded | failed | cancelled
      started_at       # unix timestamp
      finished_at      # unix timestamp (结束后)
      exit_code        # 退出码 (结束后)
      logs/
        stdout.log     # 标准输出
        stderr.log     # 标准错误
      artifacts/       # 可选，收集时复制过来
```
