# mod-security.md — 安全设计

## 威胁模型

aexp 让 Agent（和人）通过结构化接口在远程机器上执行命令。主要风险：

1. Agent 执行恶意/危险命令（rm -rf、挖矿、反弹 shell）
2. SSH key 泄露导致未授权访问
3. Agent 访问不该访问的路径
4. 多个 Agent/用户并发冲突
5. 日志中包含敏感信息（密码、token）

## 命令白名单 / 黑名单

MVP 阶段用简单黑名单：

```go
var BlockedCommands = []string{
    "rm -rf /",
    "shutdown",
    "reboot",
    "mkfs",
    "dd if=",
    "curl.*|.*sh",   // 管道执行
    "wget.*|.*sh",
    "> /dev/sd",
}
```

wrapper script 中的 `"$@"` 前加一层检查：

```bash
# wrapper.sh 开头
for blocked in "rm -rf /" "shutdown" "reboot"; do
  if echo "$*" | grep -qF "$blocked"; then
    echo "[aexp] BLOCKED: command matches deny pattern: $blocked" >&2
    exit 126
  fi
done
```

Phase 2 可以做更精细的 allowlist：

```yaml
security:
  allowed_commands:
    - python
    - conda
    - pip
    - bash scripts/*.sh
  blocked_paths:
    - /etc
    - /root/.ssh
    - /var/run/docker.sock
```

## 路径沙箱

`cwd` 参数必须在 `resource.root_dir` 下：

```go
func ValidateCwd(rootDir, cwd string) error {
    // cwd 可以是绝对路径或相对路径
    // 绝对路径必须在 rootDir 下
    // 相对路径相对于 rootDir
    resolved := filepath.Join(rootDir, cwd)
    if !strings.HasPrefix(resolved, rootDir) {
        return ErrPathEscape
    }
    return nil
}
```

远程 wrapper script 也做检查：

```bash
# 检查 cwd 是否在 root_dir 下
case "$CWD" in
  $ROOT_DIR*) ;;
  *) echo "[aexp] BLOCKED: cwd outside root_dir" >&2; exit 126 ;;
esac
```

## SSH Key 管理

- `aexp init` 生成专用密钥 `~/.aexp/id_ed25519`
- 不使用系统 `~/.ssh/id_rsa`（避免 Agent 拿到用户的所有 SSH 权限）
- 每个 resource 可以指定不同的 key（`auth_ref` 字段）
- 密钥文件权限 600

## Run 隔离

每个 run 有独立的：
- tmux session（`aexp_<run_id>`）
- 远程目录（`<root_dir>/.aexp/runs/<run_id>/`）
- 日志文件

Run 之间不能互相访问。wrapper script 不暴露其他 run 的路径。

## 日志安全

日志可能包含敏感信息。保护措施：
- 远程日志文件权限 600（只有执行用户可读）
- API 访问日志需要认证（Phase 2）
- `aexp run logs` 不缓存到终端 history
- SQLite 中的 log_lines 不存储环境变量值

## 多用户 / 多 Agent

MVP 是单用户系统。Phase 2 考虑：
- API token 认证
- resource 级别的访问控制
- run 的 `created_by` 字段用于区分不同 Agent session

## 并发控制

同一个 resource 上同时只允许一个 run（默认行为）。

```go
func (e *Executor) Submit(ctx context.Context, req SubmitRequest) (*Run, error) {
    // 检查该 resource 是否有 running 状态的 run
    activeRuns, _ := e.store.ListRuns(ctx, RunFilter{
        ResourceID: req.ResourceID,
        Status:     RunStatusRunning,
    })
    if len(activeRuns) > 0 {
        return nil, ErrResourceBusy
    }
    // ...
}
```

可以通过 `--force` 标志覆盖（人用，不用 Agent 用）。
