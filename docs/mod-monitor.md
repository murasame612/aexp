# mod-monitor — 资源监控

## 监控对象

每个 resource 每 10 秒轮询一次：

| 指标 | 来源 | 说明 |
|---|---|---|
| CPU % | `top -bn1` | 整体使用率 |
| 内存 used/total | `/proc/meminfo` | MB |
| GPU 利用率 | `nvidia-smi` | 每块 GPU |
| GPU 显存 | `nvidia-smi` | used/total per GPU |
| 负载 | `/proc/loadavg` | 1m, 5m, 15m |

## 轮询架构

```
Monitor Manager
  │
  ├── goroutine: mu-tslib  ──► ticker 10s ──► SSH probe ──► save snapshot
  ├── goroutine: szu-exp   ──► ticker 10s ──► SSH probe ──► save snapshot
  └── goroutine: ...       ──► ...
```

## 探测脚本

单次 SSH 执行，收集所有指标：

```bash
#!/bin/bash
echo "---CPU---"
top -bn1 | grep '%Cpu' | head -1

echo "---MEM---"
grep -E 'MemTotal|MemAvailable' /proc/meminfo

echo "---LOAD---"
cat /proc/loadavg

echo "---GPU---"
nvidia-smi --query-gpu=index,name,utilization.gpu,memory.used,memory.total \
  --format=csv,noheader,nounits 2>/dev/null || echo "no-gpu"

echo "---DISK---"
df -BM ROOT_DIR_PLACEHOLDER 2>/dev/null | tail -1
```

单次 SSH 往返，Go 端按 `---TAG---` 分段解析。

## Snapshot 模型

```go
type Snapshot struct {
    ResourceID string    `json:"resource_id"`
    RunID      string    `json:"run_id,omitempty"`
    Timestamp  time.Time `json:"timestamp"`
    CPUPercent float64   `json:"cpu_percent"`
    MemUsedMB  float64   `json:"mem_used_mb"`
    MemTotalMB float64   `json:"mem_total_mb"`
    Load1m     float64   `json:"load_1m"`
    Load5m     float64   `json:"load_5m"`
    Load15m    float64   `json:"load_15m"`
    GPUs       []GPUInfo `json:"gpus"`
    DiskJSON   string    `json:"disk_json,omitempty"`
}

type GPUInfo struct {
    Index    int     `json:"index"`
    Name     string  `json:"name"`
    Util     float64 `json:"util"`       // 0-100%
    MemUsed  float64 `json:"mem_used"`   // MB
    MemTotal float64 `json:"mem_total"`  // MB
}
```

## GPU 解析

nvidia-smi 输出：
```
0, NVIDIA GeForce RTX 4090, 45, 8192, 24564
1, NVIDIA GeForce RTX 4090, 0, 128, 24564
```

解析为 `[]GPUInfo`。nvidia-smi 不可用时返回空 slice。

## Monitor Manager

```go
type Manager struct {
    store    store.Store
    pool     *executor.SSHPool
    interval time.Duration
    ctx      context.Context
    cancel   context.CancelFunc
}

func NewManager(store store.Store, pool *executor.SSHPool, interval time.Duration) *Manager
func (m *Manager) Start()                                               // 启动所有 goroutine
func (m *Manager) Stop()                                                // 优雅关闭
func (m *Manager) PollResource(ctx context.Context, r *store.Resource) (*store.Snapshot, error)
```

`Start()`：
1. 从 DB 加载所有 resource
2. 每个 resource 启动一个 goroutine
3. 每个 goroutine：ticker 轮询，保存快照，出错重试

新增 resource 时启动其 goroutine；删除时取消。

## WebSocket 推送

新快照保存后，广播给关注该 resource 的 WebSocket 订阅者：

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

## 错误处理

SSH 失败时：
- 记录错误日志
- 设置 resource status 为 `unreachable`
- 不退避（10s 已经够慢）
- 下次轮询成功时恢复 status

## Resource Status 推导

**注意：resource status 只反映资源健康度，不反映 run 状态。**

```
if SSH 不可达:
    status = "unreachable"
elif 任何 GPU util > 5%:
    status = "busy"
elif 有 running 状态的 run:
    status = "busy"
else:
    status = "idle"
```

run 的状态判定规则仍由 executor 负责；monitor 包中的 active-run reconciler 会按
resource 批量、限并发地触发这些判定。探测失败只把 observation 标成 stale/error，
不会凭缓存伪造新的远端事实。
