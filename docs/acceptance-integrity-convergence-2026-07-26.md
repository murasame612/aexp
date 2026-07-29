# aexp 数据完整性与写入口收口验收标准

状态：Passed

日期：2026-07-26

依据：[prd-integrity-convergence-2026-07-26.md](prd-integrity-convergence-2026-07-26.md)

## 1. Runtime Events

- **EVENT-01**：向 `metric()` 传入 Python float、Decimal、NumPy scalar/array、
  Tensor-like `.item()`/`.tolist()` 对象和不可序列化自定义对象，调用不得抛异常。
- **EVENT-02**：目录只读、父目录创建失败、文件打开失败或 `json.dumps` 失败时，调用不得
  影响训练流程；stderr 至多输出节流告警。
- **EVENT-03**：缺少 `AEXP_UI_EVENTS` 时首次 emit 输出一次明确告警，后续相同告警不刷屏。
- **EVENT-04**：文档中的 `train/loss`、`val/loss`、`val/observed_mse` 写入后的 `name`
  与调用值一致。
- **EVENT-05**：默认仅 rank 0 写入；开启 all-ranks 后事件包含 rank/local_rank。
- **EVENT-06**：远端事件文件截断或 generation 变化后，缓存结果不得包含上一代尾部。

## 2. Run 状态与 finalization

- **RUN-01**：旧 Run 快照不得把 succeeded/failed/cancelled 权威终态改回先前状态。
- **RUN-02**：Cancel 在 SSH 窗口内遇到自然完成时返回 already finished，最终状态保持
  succeeded，输出 finalization 仍执行。
- **RUN-03**：`data_finalization_state` 更新只改变 finalization 子域，不改变 status、
  provenance、exit code、finished_at 或 status source。
- **RUN-04**：同一 runID 并发触发两次 finalization，输出发布最多一次，最终状态一致。
- **RUN-05**：状态写入失败至少产生带 runID、目标状态和错误的 warning。
- **RUN-06**：Store 新代码不得调用无条件全行 `UpdateRun`；兼容调用点必须列入删除账本。

## 3. Run Interpretation / Card

- **CARD-01**：完整 interpretation 后提交仅含 graph patch 的 proposal，question、verdict、
  evidence level、metrics、next action、related runs、artifacts、important 和 promotion
  标志全部保持不变。
- **CARD-02**：pending proposal 后只更新 verdict，graph status、proposal hash、base
  revision、patch JSON 和 reviewed_at 全部保持不变。
- **CARD-03**：缺少 expected revision 的更新被拒绝；并发更新冲突返回 409。
- **CARD-04**：现有 Card 不因当前 CLI cwd 不同而改变 Project；归属变更必须显式操作。

## 4. Evidence

- **EVIDENCE-01**：完全就绪的 formal Run（verified dataset、seeds、Git、config、协议、
  released snapshot）通过 v2 proposal 建立 `run --supports--> claim`，plan 0 blocker，
  accept 成功。
- **EVIDENCE-02**：缺 dataset/seeds/release 的 Run 返回精确 blocker code，不能以
  `RUN_PROJECT_MISMATCH project ""` 代替。
- **EVIDENCE-03**：graph PUT 修改节点/边语义返回
  `SEMANTIC_WRITE_REQUIRES_PROPOSAL`；仅布局变化成功且 graph hash/revision 不产生语义噪音。
- **EVIDENCE-04**：graph PUT 缺 expected revision 返回 400，revision 过期返回 409。
- **EVIDENCE-05**：Primary 与 Topic 对 cycle、方向、重复 run 和重复 semantic edge 得到
  相同验证结果；blocker 带可定位 ID。
- **EVIDENCE-06**：accepted graph 执行全图审计为 0 blocker；存量异常图被列入报告。
- **EVIDENCE-07**：rejected/expired proposal 的相同 patch 可产生新 attempt；draft/pending
  重试幂等；accepted 重试返回明确 already accepted。
- **EVIDENCE-08**：同一 run/claim 不能同时出现 supports、weakens、does_not_prove。
- **EVIDENCE-09**：proposal、legacy compatibility 和直接 REST 对同一非法语义得到一致拒绝。

## 5. Project

- **PROJECT-01**：存在 Run、Card 或 active Map 时删除 Project 被拒绝，并列出引用对象。
- **PROJECT-02**：active Map 与 Run Card 不能指向不存在的 Project。
- **PROJECT-03**：所有新 Run（setup/smoke/pilot/formal/ablation）在落库前必须绑定存在的
  canonical Project；缺失或未知 Project 不得留下半成品 Run。
- **PROJECT-04**：manual category 不再形成新的规范 Project 归属。
- **PROJECT-05**：历史未归属或误归属的 terminal Run 可通过显式、CAS 防冲突且有审计的
  改绑操作修正；系统不自动批量迁移，不提供 unassign。
- **PROJECT-06**：改绑只改变当前组织归属并同步兼容 Run Card 投影；不可变 RunManifest、
  Snapshot、Release、Freeze 以及历史 Journal/Evidence 引用不被重写。

## 6. 迁移与真实数据

- **MIGRATION-01**：使用真实数据库 `.backup` 副本演练，绝不直接在唯一生产库试迁移。
- **MIGRATION-02**：迁移前后 `runs`、`project_run_cards`、`evidence_chains`、
  `evidence_chain_nodes`、`evidence_chain_edges`、`artifacts`、`dataset_versions` 计数不减少。
- **MIGRATION-03**：迁移及重复启动后 `PRAGMA integrity_check=ok`。
- **MIGRATION-04**：`foreign_key_check` 无新增错误；历史孤儿以结构化审计报告保留。
- **MIGRATION-05**：重复迁移幂等，revision、hash 和 proposal 状态不漂移。

## 7. 自动化与生产验收

- **PRODUCTION-01**：`go test ./...` 全绿。
- **PRODUCTION-02**：UI-v2 typecheck、Vitest 与 production build 全绿。
- **PRODUCTION-03**：重新构建 arm64 二进制、ad-hoc 签名、替换
  `/Users/ziwu/.local/bin/aexp` 并由 `com.ziwu.aexp` 重启。
- **PRODUCTION-04**：健康接口返回 `status=ok`，UI-v2 使用新 hash 资产。
- **PRODUCTION-05**：替换前创建旧二进制和数据库备份，并记录回滚路径。
- **PRODUCTION-06**：重启前处于 running/starting 的 Run 不丢失；状态检查继续更新。

## 8. 阶段门

### Stage 0 通过条件

`EVENT-01..04`、`RUN-01..03/05`、`CARD-01..03`、`EVIDENCE-01..02` 全部通过。

### Stage 1 通过条件

`RUN-04/06`、`EVIDENCE-03..09` 全部通过。

### Stage 2 通过条件

`CARD-04`、`PROJECT-01..04`、`MIGRATION-01..05` 全部通过。

### 最终通过条件

所有必选条目与 `PRODUCTION-01..06` 通过，并在本文件附加实际命令、计数、备份路径、
二进制 SHA-256、PID 和已知剩余风险。

## 9. 验收记录（2026-07-27）

### 9.1 结论

`EVENT-01..06`、`RUN-01..06`、`CARD-01..04`、`EVIDENCE-01..09`、
`PROJECT-01..04`、`MIGRATION-01..05` 和 `PRODUCTION-01..06` 均已通过。

这次收口没有删除 Run、Card、Evidence revision 或历史图数据。新写入使用窄字段更新、
expected revision/CAS、proposal 审核与 Project 存在性门禁；历史不合法 Evidence Map
仍可读取，但由全图审计阻止其被误当成可发布证据。

### 9.2 自动化验证

执行并通过：

```bash
go test ./internal/store ./internal/api ./internal/executor \
  ./internal/eventcache ./internal/runio ./internal/mcp ./cmd/aexp -count=1
go test ./... -count=1
npm --prefix web test -- --run
npm --prefix web run typecheck
npm --prefix web run build
```

结果：

- Go 全部 15 个 package 通过；
- UI-v2 共 22 个 test files、87 个 tests 通过；
- TypeScript typecheck 通过；
- Vite production build 通过；
- 生产 UI 主资产
  `/ui-v2/assets/index-2vlXh3GQ.js` 返回 HTTP 200。

静态写入口审计：

```bash
rg -n '\.UpdateRun\(' --glob='*.go' --glob='!**/*_test.go' .
rg -n 'eventcache\.Write\(' --glob='*.go' --glob='!**/*_test.go' .
```

两项均无生产调用。兼容方法仍保留在具体 SQLite 实现和测试中，并已进入
[deprecation-ledger.md](deprecation-ledger.md)。

### 9.3 真实数据库副本演练

输入副本：

```text
.tmp/integrity-convergence/aexp-before.db
.tmp/integrity-convergence/aexp-migration-drill.db
```

新二进制连续启动同一 drill 数据库两次。启动前、第一次启动后、第二次启动后的
Evidence 表确定性 dump SHA-256 均为：

```text
9475cfa777b4066a48fa9dbbe5a7dcd46b7fdcd7389ebc192ae2e6a10fa39a1c
```

第二次启动后的检查结果：

| 表 | 行数 |
| --- | ---: |
| `runs` | 548 |
| `project_run_cards` | 161 |
| `evidence_chains` | 14 |
| `evidence_chain_nodes` | 76 |
| `evidence_chain_edges` | 70 |
| `evidence_chain_revisions` | 46 |
| `evidence_proposals` | 7 |

`PRAGMA integrity_check` 返回 `ok`，`PRAGMA foreign_key_check` 无结果。二次启动后的
备份为：

```text
.tmp/integrity-convergence/aexp-migration-drill-after-second.db
SHA-256 26fe5aff2ca549182852034921359f16e9c03f850e610ae79da1fbb67e88f761
```

完整审计产物位于：

```text
.tmp/integrity-convergence/evidence-audit/report.json
.tmp/integrity-convergence/evidence-audit/audits.ndjson
```

14 张存量图中 5 张通过，9 张被结构化阻断。阻断统计为：

- `GRAPH_HASH_MISMATCH`: 9
- `INVALID_EDGE_DIRECTION`: 8
- `RUN_PROJECT_MISMATCH`: 16
- `DUPLICATE_RUN_NODE`: 2
- `GRAPH_CYCLE`: 1
- `PROJECT_CARD_MISMATCH`: 1

这是存量图的显式修复队列，不是迁移失败；演练没有自动改写这些历史关系。

### 9.4 生产替换

最终构建、签名并安装的二进制：

```text
/Users/ziwu/.local/bin/aexp
Mach-O 64-bit executable arm64
SHA-256 a6cca821d1fc8dbb8c7aff0f398c8133048a663bff8a5685939b9dab502b278c
codesign --verify --strict: passed
```

回滚副本：

```text
/Users/ziwu/.local/bin/backups/aexp-before-integrity-20260727-004323
/Users/ziwu/.aexp/backups/aexp-before-integrity-20260727-004323.db
database backup SHA-256 ccf8cb2677708c66451819442da7943467c16a89c1ca47d69a4c944d35bb3eba
```

`com.ziwu.aexp` 重启后：

- PID：`35233`
- `/api/v1/health`：`status=ok`
- UI-v2 index 与新 hash 主资产均为 HTTP 200
- 数据库计数与演练基线一致，integrity/FK 检查通过
- 重启前后的两个活跃 Run ID 完全一致，状态均保持 `running`
- 当前 aexp PID 的僵尸子进程数为 0
- manual project category 新写入返回 HTTP 410
- Evidence audit API 返回 HTTP 200

### 9.5 已知剩余风险

1. 9 张历史 Evidence Map 仍需逐图人工确认后通过 proposal 修复；本次只审计和阻断，
   没有静默重写研究关系。
2. `finalization` 的 singleflight 在当前单个 `aexp serve` 进程内保证一次发布；未来若部署
   多控制面实例，需要再增加数据库级 lease。
3. 宿主机仍有 6 个不属于当前 aexp PID 的僵尸进程；当前 aexp 服务自身为 0。
4. 多个离线计算资源仍会产生 SSH/ProxyCommand 健康告警；这不会改变活跃 Run 的权威状态，
   但应作为资源连通性任务单独处理。
5. Vite 对两个大 chunk 给出体积告警，构建成功；这是前端性能优化项，不是本轮数据完整性
   验收阻断项。
