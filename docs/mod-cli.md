# mod-cli — CLI 接口

## 命令结构

```
aexp <resource> <action> [flags] [args]
```

## 命令

### 服务

```bash
aexp serve [--port 8080] [--db ~/.aexp/aexp.db]
```

### 初始化

```bash
aexp init
```

首次运行：
- 创建 `~/.aexp/` 目录
- 生成 SSH 密钥对 `~/.aexp/id_ed25519`（如不存在）
- 创建空 SQLite 数据库
- 打印公钥，用户手动部署到目标机器

### 资源管理

```bash
# 添加
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

# 列表
aexp resource list
# NAME         TYPE   HOST              GPU   STATUS   CPU    MEM       GPU_MEM
# mu-tslib     ssh    192.168.1.100     0     idle     23%    45%       2.1/24G
# szu-exp      ssh    192.168.1.200     0     busy     78%    67%       18/24G

# 详情
aexp resource status mu-tslib

# 测试连接
aexp resource test mu-tslib

# 更新
aexp resource update mu-tslib --conda-env llm4ts

# 删除
aexp resource remove mu-tslib
```

### Run 操作

```bash
# 提交
aexp run submit \
  --resource mu-tslib \
  --name "ECL-iTransformer-run1" \
  --cwd /workspace/Time-Series-Library \
  --conda-env tslib \
  --log-paths "logs/*.log,results/*.json" \
  -- python train.py --data ECL --model iTransformer --features M
# Submitted run run_Yn7pL2wE on mu-tslib

# 列表
aexp run list [--status running] [--resource mu-tslib]
# RUN_ID        NAME                 RESOURCE   STATUS      DURATION
# run_Yn7pL2wE  ECL-iTransformer..   mu-tslib   running     12:34
# run_Km3qPx2w  Weather-Transformer  szu-exp    running     05:21
# run_Px2wN7mk  ILI-36dim-iTrans..   mu-tslib   succeeded   45:12

# 状态
aexp run status run_Yn7pL2wE

# 日志（tail -f）
aexp run logs run_Yn7pL2wE
# Ctrl+C 停止

# 日志（最后 N 行）
aexp run logs run_Yn7pL2wE --last 100

# 取消
aexp run cancel run_Yn7pL2wE
```

### 快捷方式

```bash
# 提交 + 自动 tail 日志
aexp exec mu-tslib -- python train.py --data ECL
```

## 输出格式

默认：人可读表格。
`--json`：机器可读 JSON。

```bash
aexp resource list --json
aexp run list --json
aexp run status run_Yn7pL2wE --json
```

**表格给人看，JSON 给 Agent 看。**

## 退出码

- 0: 成功
- 1: 通用错误
- 2: 未找到
- 3: 连接错误

## 配置文件

`~/.aexp/config.yaml`：

```yaml
server:
  port: 8080
  host: 0.0.0.0

database:
  path: ~/.aexp/aexp.db

ssh:
  key: ~/.aexp/id_ed25519
  timeout: 10s

monitor:
  interval: 10s

defaults:
  conda_init: "source /opt/conda/etc/profile.d/conda.sh"
```
