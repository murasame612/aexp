# mod-logger.md — 日志管理

## 设计目标

让 Agent 和人都能方便地查看实验日志，支持实时流式和历史查询两种模式。

## 日志存储

### 远程（resource 上）

wrapper script 写入：
```
<root_dir>/.aexp/runs/<run_id>/logs/stdout.log
<root_dir>/.aexp/runs/<run_id>/logs/stderr.log
```

这是日志的真实来源。

### 本地（aexp server）

可选缓存最近日志行到 SQLite，用于：
- 快速查询（不用 SSH）
- 搜索内容
- Web UI 首屏渲染

缓存策略：每个 run 最近 10000 行，LRU 淘汰。

## 日志读取

### CLI: tail -f 模式

```bash
aexp run logs run_Yn7pL2wE
```

实现：
1. SSH 到 resource
2. `tail -f -n 200 <remote_run_dir>/logs/stdout.log`
3. 逐行打印到终端
4. Ctrl+C 退出

### CLI: 最后 N 行

```bash
aexp run logs run_Yn7pL2wE --last 100
```

实现：
1. SSH 到 resource
2. `tail -n 100 <remote_run_dir>/logs/stdout.log`
3. 打印后退出

### API: 历史日志

```
GET /api/v1/runs/{id}/logs?source=stdout&offset=0&limit=200
```

```json
{
  "run_id": "run_Yn7pL2wE",
  "source": "stdout",
  "total_lines": 3021,
  "offset": 0,
  "limit": 200,
  "lines": [
    {
      "line_no": 1,
      "content": "[aexp] ========================================",
      "timestamp": "2026-06-07T14:30:05Z"
    }
  ]
}
```

### WebSocket: 实时流

```
ws://localhost:8080/ws/runs/{id}/logs
```

服务端行为：
1. 先发送最近 200 行历史（快速填充 UI）
2. 然后 SSH tail -f，新行实时推送

协议：

```json
// 日志行
{"type": "log.line", "run_id": "run_Yn7pL2wE", "source": "stdout", "line_no": 3021, "content": "Epoch 10 | loss=0.421"}

// 保活
{"type": "ping"}

// 错误（SSH 断开等）
{"type": "error", "message": "SSH connection lost, retrying..."}
```

客户端可以发送：
```json
// 切换到 stderr
{"type": "subscribe", "source": "stderr"}

// 回到 stdout
{"type": "subscribe", "source": "stdout"}
```

## 结构化日志解析

aexp 不负责解析用户实验日志的格式，但提供一个辅助 API：

```
GET /api/v1/runs/{id}/summary
```

返回最后 N 行日志的原始内容，Agent 自己决定怎么理解。

未来可以加 `metric_paths_json` 指定的文件解析，但 MVP 不做。

## 日志保留

- 远程日志：随 resource 生命周期，aexp 不主动删除
- 本地缓存：SQLite 中超过 30 天的 log_lines 自动清理
- 用户可以 `aexp run archive <run_id>` 导出完整日志包
