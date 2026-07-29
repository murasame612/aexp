# 运行时审计：Run 状态、Run 卡片、Project 分层、事件记录（2026-07-26）

状态：Findings，未实施

配套文档：[review-evidence-chain-2026-07-26.md](review-evidence-chain-2026-07-26.md)（证据链专项）

范围：`internal/executor/`、`internal/runio/`、`internal/monitor/`、`internal/eventcache/`、
`internal/store/sqlite.go`、`internal/store/schema.sql`、`internal/api/server.go`、
`cmd/aexp/main.go` 的 project card / propose 路径、注入到远端的 `aexp_events.py`

---

## 0. 结论摘要

**"卡片状态更新不稳定"的直觉是对的，而且比预期严重。** 根因不是 UI 刷新问题，
是三处**全列覆盖写 + 无 CAS + 无字段合并**：

| 现象 | 实测后果 |
| --- | --- |
| 提交一次 graph patch | 卡片的 question / verdict / evidence_level / key_metrics / next_action / supports_claim / related_runs / artifact_paths / important / should_promote **10 个字段全部清空** |
| 写一次卡片解释字段 | pending proposal 被重置为 `none`，proposal_hash 清空，327 字节的 patch_json 归零 |
| Cancel 与 run 自然结束竞争 | 已 succeeded 的 run 被改回 cancelled，`data_finalization_state` 回退 `completed → pending`，且**输出发布被永久跳过** |

**事件记录目前不可靠，有一个会杀掉训练进程的缺陷。**
`emit()` 没有任何 try/except：`metric("loss", tensor)` 或 `metric("acc", np.float32(x))`
会抛 `TypeError` 并中断训练循环。反过来，`AEXP_UI_EVENTS` 未设置时它**静默丢弃全部遥测**
并返回一个看起来成功的 dict，Agent 无法区分"记录成功"和"什么都没记"。

**Project 分层的引用完整性是半套的。** `evidence_snapshots/releases/proposals` 有
FK 约束，而 `runs` / `project_run_cards` / `evidence_chains` 三张身份表没有。
删除一个 Project 会留下孤儿 Run、孤儿卡片和**孤儿 Primary Map**，而孤儿 Map 从此永久不可写。

### 审计方法

所有 ⛔ 标记的结论都由临时 probe 实测（`internal/store`、`internal/eventcache` 内的真实
SQLite + 真实 helper；事件 helper 从 `AexpEventsPythonHelper()` 导出后用 python3 实跑）。
验证后探针已全部删除，`go build ./...` 与
`go test ./internal/store/ ./internal/eventcache/ ./internal/executor/ ./internal/monitor/`
恢复全绿——**这些缺陷对现有测试套件全部不可见**。

---

## A. Run 状态更新

### A-1 全列覆盖写会回滚权威终态 ⛔ 已确认

**位置**：`internal/store/sqlite.go:949` `UpdateRun` / `:955` `runUpdateSQL`

`runUpdateSQL` 一次写 **57 列**，没有 CAS：

```go
func (s *SQLite) UpdateRun(ctx context.Context, r *Run) error {
	args := append(runUpdateArgs(r), r.ID)
	_, err := s.db.ExecContext(ctx, runUpdateSQL+` WHERE id=?`, args...)
	return err
}
```

任何 `GetRun → 改一个字段 → UpdateRun` 的调用方，都会用自己那份快照
**覆盖其余 56 列**，包括 `status`、`status_source`、`status_observed_at`、`exit_code`、
`finished_at`、`data_finalization_state`。

**实测**：run 处于 running；权威写入方用 `UpdateRunIfStatus` 正确提交
succeeded + exit_code=0 + data_finalization=completed；随后一个持有旧快照的
`UpdateRun` 落地：

```
status=cancelled (want succeeded)
data_finalization_state=pending (want completed)
status_source=local_cache (want remote_exit_code)
```

**对比**：探针路径 `persistRunProbe → UpdateRunIfStatus`（`executor.go:1469`）是
**正确的**——它做 CAS，失败时重新加载。问题只在绕过它的调用方。

**修复**：把 `UpdateRun` 从 Store 接口移除或改为私有，仅保留
`UpdateRunIfStatus`；对只改单一子域的写入提供窄接口
（`UpdateRunDataFinalization`、`UpdateRunProvenance`），像
`UpdateRunStatusObservation` 那样只 UPDATE 自己那几列并带 `WHERE status=?`。

---

### A-2 Cancel 会把成功的 run 标成 cancelled 并跳过输出发布 ⛔ 高危

**位置**：`internal/executor/executor.go:2422` `Cancel`

```go
run, _ := e.store.GetRun(ctx, runID)      // 快照
... tmux send-keys ...                     // SSH 往返
time.Sleep(2 * time.Second)                // ← 至少 2 秒窗口
... tmux has-session / kill-session ...    // 再两次 SSH 往返
run.Status = store.RunStatusCancelled
e.store.UpdateRun(ctx, run)                // ← 盲写，且错误被丢弃
go e.finalizeRunEvidence(run.ID)
```

这段窗口里 reconciler（默认 15s 一轮，`run_reconciler.go:87`）或任何
`CheckRunStatus` 都可能发现 `exit_code` 文件已写出，把 run 正确地转为 succeeded。
随后 Cancel 的盲写把它改回 cancelled。

**真正的损失不是状态显示，是数据**：`finalizeRunEvidence`（`executor.go:1692`）里

```go
if run.Status == store.RunStatusSucceeded && e.runIO != nil {
    _ = e.runIO.FinalizeOutputs(ctx, run, resource)
}
```

status 已被改成 cancelled，于是 `FinalizeOutputs` 被跳过——一个**实际成功的实验，
其输出永远不会发布成 Asset revision**，也就永远无法进入 Evidence Snapshot。
用户看到的是"我取消晚了，但结果没了"。

另外 `e.store.UpdateRun(ctx, run)` 的返回值被完全忽略，写失败无任何日志。

**修复**：Cancel 的最终写入改为 `UpdateRunIfStatus(run, expectedStatus)`，
`expectedStatus` 取自进入 SSH 操作前的状态；CAS 失败说明 run 已自然结束，
此时应保留真实终态并返回 `run already finished`，而不是覆盖。

---

### A-3 finalizeRunEvidence 无去重，可并发重复发布 ⛔

`go e.finalizeRunEvidence(run.ID)` 出现在 **4 个位置**
（`executor.go:1465`、`:1994`、`:2498`、`:2765`），没有任何 in-flight 去重
（无 mutex map、无 singleflight、无 DB 租约）。每个 goroutine 持有
**30 分钟** context，内部串行做 `CollectArtifacts` → `FinalizeOutputs`（含实际传输）
→ `persistRunManifest`。

两条路径同时判定 run 终态（例如 Cancel 与 reconciler 各触发一次），
或 reconciler 在上一轮 finalize 尚未结束时再次探测到终态，就会有两个
finalize 并发执行：都创建 transfer job、都写 binding、都盲写 run 行。

**修复**：以 runID 为键的 in-flight 集合 + 幂等检查
（`data_finalization_state` 已是 `completed`/`skipped` 则直接返回）。
30 分钟 context 应可配置，并在 executor 关停时被取消（目前用的是
`context.Background()`，进程退出时无法优雅收尾）。

---

### A-4 FinalizeOutputs 跨长传输持有 run 快照

**位置**：`internal/runio/service.go:135-173, 230-241`

`FinalizeOutputs` 接收 `run *store.Run`，在整个输出发布过程中持有它——
其中 `publishOutput → executeAndWait` 会轮询等待真实 rsync 传输完成，
可能是分钟级——然后 `finishFinalization` 用这份快照做全列 `UpdateRun`。
这是 A-1 的最长竞争窗口。

**修复**：`finishFinalization` 只更新 `data_finalization_*` 三列（窄 UPDATE），
不要回写整行。

---

## B. Run 卡片状态

### B-1 提交 graph patch 会清空卡片的研究解释 ⛔ 已确认

**位置**：`internal/api/server.go:4553` `handleSubmitEvidenceGraphProposal`
→ `internal/store/evidence_proposal.go:15` → `sqlite.go:2588` `SaveProjectRunCard`

HTTP handler 把客户端 body 里的 `card` **原样**交给 store：

```go
var req struct {
    Card  store.ProjectRunCard      `json:"card"`
    Patch *store.EvidenceGraphPatch `json:"patch,omitempty"`
    ...
}
req.Card.RunID = runID
saved, err := s.store.SubmitEvidenceGraphProposal(r.Context(), &req.Card, req.Patch)
```

`SubmitEvidenceGraphProposal` 只从既有卡片里取回 `existing.ID`
（`evidence_proposal.go:30-32`），**不合并任何解释字段**；
`SaveProjectRunCard` 又是全列 UPSERT（`ON CONFLICT(run_id) DO UPDATE SET` 覆盖 22 列）。

**实测**：先写入一张完整卡片，再提交一个只带 patch 的提案：

```
question: Does X reduce val loss? -> ""
verdict: Yes, by 0.03 -> ""
evidence_level: A -> ""
key_metrics: val/loss=0.21 -> ""
next_action: replicate with 3 seeds -> ""
supports_claim: claim_x -> ""
related_runs: run_baseline -> ""
artifact_paths: results/metrics.json -> ""
important: true -> false
should_promote: true -> false
```

注意 `evidence_level` 被清成 `""` 而不是回落到默认 `"C"`，
后续按 evidence_level 过滤/排序的查询会漏掉这张卡片。

CLI 路径（`cmd/aexp/main.go:3549`）先 `GetProjectRunCard` 再改，所以是安全的。
**只有 REST/UI/MCP 这条路径会丢数据**，而它正是 Agent 与前端在用的那条。

---

### B-2 写一次卡片解释字段会摧毁 pending proposal ⛔ 已确认

反方向同样成立。`SaveProjectRunCard` 覆盖
`graph_status`、`proposal_hash`、`base_graph_revision`、`graph_patch_json`、
`reviewed_at`、`no_graph_impact`、`graph_impact_reason`，并且：

```go
if strings.TrimSpace(c.GraphStatus) == "" {
    c.GraphStatus = "none"
}
```

——未携带 graph 状态的写入不是被拒绝，而是被**静默规范化为 `none`**。

**实测**：提交提案（graph_status=pending）后，用一个只带 verdict 的卡片写入：

```
graph_status "pending" -> "none"
proposal_hash "74b4f755…" -> ""
patch_json len 327 -> 0
```

一个待审提案就这样消失了，没有错误、没有事件、没有 revision 记录。
用户侧表现正是"卡片状态自己变回去了"。

**修复（B-1 + B-2 共用）**

1. `SaveProjectRunCard` 拆成两个不重叠的写入面：
   `SaveRunInterpretation`（question/verdict/level/metrics/important/…）与
   `SaveRunGraphProposal`（graph_status/hash/base_revision/patch_json/reviewed_at），
   各自只 UPDATE 自己的列。
2. `SubmitEvidenceGraphProposal` 内部先加载既有卡片，把解释字段合并进去再写。
3. `handleSubmitEvidenceGraphProposal` 不接受客户端整行 card，只接受
   `patch` + `no_graph_impact` + `graph_impact_reason`。
4. 卡片写入加 `expected_updated_at` 或 revision 做 CAS，冲突返回 409。
5. `GraphStatus == ""` 时不要静默改 `none`：新建卡片才默认 `none`，
   更新已有卡片时保留原值。

---

## C. Project 分层

### C-1 删除 Project 留下孤儿，且孤儿 Primary Map 永久不可写 ⛔ 已确认

**位置**：`internal/store/sqlite.go:1153` `DeleteProjectDefinition`

```go
DELETE FROM project_targets WHERE project_id=?
DELETE FROM project_definitions WHERE id=?
```

只处理 `project_targets`。**实测**：

```
project deleted (now <nil>) but references survive:
  run.project_id="proj_b"
  evidence_chain(chain_primary_9b5b49b980594a28).project_id="proj_b" role="primary"
  card.project_id="proj_b"
and the orphaned Primary Map can no longer be written: project "proj_b" does not exist
```

孤儿 Primary Map 的处境是死的：`validateEvidenceProposalOwnership`
（`evidence_workspace.go:68`）要求 Project 存在，而系统里**没有任何"把 Map 改挂到另一个
Project"的 API**。`acceptance-evidence-workspace-v2.md` 的 OWN-05 只规定了一次性迁移，
没有规定防止再次产生。

**修复**：`DeleteProjectDefinition` 改为在同一事务里检查引用：
存在 run / card / active Map 时拒绝删除并返回结构化 blocker
（列出阻塞对象），或提供显式 `--cascade` / `--reassign-to` 语义。
同时补一个 `ReassignEvidenceMapProject` 用于修复存量孤儿。

---

### C-2 身份表没有 FK，可以指向不存在的 Project ⛔ 已确认

FK pragma 确实是开的（`sqlite.go:32` DSN + `:37` 显式 `PRAGMA foreign_keys=ON`），
但**覆盖是半套的**：

| 表 | project_id 定义 | FK |
| --- | --- | --- |
| `runs` (schema.sql:31) | `TEXT DEFAULT ''` | ❌ |
| `project_run_cards` (:474) | `TEXT NOT NULL` | ❌ |
| `evidence_chains` (:592) | `TEXT DEFAULT ''` | ❌ |
| `evidence_snapshots` (:394) | `TEXT NOT NULL REFERENCES project_definitions(id)` | ✅ |
| `evidence_releases` (:408) | `TEXT NOT NULL REFERENCES project_definitions(id)` | ✅ |
| `evidence_proposals` (:662) | `TEXT NOT NULL REFERENCES project_definitions(id)` | ✅ |
| `project_targets` (:288) | `NOT NULL REFERENCES … ON DELETE CASCADE` | ✅ |

**实测**：

```
runs.project_id="project_does_not_exist" accepted with no such Project and no FK
project_run_cards.project_id="another_ghost_project" accepted with no such Project and no FK
```

这解释了为什么 `formalRunEvidenceBlockers` 里要写 `run.ProjectID == ""` 这种防御
——空 project_id 是一个真实且常见的状态，而不是异常。

**做对了的地方**：`CreateEvidenceChain` 对 active Map 的归属校验是成立的。
探针尝试创建无主 active chain 被正确拒绝：
`active evidence maps must belong to a registered project`（OWN-01 通过）。
修 C-2 时应把这条守卫的做法推广到 runs 和 cards，而不是反过来放宽。

**修复**：加迁移把三张表的 `project_id` 补上 FK（存量脏数据先清理或置空），
并统一"空 project_id 是否合法"的语义——建议 Run 允许空（未归档的探索性 run），
但卡片和 Map 必须有主。

---

### C-3 三套并行的项目分组仍然共存

- `project_definitions` —— PRD 指定的唯一规范身份；
- `manual_project_categories` + `manual_run_project_assignments` —— 遗留手工分类，
  仍有写入 API（`server.go:3314` `handleAssignRunManualProjectCategory`）；
- `project_profiles` —— 以 `(resource_id, cwd)` 为键的第三套隐式分组。

`prd-system-simplification.md` §1.1 已裁定
"ProjectDefinition ID 成为唯一规范 Project 身份；其他两者迁移为读模型或显式映射"，
但 manual 分类的**写**入口还在。这与
[review-evidence-chain-2026-07-26.md](review-evidence-chain-2026-07-26.md) §5 提到的
"同一意图多条写路径"是同一类问题。

---

### C-4 CLI `project card` 无条件按当前目录改写卡片归属

`cmd/aexp/main.go:2907-2908`：

```go
card.ProjectID = projectID                    // projectIDFromConfig(cfg)
card.ProjectName = projectNameFromConfig(cfg)
```

不是 `if cmd.Flags().Changed(...)` 保护的，而是无条件赋值。在另一个项目目录
（或没有项目配置的目录）下对同一个 run 执行 `aexp project card`，会把卡片
静默改挂到别的 Project 或空 Project。因为没有 FK 也没有校验（C-2），这一步不会报错。

**修复**：只在卡片新建时从 config 取 project；已有卡片的归属变更必须显式
`--project`，且校验目标 Project 存在、与 `run.project_id` 一致。

---

## D. 事件记录：如何让 Agent 稳定记录

被注入远端的 helper 源码在 `internal/executor/executor.go:3075`
`AexpEventsPythonHelper()`。以下用 `python3` 实跑该 helper 验证。

### D-1 emit 不是 fail-safe，会杀掉训练进程 ⛔ 已确认，最高优先级

`emit()`（`executor.go:3108-3128`）全程没有 try/except：

```python
path.parent.mkdir(parents=True, exist_ok=True)
with path.open("a", encoding="utf-8") as f:
    f.write(json.dumps(data, ensure_ascii=False, separators=(",", ":")) + "\n")
```

**实测**：

```
python float         -> OK
Decimal              -> RAISES TypeError: Object of type Decimal is not JSON serializable
torch tensor (stub)  -> RAISES TypeError: Object of type FakeTensor is not JSON serializable
```

`numpy.float32/float64`、`torch.Tensor`、`Decimal` 都不是 JSON 可序列化的，
而 `metric("train/loss", loss)`（漏了 `.item()`）是 ML 代码里最常见的写法之一。
后果是**遥测调用在训练循环中段抛异常，直接终止实验**。
`mkdir` / `open` 在只读挂载、磁盘满、权限不足时同样会抛 `OSError`。

一个"科研实验可靠执行"系统里，观测手段绝不能成为故障源。

**修复**

```python
def _coerce(value):
    if isinstance(value, (str, int, float, bool)) or value is None:
        return value
    for attr in ("item", "tolist"):          # torch / numpy
        if hasattr(value, attr):
            try:
                return _coerce(getattr(value, attr)())
            except Exception:
                pass
    return repr(value)
```

并把整个写入包进 `try/except Exception`，失败时写一条到 `stderr`
（带节流，避免刷屏）并返回，**永不向调用方抛异常**。

---

### D-2 未设置 AEXP_UI_EVENTS 时静默丢弃全部遥测 ⛔ 已确认

`executor.go:3119-3121`：

```python
path = _event_path()
if path is None:
    return data
```

**实测**：`del os.environ["AEXP_UI_EVENTS"]` 后 `metric("train/loss", 0.5)` 返回
`{'type': 'metric', 'name': 'train/loss', 'value': 0.5, 'time': 1785062090.6}`
——一个看起来完全成功的 dict，**stderr 没有任何输出，磁盘上没有任何文件**。

触发场景很常见：脚本在 aexp 之外单独跑、通过 `env -i` / `sudo` / `docker exec`
（未 `-e` 透传）/ 某些 `torchrun` 配置启动的子进程、或 `AEXP_RUN_DIR` 过期
（`AexpEventsPythonShim` 只在 shim 路径下检查 helper 存在性，不检查 `AEXP_UI_EVENTS`）。

Agent 侧的表现：训练正常跑完，`aexp_check_run_events` 报告零事件，
而 Agent 无法判断是"脚本没插桩"还是"插桩了但环境变量丢了"。

**修复**：首次 emit 且 `AEXP_UI_EVENTS` 缺失时向 stderr 打印一次显式告警
（`aexp_events: AEXP_UI_EVENTS is not set; telemetry is being discarded`）；
提供 `aexp_events.selftest()` 供 Agent 在 submit 前验证；
`aexp_check_run_events` 的零事件回复里必须区分
"未插桩" / "已插桩但环境缺失" / "已插桩且写入成功但无事件"。

---

### D-3 名字规范化与 helper 自己的文档矛盾 ⛔ 已确认

helper docstring（`executor.go:3083-3084`）把
`"val/observed_mse"` 明确列为**推荐**写法。但 `_normalize_event`
（`:3177-3197`）在 `len(context) <= 20 and "_" not in leaf` 不成立时会拆名。

**实测**：

```
'train/loss'        -> name='train/loss'      series=''
'val/loss'          -> name='val/loss'        series=''
'val/observed_mse'  -> name='observed_mse'    series='val'      ← 被改写
'val/mse'           -> name='val/mse'         series=''
```

同一次 run 里，`train/loss` 保留全名而 `val/observed_mse` 被拆成
`name=observed_mse, series=val`。触发条件是 leaf 里**有没有下划线**——
对使用者完全不可预测。Agent 按 `"val/observed_mse"` 去查询/比对指标会查不到。

**修复**：二选一，不要两者都做。要么删掉自动拆名，
只在 `_event_warnings` 里给出建议（保持 Agent 写入的 key 不变，这是更好的选择）；
要么在 docstring 里明确写出重写规则并保证一致
（所有多段名都拆，而不是按下划线抽签）。

---

### D-4 事件缓存按行号拼接，重启后混合两代内容 ⛔ 已确认

**位置**：`internal/eventcache/eventcache.go:75-116` `Write`

合并逻辑是纯行号 splice：
`merged = existing[:first-1] + incoming + existing[first-1+len(incoming):]`。
没有 inode、大小、mtime 或内容哈希校验。

**实测**：先缓存第 1 代的 5 行，脚本重启后新一代只有 2 行：

```
line 1: {"value":0.9,"gen":2}
line 2: {"value":0.8,"gen":2}
line 3: {"value":0.3,"gen":1}   ← 上一代的残留
line 4: {"value":0.4,"gen":1}
line 5: {"value":0.5,"gen":1}
```

Agent 通过 `aexp_tail_run_events` 读到的是一条连续的、混合了两次执行的曲线，
且无法分辨。对"resume 后继续训练"这种常规操作，损失曲线会出现无法解释的跳变。

**修复**：缓存记录远端文件的 `(size, mtime, inode)` 或首行哈希作为 generation 标识；
generation 变化时另存为新段并在读取时明确分段，而不是就地 splice。

---

### D-5 多进程（DDP）与 NFS 下的写入完整性

- `emit` 每次调用都 open→write→close，且**一次 emit 可能写多行**
  （事件行 + 若干 warning 行，`executor.go:3123-3127`），这些 `f.write` 共享同一个
  缓冲区、在 close 时统一 flush。多个 rank 共享同一 `AEXP_UI_EVENTS` 时，
  事件行与它的 warning 行之间可能被别的 rank 插入。
- Linux 本地文件系统上 `O_APPEND` 对 < PIPE_BUF 的写是原子的，
  **但 NFS 上不保证**。本项目的前提就是 NAS 存储，一旦 `AEXP_UI_EVENTS`
  落在 NFS 挂载上，多 rank 并发会产生截断/交错的坏 JSONL 行。
- helper **不注入 `rank`/`local_rank` 字段**，也不做 rank 过滤。
  N 卡训练会产生 N 份重复事件，消费端无法去重。

**修复**：helper 读取 `RANK`/`LOCAL_RANK`/`SLURM_PROCID`，
默认只在 rank 0 写入（`AEXP_EVENTS_ALL_RANKS=1` 可覆盖），
并把 rank 写进事件；每次 emit 只做一次 `write()` 调用（把多行拼成一个字符串）。

### D-6 无序列号、无 fsync

事件只有 `time.time()` 墙钟浮点数，没有单调序号。快速连续 emit 会得到相同时间戳，
NTP 校正可能造成时间回退，而缓存层的身份是行号（D-4）。
建议加进程内单调 `seq`（配合 rank 构成全序），并在 `training_done` 之后
显式 flush + `os.fsync`，避免进程被 kill 时丢失尾部事件。

---

## E. 其他

### E-1 API 鉴权

`internal/api/server.go:4881` `authorizeRequest`：

- `if token != s.apiToken` 是**非常量时间比较**，存在 token 时序侧信道。
  应改 `subtle.ConstantTimeCompare`。
- token 可以走 **URL query string**（`:4927` `r.URL.Query().Get("token")`）。
  这会进入访问日志、浏览器历史和 Referer。WebSocket/EventSource 需要它可以理解，
  但应改为一次性短时 ticket，而不是复用长期 API token。
- `if s.apiToken == "" { return true }` —— 未配置 token 时**全部放行**。
  结合 [review-evidence-chain-2026-07-26.md](review-evidence-chain-2026-07-26.md) 的
  P0-2（`PUT /evidence-chains/{id}/graph` 无门禁全量写图），
  一个无 token 且非 loopback 绑定的部署等于把证据图完全敞开。
  建议：非 loopback 监听时**强制**要求 token，启动即拒绝而不是静默放行。

### E-2 SQLite 连接池未限制写并发

`NewSQLite`（`sqlite.go:25`）设置了 WAL + `busy_timeout=5000` + FK，
但没有 `db.SetMaxOpenConns(...)`。WAL 下 SQLite 仍然只允许单写者，
reconciler（15s 轮询）+ API handler + executor goroutine + transfer worker
并发写入时依赖 5 秒 busy timeout 排队，高负载下会出现 `SQLITE_BUSY` 写失败。
考虑 `SetMaxOpenConns(1)`（或读写分离两个池）把排队交给 Go 侧，
顺便让 A-1/B-1 那类 read-modify-write 竞争窗口变窄——**但那不是修复**，
真正的修复仍然是窄列写入 + CAS。

### E-3 错误被静默丢弃

`Cancel` 里的 `e.store.UpdateRun(ctx, run)`（`executor.go:2491`）、
`e.exec(...)` 的多处返回值、`runio` 里的
`_ = s.Store.UpdateRunInputBinding(context.Background(), binding)` 等，
都丢弃了错误且不记日志。状态"不稳定"却查不到原因，很大程度上来自这里。
建议：所有状态写入失败必须至少 `logger.Warn` 一次并带 runID。

---

## F. 建议修复顺序

**Stage 0 —— 数据丢失止血（优先级最高）**

1. **D-1**：`emit` 值强制转换 + 全程 try/except。这是唯一会**毁掉一次实验**的缺陷，
   改动最小、收益最大。
2. **B-1 / B-2**：拆分卡片写入面 + `handleSubmitEvidenceGraphProposal` 不再接受整行 card。
3. **A-2**：Cancel 最终写入改 CAS，避免"成功的 run 丢掉输出发布"。

**Stage 1 —— 状态机收口**

4. **A-1**：`UpdateRun` 私有化，只保留 CAS 与窄列写入。
5. **A-3**：finalize 去重 + 幂等 + 可取消 context。
6. **A-4**：`finishFinalization` 改窄列 UPDATE。
7. **E-3**：状态写入失败必须打日志。

**Stage 2 —— Project 归属完整性**

8. **C-1**：`DeleteProjectDefinition` 引用检查 + `ReassignEvidenceMapProject`。
9. **C-2**：三张身份表补 FK（先写存量清理迁移）。
10. **C-4**：卡片归属不再随 cwd 漂移。
11. **C-3**：关闭 manual 分类的写入口（读模型保留）。

**Stage 3 —— 事件可信度**

12. **D-2**：缺环境变量时显式告警 + `selftest()` + `check_run_events` 区分三种零事件原因。
13. **D-5**：rank 感知 + 单次 write。
14. **D-4**：缓存 generation 标识。
15. **D-3**：名字规范化与文档对齐（建议直接删掉自动拆名）。
16. **D-6**：单调 seq + 收尾 fsync。

**Stage 4 —— 加固**

17. **E-1**：常量时间比较、query token 换 ticket、非 loopback 强制 token。
18. **E-2**：连接池策略。

---

## G. 测试补强

现有套件对本文所有 ⛔ 项均为绿。至少需要：

- **并发写入测试**：对 Run 行和卡片行，两个写入者交错时不允许出现字段回退。
  可用一个通用表驱动测试覆盖 A-1 / B-1 / B-2。
- **Cancel 竞争测试**：run 在 Cancel 的 SSH 窗口内自然结束，断言终态是 succeeded
  且 `FinalizeOutputs` 仍被执行。
- **finalize 幂等测试**：同一 runID 并发触发两次 finalize，断言输出只发布一次。
- **Project 删除测试**：存在 run / card / active Map 时删除必须被拒绝并列出阻塞对象。
- **FK 迁移测试**：在真实生产库备份上跑迁移，断言无损且脏引用被显式报告。
- **事件 helper 契约测试**（Python 侧）：
  - 传入 torch/numpy/Decimal/自定义对象，emit 必须成功且不抛；
  - 只读目录 / 磁盘满模拟下 emit 必须不抛；
  - `AEXP_UI_EVENTS` 缺失时必须产生一次 stderr 告警；
  - 文档中列出的每个推荐 metric 名，emit 后 `name` 必须与传入值一致。
- **eventcache generation 测试**：截断/重写远端文件后，读取结果不得包含上一代内容。
