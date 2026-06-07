# mod-resource.md — 异构计算资源管理

## 设计原则

资源不只是容器。aexp 的资源可以是：

| type | 说明 | 连接方式 |
|---|---|---|
| `ssh` | 任何 SSH 可达的机器 | SSH |
| `docker` | Docker 容器（在某台宿主机上） | SSH 到宿主机 + docker exec |
| `local` | 本机 | 本地 exec |
| `slurm` | Slurm 集群节点 | SSH + sbatch/srun |
| `k8s` | Kubernetes Pod | kubectl exec |

MVP 只实现 `ssh`，其余 type 保留 schema 兼容。

## 资源注册

### CLI

```bash
aexp resource add \
  --name mu-tslib \
  --type ssh \
  --host 192.168.1.100 \
  --port 22 \
  --user root \
  --root-dir /workspace \
  --conda-env tslib \
  --gpu-indices 0 \
  --tags "4090,timeseries,dam"
```

### API

```
POST /api/v1/resources
{
  "name": "mu-tslib",
  "type": "ssh",
  "host": "192.168.1.100",
  "port": 22,
  "user": "root",
  "root_dir": "/workspace",
  "conda_env": "tslib",
  "gpu_indices": "0",
  "tags": "4090,timeseries,dam"
}
```

## SSH 连接池

每个 resource 维护一个持久 SSH 连接，避免每次操作都重连。

```go
// internal/executor/ssh_pool.go
type SSHPool struct {
    conns   map[string]*ssh.Client  // resource_id -> client
    mu      sync.RWMutex
    keyPath string
}

func (p *SSHPool) Get(ctx context.Context, r *Resource) (*ssh.Client, error)
func (p *SSHPool) Close(resourceID string) error
func (p *SSHPool) CloseAll() error
```

连接复用策略：
- 取连接前发 keepalive，失败则重连
- 空闲连接 5 分钟超时自动关闭
- 连接错误时设置 resource status 为 `unreachable`

## SSH 认证

优先级：
1. 指定的 key file（`--auth-ref` 或 config）
2. `~/.aexp/id_ed25519`（`aexp init` 生成）
3. `~/.ssh/id_rsa`（系统默认）

首次使用：
```bash
aexp init
# 生成密钥对 ~/.aexp/id_ed25519
# 打印公钥，用户手动部署到目标机器
```

## 健康检查

连接测试（`aexp resource test <name>`）：

```bash
# SSH 连通性
ssh -o ConnectTimeout=5 <host> echo ok

# conda 环境是否存在
ssh <host> "source /opt/conda/etc/profile.d/conda.sh && conda env list | grep <env>"

# GPU 是否可用
ssh <host> "nvidia-smi --query-gpu=index,memory.total --format=csv,noheader"

# workspace 是否存在
ssh <host> "test -d <root_dir> && echo ok"
```

## 资源列表输出

### 人看（默认）

```
NAME         TYPE   HOST              GPU   STATUS   CPU    MEM       GPU_MEM
mu-tslib     ssh    192.168.1.100     0     idle     23%    45%       2.1/24G
szu-exp      ssh    192.168.1.200     0     busy     78%    67%       18/24G
```

### Agent 看（`--json`）

```json
[
  {
    "id": "rsrc_muTslib001",
    "name": "mu-tslib",
    "type": "ssh",
    "host": "192.168.1.100",
    "status": "idle",
    "gpu_indices": "0",
    "conda_env": "tslib",
    "tags": ["4090", "timeseries", "dam"],
    "latest_snapshot": {
      "cpu_percent": 23.1,
      "mem_used_mb": 28800,
      "mem_total_mb": 64000,
      "gpus": [{"index": 0, "util": 5, "mem_used": 2100, "mem_total": 24564}]
    }
  }
]
```
