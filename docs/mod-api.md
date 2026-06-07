# mod-api — HTTP API + WebSocket

## Server

Single Go binary serves:
- REST API at `/api/v1/*`
- WebSocket at `/ws/*`
- Static web UI at `/` (embedded via `embed.FS`)

Start with: `aexp serve --port 8080 --db ~/.aexp/aexp.db`

## REST Endpoints

### Containers

```
GET    /api/v1/containers                  List all containers
POST   /api/v1/containers                  Register new container
GET    /api/v1/containers/{id}             Get container detail + latest resource
PUT    /api/v1/containers/{id}             Update container
DELETE /api/v1/containers/{id}             Remove container
GET    /api/v1/containers/{id}/resources   Resource history (last N snapshots)
POST   /api/v1/containers/{id}/test        Test SSH connectivity
```

### Runs

```
GET    /api/v1/runs                        List all runs (filterable)
POST   /api/v1/runs                        Submit new run
GET    /api/v1/runs/{id}                   Get run detail
PATCH  /api/v1/runs/{id}                   Update run (notes, etc)
POST   /api/v1/runs/{id}/cancel            Cancel run
GET    /api/v1/runs/{id}/logs              Get log lines (with offset/limit)
GET    /api/v1/runs/{id}/logs/stream       WebSocket: live log stream
POST   /api/v1/runs/{id}/status-check      Force status re-check
```

### System

```
GET    /api/v1/health                      Health check
GET    /api/v1/stats                       Summary stats (total containers, runs, etc)
```

## WebSocket Endpoints

### Log Streaming

```
GET /ws/runs/{id}/logs
```

Server pushes new log lines as they appear:

```json
{"type": "log", "stream": "stdout", "line": 142, "content": "Epoch 3/100, loss=0.0234"}
```

Also sends periodic keepalive:
```json
{"type": "ping"}
```

Client can send:
```json
{"type": "seek", "line": 0}
```

### Resource Updates

```
GET /ws/resources
```

Broadcasts resource snapshots for all containers:

```json
{
  "type": "resource",
  "container_id": "c_Ab3xK9mQ",
  "cpu_percent": 67.3,
  "gpus": [{"index": 0, "util": 45, "mem_used": 8192, "mem_total": 24564}]
}
```

## Request/Response Examples

### Submit Run

```
POST /api/v1/runs
Content-Type: application/json

{
  "container_id": "c_Ab3xK9mQ",
  "name": "ECL-iTransformer-run1",
  "command": "python train.py --data ECL --model iTransformer --features M",
  "cwd": "/workspace/Time-Series-Library",
  "conda_env": "tslib",
  "log_paths": "logs/*.log,results/*.json"
}
```

Response:
```json
{
  "id": "r_Yn7pL2wE",
  "container_id": "c_Ab3xK9mQ",
  "name": "ECL-iTransformer-run1",
  "status": "running",
  "tmux_session": "aexp_r_Yn7pL2wE",
  "pid": 12345,
  "started_at": "2026-06-07T14:30:00Z"
}
```

### List Runs

```
GET /api/v1/runs?status=running&container=c_Ab3xK9mQ&limit=20&offset=0
```

```json
{
  "runs": [...],
  "total": 42,
  "limit": 20,
  "offset": 0
}
```

### Get Logs

```
GET /api/v1/runs/r_Yn7pL2wE/logs?offset=100&limit=50
```

```json
{
  "run_id": "r_Yn7pL2wE",
  "total_lines": 1523,
  "offset": 100,
  "lines": [
    {"line": 101, "stream": "stdout", "content": "Epoch 1/100"},
    {"line": 102, "stream": "stdout", "content": "  loss=0.4521, val_loss=0.3892"}
  ]
}
```

## Middleware

- **CORS**: allow all origins in dev, restrict in production
- **Logging**: request method, path, status, duration
- **Recovery**: panic recovery with error response
- **RequestID**: generate unique ID per request for tracing

## Error Format

```json
{
  "error": "container not found",
  "code": "NOT_FOUND",
  "details": "no container with id c_invalid"
}
```

HTTP status codes:
- 200 OK
- 201 Created
- 400 Bad Request (validation error)
- 404 Not Found
- 409 Conflict (e.g., duplicate container name)
- 500 Internal Server Error

## Server Struct

```go
// api/server.go
type Server struct {
    store     store.Store
    executor  *executor.Executor
    monitor   *monitor.Manager
    router    *http.ServeMux
    hub       *WSHub          // WebSocket hub
}

func NewServer(store store.Store, exec *executor.Executor, mon *monitor.Manager) *Server
func (s *Server) Start(addr string) error
```

## Embedded Web UI

```go
//go:embed web/*
var webFS embed.FS

// Serve index.html at /
// Serve static files at /assets/*
```

No build step. Single HTML file with inline JS + Tailwind CDN.
