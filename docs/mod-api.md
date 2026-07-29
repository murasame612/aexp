# mod-api — HTTP API + WebSocket

## Server

单 Go 二进制，同时提供：
- REST API: `/api/v1/*`
- WebSocket: `/ws/*`
- 静态 Web UI: `/`（embed.FS 嵌入）

启动：`aexp serve --port 8080 --db ~/.aexp/aexp.db`

## REST 端点

### Resources

```
GET    /api/v1/resources                  列出所有资源
POST   /api/v1/resources                  注册新资源
GET    /api/v1/resources/{id}             获取资源详情 + 最新快照
PUT    /api/v1/resources/{id}             更新资源
DELETE /api/v1/resources/{id}             删除资源
GET    /api/v1/resources/{id}/snapshots   资源历史快照
POST   /api/v1/resources/{id}/test        测试 SSH 连通性
```

### Runs

```
GET    /api/v1/runs                        列出所有 run（可过滤）
POST   /api/v1/runs                        先持久化 queued run，返回 202，后台预检/启动
GET    /api/v1/runs/active                 独立的活跃 RunSummary（不受实验列表过滤器影响）
GET    /api/v1/runs/summaries              分页轻量 RunSummary，不返回完整命令/大字段
GET    /api/v1/runs/changes                按 after_seq 或 updated_since 增量补变更
GET    /api/v1/runs/changes/stream         全局 run-change SSE（支持 Last-Event-ID）
GET    /api/v1/runs/{id}                   获取 run 详情
POST   /api/v1/runs/{id}/cancel            取消 run
GET    /api/v1/runs/{id}/logs              获取日志行（支持 after_line cursor）
GET    /api/v1/runs/{id}/summary           获取 run 摘要（最后 N 行）
GET    /api/v1/runs/{id}/artifacts         列出产物
GET    /api/v1/runs/{id}/artifact-collection 查看 declared/discovering/indexed/partial/failed 状态
POST   /api/v1/runs/{id}/artifacts/collect 异步重建远端产物 inventory（不自动下载）
GET    /api/v1/runs/{id}/manifest          读取版本化、哈希后的 Run Manifest
POST   /api/v1/runs/{id}/status-check      强制重新检查状态
```

`run_changes` 是 SQLite 中的持久变更流水，CLI、MCP、API 和 reconciler 的写入都会触发；
因此 SSE 不是单进程内存通知。ui-v2 先取 `/runs/active` 的 `change_cursor`，再连接 SSE，
断线时按 cursor 重放，并保留 30 秒低频轮询作为回退。

### Executable Project / Target 与 Launchpad

旧 `GET /projects` 是研究证据聚合视图。可执行模型使用独立兼容路由：

```text
GET|POST       /api/v1/project-definitions
GET|PUT|DELETE /api/v1/project-definitions/{id}
GET|POST       /api/v1/project-definitions/{id}/targets
GET|PUT|DELETE /api/v1/project-targets/{id}
POST           /api/v1/project-definitions/{id}/targets/{targetID}/prepare-plan
POST           /api/v1/project-definitions/{id}/targets/{targetID}/prepare
POST           /api/v1/run-comparisons/analyze
```

`prepare-plan` 无远端写入；`prepare` 创建一个 tracked setup run，固定为非正式证据。
比较接口执行 provenance/comparability gate、seed 聚合，并返回 Markdown 报告。

### System

```
GET    /api/v1/health                      健康检查
GET    /api/v1/stats                       汇总统计
```

## WebSocket 端点

### 日志流

```
ws://localhost:8080/ws/runs/{id}/logs
```

ui-v2 先通过 HTTP `after_line=<cursor>` 补快照，再以同一 cursor 建立 WebSocket。
服务端从 `after_line + 1` 开始补发并 follow；断线后客户端重复 HTTP catch-up，按
`(source,line_no)` 去重。文件截断时 HTTP 响应 `reset=true`，客户端替换旧 generation。

协议：

```json
// 日志行
{"type": "log.line", "run_id": "run_Yn7pL2wE", "source": "stdout", "line_no": 3021, "content": "Epoch 10 | loss=0.421", "timestamp": "2026-06-07T14:45:00Z"}

// 保活
{"type": "ping"}

// 错误
{"type": "error", "message": "SSH connection lost, retrying..."}
```

客户端可发送：
```json
// 切换到 stderr
{"type": "subscribe", "source": "stderr"}

// 回到 stdout
{"type": "subscribe", "source": "stdout"}
```

### Run 状态

```
ws://localhost:8080/ws/runs/{id}/status
```

```json
{"type": "run.status", "run_id": "run_Yn7pL2wE", "status": "succeeded", "exit_code": 0}
```

### 资源指标

```
ws://localhost:8080/ws/resources/{id}/metrics
```

```json
{
  "type": "resource.snapshot",
  "resource_id": "rsrc_muTslib",
  "timestamp": "2026-06-07T14:30:10Z",
  "cpu_percent": 67.3,
  "mem_used_mb": 12800,
  "mem_total_mb": 64000,
  "gpus": [{"index": 0, "util": 45, "mem_used": 8192, "mem_total": 24564}]
}
```

## 请求/响应示例

### 提交 Run

```
POST /api/v1/runs
{
  "resource_id": "rsrc_muTslib",
  "name": "ECL-iTransformer-run1",
  "command": "python train.py --data ECL --model iTransformer",
  "cwd": "/workspace/Time-Series-Library",
  "conda_env": "tslib",
  "log_paths": ["logs/*.log"],
  "artifact_paths": ["results/*.json"],
  "metric_paths": ["results/*.json"]
}
```

响应：
```json
{
  "id": "run_Yn7pL2wE",
  "resource_id": "rsrc_muTslib",
  "name": "ECL-iTransformer-run1",
  "status": "running",
  "tmux_session": "aexp_run_Yn7pL2wE",
  "started_at": "2026-06-07T14:30:05Z"
}
```

### 列出 Run

```
GET /api/v1/runs?status=running&resource=rsrc_muTslib&limit=20&offset=0
```

```json
{
  "runs": [...],
  "total": 42,
  "limit": 20,
  "offset": 0
}
```

### 获取日志

```
GET /api/v1/runs/run_Yn7pL2wE/logs?source=stdout&offset=100&limit=50
```

```json
{
  "run_id": "run_Yn7pL2wE",
  "source": "stdout",
  "total_lines": 3021,
  "offset": 100,
  "limit": 50,
  "lines": [
    {"line_no": 101, "content": "Epoch 1/100", "timestamp": "2026-06-07T14:31:00Z"}
  ]
}
```

## 错误格式

```json
{
  "error": "resource not found",
  "code": "RESOURCE_NOT_FOUND",
  "details": "no resource with id rsrc_invalid"
}
```

HTTP 状态码：
- 200 OK
- 201 Created
- 400 Bad Request
- 404 Not Found
- 409 Conflict（重复资源名）
- 500 Internal Server Error

## Server 结构

```go
type Server struct {
    store    store.Store
    executor *executor.Executor
    monitor  *monitor.Manager
    router   *chi.Mux
    hub      *WSHub
}

func NewServer(store store.Store, exec *executor.Executor, mon *monitor.Manager, logger *slog.Logger, apiToken string, allowLoopbackNoAuth bool) *Server
func (s *Server) Start(addr string) error
```

## 嵌入式 Web UI

```go
//go:embed web/*
var webFS embed.FS
```

无构建步骤，单 HTML 文件编译进二进制。
