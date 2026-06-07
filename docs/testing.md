# testing.md — 编译与临时测试环境

本文说明如何从当前源码编译 `aexp` 二进制，以及如何使用临时 smoke 测试环境验证 SSH ProxyCommand、远程 tmux 执行、日志、状态刷新、取消和审计事件。

注意：所有 smoke 测试都只是功能连通性检查，**不要把 smoke test 当成真实实验结果**。

## 1. 编译二进制

在仓库根目录执行：

```bash
go test ./...
go vet ./...
go build -o ./aexp ./cmd/aexp
```

这会把当前工作区源码编译成仓库根目录下的 `./aexp`。

如果只想临时测试，不覆盖仓库里的 `./aexp`，可以编译到 `/tmp`：

```bash
go build -o /tmp/aexp-verify ./cmd/aexp
```

确认二进制可用：

```bash
./aexp --version
/tmp/aexp-verify --version
```

## 2. 临时 smoke 测试脚本

仓库提供了脚本：

```bash
scripts/smoke_proxy.sh
```

默认行为：

- 从当前源码重新编译一个测试二进制：`/tmp/aexp-smoke-bin`
- 创建临时 HOME / 临时 SQLite DB：`/tmp/aexp-smoke-home-XXXXXX/.aexp/aexp.db`
- 通过 SSH ProxyCommand 连接临时容器
- 注册一个临时 resource
- 运行三类 smoke run：
  - argv 模式：验证参数里的空格、单引号、分号不会被 shell 破坏
  - `--shell` 模式：验证 shell 语义和 `CUDA_VISIBLE_DEVICES`
  - cancel 模式：验证远程 tmux run 能被取消
- 检查 agent event 审计记录
- 检查远程 `~/.aexp/wrapper.version`

直接运行：

```bash
scripts/smoke_proxy.sh
```

脚本成功时会输出类似：

```text
SMOKE OK
tmp_home: /tmp/aexp-smoke-home-a0unGJ
resource: smoke-a200-191300
argv_run: run_cJO3r7H53paF
shell_run: run_jrFbM6iTfn53
cancel_run: run_oO6R3YNW13RX
```

其中 `tmp_home` 就是这次临时测试环境的位置。

## 3. 指定临时容器

临时容器地址经常变化，可以用环境变量覆盖：

```bash
AEXP_HOST=a20049811505541120898398 scripts/smoke_proxy.sh
```

默认使用的 ProxyCommand 是：

```bash
nc -X 5 -x member.aicloud.szu.edu.cn:30027 %h %p
```

如需覆盖：

```bash
AEXP_HOST=a20049811505541120898398 \
AEXP_PROXY_COMMAND='nc -X 5 -x member.aicloud.szu.edu.cn:30027 %h %p' \
AEXP_KEY=/Users/ziwu/.ssh/id_ed25519 \
scripts/smoke_proxy.sh
```

## 4. 查看临时测试结果

脚本默认使用临时 DB，所以测试结果不会出现在正在运行的 `8080` 前端里。

假设脚本输出：

```text
tmp_home: /tmp/aexp-smoke-home-a0unGJ
```

对应数据库是：

```text
/tmp/aexp-smoke-home-a0unGJ/.aexp/aexp.db
```

用 SQLite 查看：

```bash
sqlite3 /tmp/aexp-smoke-home-a0unGJ/.aexp/aexp.db \
  "select id, name, status, kind, gpu_index, created_at from runs order by created_at;"

sqlite3 /tmp/aexp-smoke-home-a0unGJ/.aexp/aexp.db \
  "select run_id, tool_name, output_json from agent_events order by id;"
```

也可以临时启动一个只看这次测试 DB 的 Web UI：

```bash
./aexp serve \
  --port 18081 \
  --db /tmp/aexp-smoke-home-a0unGJ/.aexp/aexp.db
```

启动后终端会打印 API token。打开：

```text
http://127.0.0.1:18081
```

把 token 填到页面顶部的 API Token 输入框里，即可看到这次临时 smoke 的 resource 和 runs。

## 5. 让测试结果出现在 8080 前端

如果 `8080` 上的前端是这样启动的：

```bash
./aexp serve --port 8080
```

它默认读取：

```text
~/.aexp/aexp.db
```

而 `scripts/smoke_proxy.sh` 默认读取临时 DB，所以前端看不到脚本结果。

如果确实希望 smoke 结果写进 `8080` 正在看的默认 DB，可以这样运行：

```bash
AEXP_TMP_HOME="$HOME" scripts/smoke_proxy.sh
```

这样脚本仍然会从当前源码编译 `/tmp/aexp-smoke-bin`，但 CLI 数据会写入：

```text
~/.aexp/aexp.db
```

刷新 `http://127.0.0.1:8080` 后，填入 8080 服务启动时打印的 token，就能看到 smoke runs。

如果 8080 服务使用了自定义 DB，例如：

```bash
./aexp serve --port 8080 --db ./.tmp/aexp-ui-check.db
```

那么脚本默认不会写到这个 DB。此时有两个选择：

1. 用 `./aexp serve --port 18081 --db <tmp_home>/.aexp/aexp.db` 单独看测试库。
2. 手动通过 8080 的 REST API 提交 smoke run，让结果进入 8080 当前 DB。

## 6. 手动通过 API 提交可见 smoke

如果已经有一个前端服务在运行，例如：

```text
http://127.0.0.1:8080
```

并且终端打印了 token：

```text
API Token: xxxxx
```

可以直接向这个服务提交 smoke。示例：

```bash
TOKEN='替换成服务启动时打印的 token'
BASE='http://127.0.0.1:8080/api/v1'
HOST='a20049811505541120898398'
PROXY='nc -X 5 -x member.aicloud.szu.edu.cn:30027 %h %p'

RES=$(
  node -e 'console.log(JSON.stringify({
    name: "ui-smoke-a200",
    type: "ssh",
    host: process.argv[1],
    port: 22,
    user: "root",
    auth_ref: "/Users/ziwu/.ssh/id_ed25519",
    root_dir: "/root",
    gpu_indices: "0",
    tags: "smoke,ui",
    proxy_command: process.argv[2]
  }))' "$HOST" "$PROXY" |
  curl -s \
    -H "Authorization: Bearer $TOKEN" \
    -H 'Content-Type: application/json' \
    -d @- \
    "$BASE/resources/"
)

RID=$(printf '%s' "$RES" | node -e '
let s = "";
process.stdin.on("data", d => s += d);
process.stdin.on("end", () => console.log(JSON.parse(s).id));
')

BODY=$(
  node -e 'console.log(JSON.stringify({
    resource_id: process.argv[1],
    name: "ui-visible-smoke",
    program: "bash",
    args: ["-lc", "echo UI_SMOKE_OK; echo CUDA=$CUDA_VISIBLE_DEVICES"],
    cwd: "/root",
    gpu_index: 0,
    kind: "smoke",
    created_by: "ui-smoke-script"
  }))' "$RID"
)

RUN=$(
  curl -s \
    -H "Authorization: Bearer $TOKEN" \
    -H 'Content-Type: application/json' \
    -d "$BODY" \
    "$BASE/runs/"
)

RUN_ID=$(printf '%s' "$RUN" | node -e '
let s = "";
process.stdin.on("data", d => s += d);
process.stdin.on("end", () => console.log(JSON.parse(s).id));
')

curl -s \
  -H "Authorization: Bearer $TOKEN" \
  -X POST \
  "$BASE/runs/$RUN_ID/status-check"

curl -s \
  -H "Authorization: Bearer $TOKEN" \
  "$BASE/runs/$RUN_ID/summary"
```

提交成功后刷新前端，就能看到 `ui-smoke-a200` 和 `ui-visible-smoke`。

## 7. 常见疑问

### 为什么脚本成功了，前端看不到？

因为脚本默认写临时 DB，前端服务看的是另一个 DB。看脚本输出里的 `tmp_home`，或者用 `AEXP_TMP_HOME="$HOME"` 写入默认 DB。

### 脚本用的是不是最新二进制？

是。脚本每次都会执行：

```bash
go build -o /tmp/aexp-smoke-bin ./cmd/aexp
```

它用的是当前工作区源码。仓库根目录下已有的 `./aexp` 不会影响脚本，除非你显式设置：

```bash
AEXP_BIN=./aexp scripts/smoke_proxy.sh
```

### 怎么确认 wrapper 已升级？

脚本最后会打印远程：

```bash
cat ~/.aexp/wrapper.version
```

如果 wrapper 代码变化，`aexp` 会通过版本 hash 自动重新部署远程 `~/.aexp/wrapper.sh`。
