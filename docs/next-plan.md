# aexp Next Plan

目标：让 agent 像使用 SSH 一样自然地操作远端实验，但把 SSH 时代最容易出错的隐性上下文固化下来。

当前定位：

```text
aexp exec        = 超短 SSH 指令，适合 10-30 秒内的检查命令
aexp run submit  = 长时间 SSH 任务，适合 setup、数据准备、训练、评估
aexp project     = 项目级可复用 recipe，像 repo 里的 aexp Makefile
```

核心原则：

- `project` 只保存“怎么跑这个项目”，不要变成新的训练框架。
- 实验参数放在项目自己的 `configs/` 或 `scripts/`，不要塞进 `.aexp.yaml`。
- `run` 只记录“这次实际跑了什么”，包括 resolved cwd/env/python/gpu/logs/metrics/events。
- `setup/smoke` 永远不是正式实验结果。
- 凡是 agent 需要反复记忆的命令细节，都应该收敛成默认值、doctor 建议或 project recipe。

## P0

### 1. `aexp project init`

现在 `project` 已经能读取 `.aexp.yaml`，但 agent 仍然要手写配置。下一步需要一键生成项目模板。

建议命令：

```bash
aexp project init --resource mu --cwd /path/to/project --env auto
```

生成示例：

```yaml
resource: mu
cwd: /path/to/project
env: auto
default_gpu: 0

logs:
  - logs/**/*.log
metrics:
  - runs/**/*.csv
  - results/**/*.json

sync:
  source: ./
  target: /path/to/project
  profile: code

setup:
  command: python -m pip install -r requirements.txt
  kind: setup

train:
  command: bash scripts/train.sh configs/experiments/default.yaml
  kind: formal
```

验收标准：

- 没有 `.aexp.yaml` 时能创建。
- 已存在 `.aexp.yaml` 时默认拒绝覆盖，支持 `--force`。
- 能根据项目文件给出初始猜测：`requirements.txt`、`pyproject.toml`、`scripts/train*.sh`、`configs/`。
- 输出下一步命令：

```bash
aexp project doctor
aexp project run setup --dry-run
aexp project run train --dry-run
```

### 2. run 状态一致性

现在最大信任问题是 `run status`、`run list`、GPU lock 可能不同步。必须让 running 状态在展示和锁判断前被刷新。

建议实现：

- `aexp run refresh <id>`
- `aexp run refresh --resource mu`
- `aexp run list` 对 `running/starting` 做轻量 refresh，或明确标注 `cached`。
- GPU lock 判断前先刷新相关 running runs。

验收标准：

- 一个已结束的 tmux run 不再长期显示 running。
- `run status <id>` 与 `run list` 对同一 run 的状态一致。
- 已 finished run 不能被 cancel，提示 `run already finished`。
- lock 不会因为 stale running run 卡住新任务。

### 3. exec 持久化连接

`exec` 的定位是短 SSH，但每次 CLI 新进程都会重新建连接。要接近 SSH 手感，需要让 CLI 优先走本地 daemon/API 复用 SSH pool。

建议实现：

```text
aexp serve 常驻 SSH pool
aexp exec 默认尝试本地 API
本地 API 不可用时 fallback 直连
```

验收标准：

- 同一 resource 连续执行短命令明显变快。
- `aexp exec --direct` 可强制不用 daemon。
- daemon 不可用时错误清楚，不把 API 失败误报成远端命令失败。
- 长任务仍然提示使用 `aexp run submit` 或 `aexp project run`。

### 4. 事件输出 helper

现在 `$AEXP_UI_EVENTS` 虽然可用，但 agent 还要记 JSONL 格式。应该提供固定 helper。

建议命令：

```bash
aexp-event metric train/loss 0.23 --epoch 3
aexp-event metric val/mAP50 0.61 --epoch 10
aexp-event progress train 30 --total 100
aexp-event note "finished validation"
```

Python helper：

```python
from aexp_events import metric, progress, param, note

param("model", "yolov8s")
metric("train/loss", loss, epoch=epoch)
progress("train", epoch, total=epochs)
```

验收标准：

- helper 自动读取 `$AEXP_UI_EVENTS`。
- 未设置 `$AEXP_UI_EVENTS` 时安静 no-op，或者给一次清晰 warning。
- JSONL schema 与前端通用 dashboard 兼容。
- `aexp project init` 生成的训练模板能展示 helper 用法。

## P1

### 5. project doctor 成为下一步命令生成器

`project doctor` 不应只输出 OK/FAIL，还应该告诉 agent 现在该做什么。

建议输出：

```text
project: /path/to/project
resource: mu
env: auto -> .venv

recipes:
  setup: setup, not evidence
  train: formal, experiment evidence

recommended:
  aexp project sync
  aexp project run setup
  aexp project run train --dry-run
```

失败时输出修复命令：

```text
cwd missing on remote
Use:
  aexp project sync --dry-run
  aexp project sync
```

验收标准：

- 能识别 recipe 的 `kind`，并明确 setup/smoke 不是正式结果。
- 能检查 `.aexp.yaml` 中的 `resource/cwd/env/sync/train` 是否完整。
- 失败时给可复制的下一步命令。

### 6. project profile cache

`--project-env auto` 不应每次重新探测。应该缓存 `resource + cwd` 的环境解析结果。

建议行为：

- 默认复用最近成功 profile。
- `--refresh-env` 强制重新探测。
- profile 中保存 `resolved_env/resolved_python/resolved_cwd/torch/cuda/warnings`。

验收标准：

- 同一个 project 的连续 submit 不重复做慢探测。
- run 记录仍保存本次使用的 resolved 结果快照。
- profile 过期或路径失效时 doctor 给出明确提示。

### 7. resource 控制通道状态

resource 不能只显示 idle/busy，还要显示控制通道是否可用。

建议字段：

```text
ssh_status: ok / failed
last_doctor_error
last_checked_at
last_success_at
```

验收标准：

- 资源卡片能显示 “idle but SSH failed”。
- 最近一次 SSH/SOCKS/ProxyCommand 错误能在 UI 里看到。
- agent 不会因为 `idle` 误以为一定能提交任务。

### 8. UI 多 run 比较

单 run 指标图已经有了，但论文实验更需要横向比较。

建议能力：

- 选择多个 formal/ablation runs。
- 同一 metric 同图比较。
- 默认隐藏 setup/smoke。
- legend 使用 run name，hover 显示 run id/resource/kind。

验收标准：

- 能比较 IR/VIS/Fusion 的 `mAP50-95`。
- 能比较多个 loss 曲线。
- 图表数据来自通用 events/metrics，不写死 YOLO。

## P2

### 9. project sync 打磨

`project sync` 已有基础，但还需要更像项目命令。

建议能力：

- `.aexpignore`
- `--dry-run` 默认展示 excludes 来源。
- `sync.profile` 支持 `code/code-data/all`。
- 远端缺 rsync 时给明确修复建议。

验收标准：

- ML 项目默认不传 `.venv`、cache、训练输出。
- 不误排除用户明确想同步的数据目录。
- dry-run 输出足够让 agent 判断是否会传错目录。

### 10. 文档和示例项目模板

需要一个标准 ML 项目示例，减少 agent 读 help 的成本。

建议目录：

```text
examples/python-ml/
  .aexp.yaml
  scripts/train.sh
  configs/experiments/default.yaml
  aexp_event.py
```

验收标准：

- README 中能用 5 条命令跑完整流程：

```bash
aexp project init
aexp project doctor
aexp project sync --dry-run
aexp project run setup
aexp project run train
```

## 建议 Goal

可以把长期 goal 写成：

```text
持续优化 aexp，让 agent 能用 project/exec/run 三层语义低摩擦地完成远端实验：
exec 只做短 SSH 检查，run 负责长任务事实记录，project 负责项目级 recipe 复用。
按 docs/next-plan.md 的 P0 -> P1 -> P2 顺序推进，每完成一项要测试、编译、替换本机 aexp，并拆 commit 提交。
```

## 当前完成状态

- `aexp project init`：已实现，支持 dry-run、覆盖保护、requirements/pyproject/uv/train scripts/configs 初始猜测。
- `aexp project run <recipe>`：已实现。
- `aexp project sync`：已实现基础版本。
- `aexp run refresh <id|--resource>`：已实现。
- `aexp run list` active run 自动刷新与 cached 标注：已实现。
- GPU lock 前 active run 刷新：已实现。
- finished run cancel 拒绝：已实现，并有 executor 单测覆盖。
- `aexp exec` 本地 API fast path：已实现，默认尝试 `aexp serve` 复用 SSH pool，不可达时 fallback 直连，支持 `--direct`。
- `aexp event` / `aexp-event` structured events helper：已实现，支持 metric/progress/param/note，自动写 `$AEXP_UI_EVENTS`，并在 project init 模板中展示用法。
- `.aexp.yaml` 多行 `command: |`：已实现。
- `project run --dry-run` 清晰展开：已实现。
- `setup` 默认 no-gpu：已实现。
- Runs UI 默认隐藏 setup/smoke：已实现。
- 通用 events dashboard：已实现基础版本。
- 通用 metric group charts：已实现基础版本。
- `project doctor` 下一步命令生成器：已实现，JSON/CLI 均包含 recipes、recommended commands、缺失 cwd/sync/recipe 的修复建议。
- project profile cache：已实现，`run submit` / `project run` / `exec` 的 `--project-env auto` 默认复用可用缓存，`--refresh-env` 可强制重新探测，run 记录仍保存 resolved 快照。
- resource 控制通道状态：已实现，resource 记录保存 `ssh_status/last_doctor_error/last_checked_at/last_success_at`，doctor/project doctor 会更新，CLI/UI 能显示 idle 但 SSH failed。
- UI 多 run 比较：已实现，Runs 表可选择 formal/ablation 且带 UI events 的 run，一次性拉取 events 后按同名 metric 同图比较，legend 使用 run name 并保留 run id/resource/kind 元信息。

优先继续：project sync 打磨、文档和示例项目模板。
