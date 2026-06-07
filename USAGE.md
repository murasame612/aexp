# aexp 使用指南

## 快速开始

```bash
# 1. 初始化（创建 ~/.aexp/ 目录和数据库）
./aexp init

# 2. 生成 SSH 密钥并部署到目标机器
ssh-keygen -t ed25519 -f ~/.aexp/id_ed25519 -N ''
ssh-copy-id -i ~/.aexp/id_ed25519.pub root@192.168.1.100

# 3. 启动服务器
./aexp serve --port 8080

# 4. 浏览器打开 http://localhost:8080 查看仪表盘
```

---

## 命令列表

### 服务管理

```bash
# 启动服务器（默认端口 8080）
./aexp serve

# 指定端口和数据库路径
./aexp serve --port 9090 --db /data/aexp.db
```

---

### 资源管理

#### 探查远程环境

在注册资源前，先看看远程机器有什么：

```bash
./aexp resource explore 192.168.1.100 --user root
```

输出示例：
```
Host:       192.168.1.100
OS:         Ubuntu 22.04.3 LTS
GPU:        1 device(s)
  [0] NVIDIA GeForce RTX 4090 (24564 MB)
Conda envs:
  - base                 /opt/conda
  - tslib                /opt/conda/envs/tslib
  - llm4ts               /opt/conda/envs/llm4ts
Python:
  - /opt/conda/envs/tslib/bin/python           Python 3.10.12 (env:tslib)
  - /opt/conda/envs/llm4ts/bin/python          Python 3.11.4 (env:llm4ts)
Workspaces:
  - /workspace                     (15 subdirs)
tmux:       0 session(s)
aexp runs:  0 active
```

这样就知道该用什么 `--root-dir` 和 `--conda-env` 了。

```bash
# JSON 输出（给 Agent 用）
./aexp resource explore 192.168.1.100 --json
```

#### 添加资源

```bash
./aexp resource add \
  --name mu-tslib \
  --host 192.168.1.100 \
  --port 22 \
  --user root \
  --root-dir /workspace \
  --conda-env tslib \
  --gpu-indices 0 \
  --tags "4090,timeseries"
```

必填参数：`--name`, `--host`, `--root-dir`

可选参数：
| 参数 | 默认值 | 说明 |
|---|---|---|
| `--type` | ssh | 资源类型 (ssh/docker/local) |
| `--port` | 22 | SSH 端口 |
| `--user` | root | SSH 用户 |
| `--conda-env` | (空) | 默认 conda 环境 |
| `--gpu-indices` | (空) | 可见 GPU 索引，如 0,1 |
| `--tags` | (空) | 逗号分隔标签 |
| `--auth-ref` | ~/.aexp/id_ed25519 | SSH 密钥路径 |

#### 列出资源

```bash
# 表格输出
./aexp resource list

# JSON 输出（给 Agent 用）
./aexp resource list --json
```

#### 删除资源

```bash
./aexp resource remove mu-tslib
```

---

### 实验运行

#### 提交实验

两种模式：

**结构化模式（默认，推荐）** — argv 精确保留：

```bash
./aexp run submit \
  --resource mu-tslib \
  --name "ECL-iTransformer-run1" \
  --cwd /workspace/Time-Series-Library \
  --conda-env tslib \
  -- python train.py --data ECL --model iTransformer --lr 0.001
```

**Shell 模式（`--shell`）** — 需要 shell 语法时：

```bash
./aexp run submit \
  --resource mu-tslib \
  --shell \
  -- 'echo start; python train.py --data ECL | tee output.log'
```

**指定 GPU：**

```bash
./aexp run submit --resource mu-tslib --gpu-index 0 -- python train.py
```

注意：`--` 之后的所有内容是要执行的命令（默认 argv 模式）。

必填参数：`--resource`, 命令部分

可选参数：
| 参数 | 默认值 | 说明 |
|---|---|---|
| `--name` | (空) | 运行名称（方便记忆） |
| `--kind` | formal | 类型: smoke/pilot/formal/ablation |
| `--gpu-index` | -1 | GPU 索引（-1 为全部） |
| `--shell` | false | Shell 模式：用 bash -lc 解释命令 |
| `--cwd` | (空) | 工作目录（相对于 root-dir 或绝对路径） |
| `--conda-env` | (空) | 覆盖资源默认的 conda 环境 |
| `--log-paths` | (空) | 日志文件 glob，如 `logs/*.log` |
| `--artifact-paths` | (空) | 产物文件 glob |
| `--metric-paths` | (空) | 指标文件 glob |

#### 列出运行

```bash
# 全部
./aexp run list

# 只看正在运行的
./aexp run list --status running

# 只看某个资源上的
./aexp run list --resource mu-tslib

# JSON 输出
./aexp run list --json
```

#### 查看运行状态

```bash
./aexp run status run_Yn7pL2wE
```

输出示例：
```
ID:        run_Yn7pL2wE
Name:      ECL-iTransformer-run1
Resource:  rsrc_muTslib
Status:    running
Command:   python train.py --data ECL --model iTransformer
CWD:       /workspace/Time-Series-Library
tmux:      aexp_run_Yn7pL2wE
Started:   2026-06-07T14:30:05+08:00
```

#### 查看日志

```bash
# 实时 tail（Ctrl+C 停止）
./aexp run logs run_Yn7pL2wE

# 只看最后 50 行
./aexp run logs run_Yn7pL2wE --last 50
```

#### 取消运行

```bash
./aexp run cancel run_Yn7pL2wE
```

---

## Web 仪表盘

启动 `./aexp serve` 后，浏览器打开 `http://localhost:8080`：

- **Dashboard**：所有资源状态 + 活跃运行
- **Resources**：资源列表，CPU/GPU/内存实时数据
- **Runs**：所有运行记录，点击查看详情和日志

WebSocket 自动连接，日志实时更新。

---

## 典型工作流

### 人手动跑实验

```bash
# 1. 先探查远程机器有什么
./aexp resource explore 192.168.1.100

# 2. 根据探查结果注册资源
./aexp resource add --name mu-tslib --host 192.168.1.100 --root-dir /workspace --conda-env tslib

# 3. 提交实验
./aexp run submit --resource mu-tslib --name "test-run" -- python train.py --epochs 10

# 4. 看日志
./aexp run logs run_xxx

# 5. 跑完了看结果
./aexp run status run_xxx
```

### Agent 自动化（JSON 模式）

```bash
# Agent 先探查环境
./aexp resource explore 192.168.1.100 --json
# → {"host":"...","gpus":[{"index":0,"name":"RTX 4090",...}],"conda_envs":[...],...}

# Agent 注册资源
./aexp resource add --name mu-tslib --host 192.168.1.100 --root-dir /workspace --conda-env tslib

# Agent 查询可用资源
./aexp resource list --json
# → [{"id":"rsrc_xxx","name":"mu-tslib","status":"idle",...}]

# Agent 提交实验
./aexp run submit --resource mu-tslib --name "exp1" -- python train.py --json
# → {"id":"run_xxx","status":"running",...}

# Agent 轮询状态
./aexp run status run_xxx --json
# → {"status":"succeeded","exit_code":0,...}
```

---

## 数据存储

- 数据库：`~/.aexp/aexp.db`（SQLite，可直接用 sqlite3 查看）
- 日志：远程机器上 `<root-dir>/.aexp/runs/<run_id>/logs/`

```bash
# 直接查数据库
sqlite3 ~/.aexp/aexp.db "SELECT id, name, status FROM runs;"
sqlite3 ~/.aexp/aexp.db "SELECT * FROM agent_events ORDER BY timestamp DESC LIMIT 10;"
```
