# aexp 使用指南

`aexp` 是本地调度器：命令在本机运行，通过 SSH 调度到已注册的 resource。远程机器不需要安装 `aexp`，只需要 SSH、tmux 和你的训练运行环境。

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
# 启动服务器（默认端口 8080，只监听本机 127.0.0.1）
./aexp serve

# 指定端口和数据库路径
./aexp serve --port 9090 --db /data/aexp.db

# 后台运行，日志写到 ~/.aexp/aexp.log
./aexp serve --daemon

# 暴露给其他机器访问时，远程请求仍需要 API token
./aexp serve --host 0.0.0.0 --port 8080

# 本机 localhost 也强制要求 API token
./aexp serve --require-token-local
```

默认情况下，来自 `localhost` / `127.0.0.1` / `::1` 的 API 和 WebSocket 请求不需要填写 token；非本机访问仍然需要启动时打印的 API token。这样本机浏览器使用不别扭，同时不会把无 token API 暴露到局域网。

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
| `--conda-base` | (空) | Conda/Miniforge base prefix，如 `/home/user/miniforge3` |
| `--conda-init` | (空) | conda 初始化脚本；通常由 `conda_base/etc/profile.d/conda.sh` 推导 |
| `--conda-env` | (空) | 默认 conda 环境 |
| `--gpu-indices` | (空) | 可见 GPU 索引，如 0,1 |
| `--tags` | (空) | 逗号分隔标签 |
| `--auth-ref` | ~/.aexp/id_ed25519 | SSH 密钥路径 |
| `--socks-proxy` | (空) | SOCKS5 代理 (host:port) |
| `--proxy-command` | (空) | SSH ProxyCommand (如 `'nc -X 5 -x host:port %h %p'`) |

#### 通过代理连接资源

```bash
# SOCKS5 代理
./aexp resource add \
  --name gpu-server \
  --host 10.0.0.5 \
  --root-dir /workspace \
  --socks-proxy proxy.example.com:1080

# SSH ProxyCommand (如通过跳板机)
./aexp resource add \
  --name gpu-server \
  --host 10.0.0.5 \
  --root-dir /workspace \
  --proxy-command "nc -X 5 -x member.aicloud.szu.edu.cn:30027 %h %p"
```

#### 列出资源

```bash
# 表格输出
./aexp resource list

# JSON 输出（给 Agent 用）
./aexp resource list --json

# 详细表格：显示 root_dir / conda / GPU 配置
./aexp resource list --verbose
```

#### 更新资源

`resource update` 只修改显式传入的字段；传空字符串可以清空字段。

```bash
# 给已有 mu 补 conda/默认环境
./aexp resource update mu \
  --conda-base /home/murasame/miniforge3 \
  --conda-env ts-baseline

# 单独改默认环境
./aexp resource update mu --conda-env llm4ts

# 清空默认环境
./aexp resource update mu --conda-env ''
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

**强制提交（跳过 slot lock）：**

```bash
# 同一 resource 上已有 running run 时仍可提交（如 CPU-only 任务）
./aexp run submit --resource mu-tslib --force -- python preprocess.py
```

注意：`--` 之后的所有内容是要执行的命令（默认 argv 模式）。**所有 flag 必须写在 `--` 之前**。

必填参数：`--resource`, 命令部分

可选参数：
| 参数 | 默认值 | 说明 |
|---|---|---|
| `--name` | (空) | 运行名称（方便记忆） |
| `--kind` | formal | 类型: smoke/pilot/formal/ablation |
| `--gpu-index` | -1 | GPU 索引（-1 为全部） |
| `--force` | false | 跳过 GPU slot lock，允许同 resource/GPU 并发提交 |
| `--shell` | false | Shell 模式：用 bash -lc 解释命令 |
| `--cwd` | (空) | 工作目录（相对于 root-dir 或绝对路径） |
| `--conda-env` | (空) | 覆盖资源默认的 conda 环境 |
| `--log-paths` | (空) | 日志文件 glob，如 `logs/*.log` |
| `--artifact-paths` | (空) | 产物文件 glob |
| `--metric-paths` | (空) | 指标文件 glob |

`--cwd` 是 aexp 管理层的工作目录约束，必须落在 resource 的 `root_dir` 下；它不是远程 shell 的强安全沙箱。命令里显式写 `cd /other/path && ...` 仍由远程 shell 执行，但这会绕开 aexp 对工作目录的结构化理解。推荐把项目目录注册为 resource `root_dir`，然后用 `--cwd` 指向其中的项目或子目录。

`--log-paths`、`--artifact-paths`、`--metric-paths` 当前作为 run 元数据保存，建议写相对项目/运行目录的 glob，例如 `logs/**/*.log`、`results/**/*.json`。后续采集器应按 resource `root_dir` 和 run `cwd` 解释这些路径；现在不要把它们当作已经完成的文件管理/下载功能。

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

#### 远程执行（运维/检查命令）

`aexp exec` 在已注册的 resource 上执行一次性命令，**不创建 Run**。用于检查环境、查看文件、诊断问题。

命令语义：
- `--` 后只有一个参数时，它就是远程 shell command string。
- `--` 后有多个参数时，默认按 argv 风格逐个 shell-quote，适合 `bash -lc '...'` 这类调用。
- 需要把多个参数原样拼成 shell 语法时，加 `--shell`。

```bash
# 查看 GPU 状态
./aexp exec --resource mu -- nvidia-smi

# bash -lc 形式会按 argv 安全拼接
./aexp exec --resource mu -- bash -lc 'cd /workspace/project && python script.py'

# 查看目录内容
./aexp exec --resource mu --cwd /workspace -- 'ls -la outputs/ | head'

# 多参数 shell 语法
./aexp exec --resource mu --shell -- echo start '&&' nvidia-smi

# JSON 输出（供 agent 解析）
./aexp exec --resource mu --json -- 'du -sh /workspace/.aexp/runs/*'

# 自定义超时
./aexp exec --resource mu --timeout 120 -- 'find /workspace -name "*.pt" | wc -l'
```

安全限制：
- `validateCommand` 拦截 `rm -rf /` 等危险模式
- `--cwd` 受 root_dir sandbox 约束
- 默认 30s 超时，最大 300s
- 输出截断到 1 MiB
- 所有执行写入 agent_events 审计日志

---

## 远程项目实验黄金路径

推荐把 resource 的 `root_dir` 设成项目根目录，避免 `--cwd` 越界，也让日志、指标和产物路径更好解释。

```bash
# 1. 注册项目所在机器/目录
./aexp resource add \
  --name szu-bridge \
  --host szu.example.com \
  --user root \
  --root-dir /share/home/user/project \
  --conda-env tslib \
  --gpu-indices 0

# 2. 确认 root_dir / conda / GPU 配置
./aexp resource list --verbose

# 3. 提交正式实验
./aexp run submit \
  --resource szu-bridge \
  --name downstream-bridge-v1 \
  --kind formal \
  --cwd /share/home/user/project \
  --conda-env tslib \
  --gpu-index 0 \
  --metric-paths 'results/**/*.json' \
  --log-paths 'logs/**/*.log' \
  --artifact-paths 'checkpoints/**/*' \
  --shell -- 'bash scripts/run.sh'

# 4. 追踪状态和日志
./aexp run status run_xxx
./aexp run logs run_xxx --last 100
```

如果项目已经在 `/share/...`，但 resource 误注册成 `--root-dir /root`，不要硬塞 `--cwd /share/...`。更好的做法是把 resource 的 root_dir 改成项目根目录，或重新注册一个更准确的 resource。

---

## Web 仪表盘

启动 `./aexp serve` 后，浏览器打开 `http://localhost:8080`：

- **Dashboard**：所有资源状态 + 活跃运行
- **Resources**：资源列表，CPU/GPU/内存实时数据
- **Runs**：所有运行记录，点击查看详情和日志

本机访问默认不需要填写 API token。WebSocket 自动连接，日志实时更新；如果从非本机访问，需要在页面右上角填入启动时打印的 token。

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
