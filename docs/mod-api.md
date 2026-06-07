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
POST   /api/v1/runs                        提交新 run
GET    /api/v1/runs/{id}                   获取 run 详情
PATCH  /api/v1/runs/{id}                   更新 run（notes 等）
POST   /api/v1/runs/{id}/cancel            取消 run
GET    /api/v1/runs/{id}/logs              获取日志行（分页）
GET    /api/v1/runs/{id}/summary           获取 run 摘要（最后 N 行）
GET    /api/v1/runs/{id}/errors            获取 stderr 内容
GET    /api/v1/runs/{id}/metrics           读取指标文件
GET    /api/v1/runs/{id}/artifacts         列出产物
POST   /api/v1/runs/{id}/status-check      强制重新检查状态
```

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

服务端行为：
1. 先发送最近 200 行历史（快速填充 UI）
2. 然后 SSH tail -f，新行实时推送

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
