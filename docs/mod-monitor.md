# mod-monitor — Resource Monitoring

## What We Monitor

Per container, polled every 10 seconds:

| Metric | Source | Notes |
|---|---|---|
| CPU % | `/proc/stat` or `top -bn1` | overall usage |
| Memory used/total | `/proc/meminfo` | in MB |
| GPU utilization | `nvidia-smi` | per GPU index |
| GPU memory | `nvidia-smi` | used/total per GPU |
| Load average | `/proc/loadavg` | 1m, 5m, 15m |
| Disk usage (workspace) | `df <workspace>` | optional, Phase 2 |

## Polling Architecture

```
Monitor (goroutine per container)
  |
  ticker 10s
  |
  SSH exec: quick probe script
  |
  parse output
  |
  save to DB (resource_snapshots table)
  |
  push to WebSocket subscribers (if any)
```

## Probe Script

Execute via SSH, single script for all metrics:

```bash
#!/bin/bash
# aexp collects all metrics in one shot

echo "---CPU---"
grep 'cpu ' /proc/stat

echo "---MEM---"
grep -E 'MemTotal|MemAvailable' /proc/meminfo

echo "---LOAD---"
cat /proc/loadavg

echo "---GPU---"
nvidia-smi --query-gpu=index,name,utilization.gpu,memory.used,memory.total \
  --format=csv,noheader,nounits 2>/dev/null || echo "no-gpu"

echo "---DISK---"
df -BM <workspace> 2>/dev/null | tail -1
```

Single SSH roundtrip. Parse the tagged sections in Go.

## ResourceSnapshot Model

```go
type ResourceSnapshot struct {
    ContainerID string    `json:"container_id"`
    Timestamp   time.Time `json:"timestamp"`
    CPUPercent  float64   `json:"cpu_percent"`
    MemUsedMB   float64   `json:"mem_used_mb"`
    MemTotalMB  float64   `json:"mem_total_mb"`
    Load1m      float64   `json:"load_1m"`
    Load5m      float64   `json:"load_5m"`
    Load15m     float64   `json:"load_15m"`
    GPUs        []GPUInfo `json:"gpus"`
}

type GPUInfo struct {
    Index    int     `json:"index"`
    Name     string  `json:"name"`
    Util     float64 `json:"util"`       // 0-100%
    MemUsed  float64 `json:"mem_used"`   // MB
    MemTotal float64 `json:"mem_total"`  // MB
}
```

## CPU Calculation

`/proc/stat` gives cumulative jiffies. Need two samples to calculate %:

```
cpu  user nice system idle iowait irq softirq steal
```

```
idle_delta = idle2 - idle1
total_delta = total2 - total1
cpu_percent = (1 - idle_delta / total_delta) * 100
```

For MVP: just use `top -bn1 | head -5` and parse the `%Cpu(s)` line.
Simpler, one-shot, good enough.

## GPU Parsing

nvidia-smi output:
```
0, NVIDIA GeForce RTX 4090, 45, 8192, 24564
1, NVIDIA GeForce RTX 4090, 0, 128, 24564
```

Parse into `GPUInfo` slice. If nvidia-smi not available (no GPU), return empty slice.

## Monitoring Manager

```go
// monitor/monitor.go
type Manager struct {
    store      store.Store
    pool       *executor.SSHPool
    interval   time.Duration  // default 10s
    ctx        context.Context
    cancel     context.CancelFunc
}

func NewManager(store store.Store, pool *executor.SSHPool, interval time.Duration) *Manager

func (m *Manager) Start()                                    // start all goroutines
func (m *Manager) Stop()                                     // graceful shutdown
func (m *Manager) PollContainer(ctx context.Context, c *store.Container) (*store.ResourceSnapshot, error)
```

On `Start()`:
1. Load all containers from DB
2. Start one goroutine per container
3. Each goroutine: poll on ticker, save snapshot, retry on error

When a new container is added, start its goroutine.
When a container is removed, cancel its goroutine.

## WebSocket Push

When a new snapshot is saved, broadcast to all WebSocket subscribers watching that container:

```
ws://localhost:8080/ws/resources?container=dams-tlib-0

{
  "type": "resource_update",
  "container_id": "c_Ab3xK9mQ",
  "timestamp": "2026-06-07T14:30:10Z",
  "cpu_percent": 67.3,
  "mem_used_mb": 12800,
  "mem_total_mb": 64000,
  "gpus": [
    {"index": 0, "util": 45, "mem_used": 8192, "mem_total": 24564}
  ]
}
```

## Error Handling

If SSH fails during polling:
- Log the error
- Set container status to `error`
- Retry after interval (don't backoff — 10s is already slow)
- On next successful poll, set status back to `idle` or `busy` (based on GPU util > 5%)

## Container Status Derivation

```
if any GPU util > 5%:
    status = "busy"
else if runs with status=running exist:
    status = "busy"
else:
    status = "idle"

if SSH unreachable:
    status = "error"
```
