# mod-container — Container Management

## What is a "Container"?

In aexp, a container is any SSH-accessible compute environment. It could be:
- A Docker container on a GPU server
- A bare-metal server
- A cloud VM
- A tmux session on a shared machine

The only requirement: `aexp` can SSH into it.

## Container Registration

### CLI

```bash
aexp container add \
  --name dam-tslib-0 \
  --host 192.168.1.100 \
  --port 22 \
  --user root \
  --workspace /workspace \
  --conda-env tslib \
  --gpu-indices 0 \
  --tags "dam,timeseries,4090"
```

### API

```
POST /api/v1/containers
{
  "name": "dam-tslib-0",
  "host": "192.168.1.100",
  "port": 22,
  "user": "root",
  "workspace": "/workspace",
  "conda_env": "tslib",
  "gpu_indices": "0",
  "tags": "dam,timeseries,4090"
}
```

## SSH Connection

Each container stores SSH connection params. `aexp` connects using:

1. SSH key from `~/.ssh/id_rsa` (default) or specified key file
2. Password auth supported but discouraged
3. Connection pooling — reuse connections, don't reconnect per command

### SSH Client Pool

```go
// executor/ssh_pool.go
type SSHPool struct {
    conns map[string]*ssh.Client  // container_id -> client
    mu    sync.RWMutex
}

func (p *SSHPool) Get(ctx context.Context, container *Container) (*ssh.Client, error)
func (p *SSHPool) Close(containerID string) error
func (p *SSHPool) CloseAll() error
```

Connection health check: before reusing a connection, send a keepalive.
If it fails, reconnect.

## Container Status Check

When listing containers, `aexp` SSHes in and runs quick probes:

```bash
# CPU + Memory
cat /proc/stat /proc/meminfo | head -20

# GPU (nvidia-smi)
nvidia-smi --query-gpu=index,name,utilization.gpu,memory.used,memory.total --format=csv,noheader

# Running aexp sessions
tmux list-sessions 2>/dev/null | grep aexp_
```

This is done periodically (every 10s by the resource poller) and on-demand.

## SSH Key Configuration

First run: `aexp init` generates a keypair at `~/.aexp/id_ed25519` and prints
the public key for the user to deploy to containers.

```bash
aexp init
# Generates keypair
# Prints: ssh-ed25519 AAAA... aexp@hostname
# User copies this to container's ~/.ssh/authorized_keys

aexp container add --name ... --ssh-key ~/.aexp/id_ed25519
```

Or use existing `~/.ssh/id_rsa` by default.
