# 证据链审计与减负方案（2026-07-26）

状态：Findings + proposal，未实施

范围：`internal/store/evidence_*.go`、`internal/store/run_readiness.go`、
`internal/api/server.go` 的 evidence 路由、`internal/mcp/server.go` 的 evidence 工具、
`web/src/evidenceChain.ts`

配套文档：[review-runtime-audit-2026-07-26.md](review-runtime-audit-2026-07-26.md)
（Run 状态、Run 卡片、Project 分层、事件记录专项）

对照文档：[prd-evidence-workspace-v2.md](prd-evidence-workspace-v2.md)、
[acceptance-evidence-workspace-v2.md](acceptance-evidence-workspace-v2.md)、
[prd-system-simplification.md](prd-system-simplification.md)、
[deprecation-ledger.md](deprecation-ledger.md)

---

## 0. 结论摘要

1. **有一个 P0 功能性缺陷：规范的 v2 proposal 路径永远无法建立任何正式证据关系。**
   即使 Run 完全满足 readiness policy（formal grade、verified dataset、seeds、git commit、
   config hash、split/eval protocol、released snapshot 全部齐备），
   `run --supports--> claim` 也一定被拒绝，且返回的 blocker 是错误且不可修复的
   `RUN_PROJECT_MISMATCH: run "..." does not belong to project ""`。
   证据链的核心语义在规范路径上是不可达的；只有被标记为 legacy 的 Run Card 路径能工作。

2. **门禁不是图的不变量，只是 proposal 增量上的一次性检查。** 直接写图的 REST 入口
   完全不过 formal gate，且已 accepted 的图内容在后续 proposal 中永不重新校验。

3. **语义层基本为空。** 系统严格校验"这个 Run 的输入输出是否可复现"，但完全不校验
   "这个 Run 是否真的支持这句话"。claim 是自由文本，没有可核验断言，
   release gate 与具体 claim 之间没有任何绑定。

4. **不适合 Agent 独立使用**（详见 §3）。主要不是工具数量问题，
   而是"唯一能工作的路径被文档标为 legacy"+"错误消息不可行动"。

5. **需要减负，但减的是写入口和路径数量，不是概念。**
   `prd-system-simplification.md` 的四概念冻结是对的，应保留；
   过载出在同一意图有 3 条写路径、单条 claim 要走 ~14 步 2 次 review、
   node 类型系统分叉成两套方向规则。

### 审计方法

所有"已确认"结论都由临时 probe 测试在 `internal/store` 内实测得到（`newTestStore` +
真实 SQLite schema + 真实 helper），验证后已删除探针文件。
基线测试 `go test ./internal/store/ ./internal/api/ ./internal/mcp/` **全绿**——
即下列缺陷对现有测试套件完全不可见，这本身是需要修的问题（§7）。

---

## 1. 缺陷清单

### P0-1 v2 proposal 路径无法建立任何正式证据关系 ⛔ 已确认

**位置**：`internal/store/evidence_workspace.go:372`

```go
readinessPlan := &EvidenceGraphProposalPlan{Blockers: make([]EvidenceGraphBlocker, 0)}
if err := s.appendEvidenceEligibilityBlockers(ctx, &merged, patch, readinessPlan); err != nil {
```

`readinessPlan` 是新建的空结构，`ProjectID` 为 `""`。
`appendEvidenceEligibilityBlockers` 把它转手传给
`formalRunEvidenceBlockers(ctx, plan.ProjectID, uses)`，其中：

```go
if strings.TrimSpace(run.ProjectID) == "" || run.ProjectID != projectID {
    blockers = append(blockers, ...RUN_PROJECT_MISMATCH...)
    continue                     // ← 后续真实 readiness 检查全部被跳过
}
```

`projectID == ""` 时这个条件**恒为真**：`run.ProjectID` 为空命中前半句，
非空命中后半句。于是任何带 `supports` / `weakens` / `does_not_prove` 边的 v2 proposal
100% 被 block，且 `continue` 使得真正的 readiness 诊断永不产生。

**实测**：完全就绪的 Run（`CheckRunClaimReadiness` 返回 0 blocker）+ released snapshot，
经 `aexp_create_evidence_proposal` 提 `run --supports--> claim`：

```
precondition OK: run is formal, provenance-complete, and has a released snapshot
  blocker RUN_PROJECT_MISMATCH: run "run_promotion_p5" does not belong to project ""
accept err = proposal has blockers; run the side-effect-free plan first
LEGACY path on the identical edge: eligible=true blockers=[]
```

**影响面**

- `acceptance-evidence-workspace-v2.md` 的 **GATE-04（P1，正式证据通过）在 v2 路径上不成立**。
- **GATE-02 的现有测试通过但结论无效**：
  `TestEvidenceWorkspaceFormalBlockerIdentifiesEdgeNodeAndRun` 只断言
  "存在带 node/edge/run ID 的 blocker"，实测该 blocker 就是这个 bogus 的
  `RUN_PROJECT_MISMATCH`，而不是预期的 `dataset_missing` / `seeds_missing` /
  `EVIDENCE_RELEASE_MISSING`。测试给了假信心。
- `PlanEvidencePromotion` 和 legacy `PlanEvidenceGraphProposal` 都正确传了 projectID，
  所以**只有推荐路径是坏的**。

**修复**：不要把 projectID 藏在 plan 结构里传递，改成显式参数，让编译器强制调用方提供。

```go
func (s *SQLite) appendEvidenceEligibilityBlockers(
    ctx context.Context, projectID string, merged *EvidenceChainGraph,
    patch EvidenceGraphPatch,
) ([]EvidenceGraphBlocker, error)
```

三个调用点（v2 plan、legacy plan、promotion plan）各自传入自己已解析出的
`chain.ProjectID` / `proposal.ProjectID` / `source.ProjectID`。

---

### P0-2 直接写图完全绕过 formal gate ⛔ 已确认

**位置**：`internal/api/server.go:283` → `handleSaveEvidenceChainGraph` →
`SaveEvidenceChainGraphCAS`（`internal/store/sqlite.go:3470`）

`SaveEvidenceChainGraphCAS` 校验：图结构、run 存在性、run 跨项目、project card 匹配、
map reference。**不校验** `formalRunEvidenceBlockers` —— 即不校验 evidence grade、
dataset 是否 verified、seeds、git commit、config hash、split/eval protocol、
是否有 released snapshot。

**实测**：一个 exploratory grade、无 dataset、无 seeds、无 snapshot 的 Run，
经该入口写入 **Primary Map** 的 `run --supports--> claim`，成功，revision 递增为 1。

这直接违反 `prd-system-simplification.md` §1.1 的决策：

> Agent 可通过 add-node/add-edge 直接改 accepted graph → **默认 Agent 只能提交 proposal；
> accepted graph 继续经过 revision-aware review**

`deprecation-ledger.md` 把该能力记为"direct Evidence create/add-node/add-edge/list CLI →
hidden expert compatibility"，但收口只做在 CLI 层，UI 使用的 REST 入口仍然是开放的
全量语义写。任何持有 token 的 Agent 都可以调这个 REST 端点。

**次要问题**：`expected_revision` 缺省时 handler 取 `-1`，`replaceEvidenceGraphTx` 对
`ExpectedRevision < 0` 跳过 CAS，即盲写覆盖。UI 目前确实传了 revision
（`web/src/api.ts:522`），所以实际不触发，但 REST 契约上留着这条无保护路径。

**可信的缓解**：`evidence_chain_revisions` 保留每个语义 revision 的完整 `graph_json` +
actor + source_kind，所以历史是 append-only 可审计的。图本身可变，但改动有痕。
这一点设计是对的，应保留。

**修复**

1. `PUT /evidence-chains/{id}/graph` 降级为**仅布局**入口：只接受
   `x/y/width/height/pinned` 的变更，检测到任何语义 diff（节点/边的增删、type、title、
   body、run_id、rationale 变化）即返回 `SEMANTIC_WRITE_REQUIRES_PROPOSAL`。
   `CanonicalEvidenceGraph` 已经排除坐标和 pin，所以判据现成：
   `新 hash == 当前 hash` 才允许写。
2. `expected_revision` 改为必填。
3. 把 `formalRunEvidenceBlockers` 移到 `replaceEvidenceGraphTx` 之前的共享校验里，
   使任何写路径都过同一个门。

---

### P1-3 topic map 松校验 + proposal 严校验 → 图可被写成永久不可提案状态 ⛔ 已确认

`SaveEvidenceChainGraphCAS` 用 `validateEvidenceChainGraph(&graph, chain.Role == "primary")`，
即**只有 Primary 走严格 DAG/方向校验**；Topic Map 允许环、允许非法方向、允许重复 run 节点。

但 `PlanEvidenceProposal` 对**所有** Map 都用 `ValidateEvidenceChainGraph`（严格），
且校验对象是 `merged = 现有图 + patch`。

**实测**：向 Topic Map 直接写入 `c1 --supersedes--> c2 --supersedes--> c1` 成功；
随后向该 Topic 提交一个完全无关且合法的 note 节点 proposal：

```
HOLE CONFIRMED: blockers=[{Code:GRAPH_CYCLE Message:semantic evidence edges must form a
directed acyclic graph NodeID: EdgeID: RunID:}]
```

Topic Map 从此**永久不可提案**：任何 proposal 都被既存内容 block，
而修复既存内容的唯一手段（proposal）本身被 block。blocker 也没有 NodeID/EdgeID，
Agent 无法定位是哪条边。

**修复**：统一为一套校验（严格），对历史松散数据用一次性迁移标注而不是运行期分叉规则；
`GRAPH_CYCLE` 必须带上参与环的 node/edge ID；plan 需要区分
"blocker 来自 patch" 与 "blocker 来自既存图"，后者给出可执行的修复指引。

---

### P1-4 被 reject / expire 后，相同 patch 永远无法重新提交 ⛔ 已确认

`CreateEvidenceProposal`：

```go
if existing, err := s.getEvidenceProposalByHash(ctx, proposalHash); ... else if existing != nil {
    return existing, nil          // ← 不看 status
}
```

`canonicalWorkspaceProposal` 的 hash 输入不含时间戳，只含 project / target map /
base_revision / summary / routing_reason / sources / graph。

**实测**：创建 proposal → reject → 用完全相同的 patch 再次创建 → 返回的是那个
**status=rejected** 的旧 proposal，ID 相同，没有新 pending 产生，也没有报错。

**两个后果**

- Agent 拿到一个 `status=rejected` 的对象却以为提交成功，后续 plan 报
  `PROPOSAL_NOT_PENDING`，形成静默循环。
- 与 14 天 `PROPOSAL_EXPIRED` 组合出真死锁：若目标图 14 天内未产生新 revision
  （base_revision 不变 → hash 不变），该 patch 再也无法被提交为可接受的 proposal。

`SubmitEvidenceGraphProposal` 的 Run Card 路径有同样的 `existing.ProposalHash == proposalHash`
早返回，问题相同。

**修复**：hash 去重只在 `status IN (draft, pending)` 时复用（幂等重试的本意）；
命中 terminal 状态（rejected / expired / accepted）时，
要么创建新记录（hash 加 attempt 序号），要么返回结构化错误
`PROPOSAL_ALREADY_REJECTED` 并给出"需要改动 summary 或 patch"的指引。
`RerouteEvidenceProposal` 依赖 `created.ID == previous.ID` 判断，同一处修复后也要复查。

---

### P1-5 矛盾边可以共存 ⛔ 已确认

`validateEvidenceChainGraph` 的语义边去重键是
`source + "\x00" + target + "\x00" + type`。`supports`、`weakens`、`does_not_prove`
是三个不同 type，因此同一 `(run, claim)` 对上三条边可以同时存在。

**实测**：同一 Run 对同一 Claim 同时 `supports` + `weakens` + `does_not_prove`，
`ValidateEvidenceChainGraph` 通过。

对一个以"可引用研究结论"为目标的系统，这是语义完整性缺口而不只是洁癖问题：
导出/晋升时无法判定该 claim 的证据方向。

**修复**：对 `{supports, weakens, does_not_prove}` 建立互斥组，
去重键改为 `source + target + group`，冲突时返回
`CONTRADICTORY_EVIDENCE_EDGE` 并带上两条边的 ID。
真需要表达"同一 Run 部分支持部分反驳"时，应拆成两个更窄的 claim。

---

## 2. 门禁不是不变量（横切问题）

上面 5 条共享一个根因：**formal gate 只在 proposal accept 的瞬间、只对 patch 增量执行。**

`appendEvidenceEligibilityBlockers` 遍历的是 `patch.Edges`，不是 `merged.Edges`。
配合 P0-2 的开放写入口，结果是：

- 已 accepted 的图内容永不重新校验；
- 一条通过任意途径进入图的 ungated `supports` 边会永久留存；
- Run 的状态后续变化（snapshot release 被判 failed、dataset 被标记非 verified）
  不会反映到已建立的证据关系上；
- 因此"图中存在 `run --supports--> claim`"**不蕴含**"该 Run 满足 readiness policy"。

这使得 Agent 无法从图状态推断合法性，只能相信历史流程都走对了——
而 P0-1 证明历史流程恰好是坏的。

**建议**：增加一个不依赖 proposal 的全图一致性检查
`AuditEvidenceChain(ctx, chainID) []EvidenceGraphBlocker`，遍历 `merged` 全量语义边，
并暴露为 `aexp_audit_evidence_chain` 与 `aexp_doctor` 的一项。
它同时是 P0-2 修复后的存量数据清理工具。

---

## 3. Agent 适用性评估

**结论：当前状态不适合 Agent 独立使用。**核心问题按严重度：

| # | 问题 | Agent 侧表现 |
| --- | --- | --- |
| 1 | 规范路径不可用（P0-1） | 按文档"preferred"用 `aexp_create_evidence_proposal` 记录结论，永远失败 |
| 2 | 错误消息不可行动 | `run "X" does not belong to project ""` —— 项目归属其实是对的，Agent 会去反复"修"一个不存在的问题 |
| 3 | 两条路径描述与实际相反 | `aexp_propose_evidence_graph` 的 description 写着 "Legacy... Prefer aexp_create_evidence_proposal"，但**它是唯一能工作的**，且同样在默认工具集里 |
| 4 | blocker 缺少定位信息 | `GRAPH_CYCLE`、`DUPLICATE_SEMANTIC_EDGE` 等不带 node/edge ID，Agent 无法定向修 |
| 5 | 冲突后无 rebase | `REVISION_CONFLICT` 只能重造 proposal；`aexp_reroute_evidence_proposal` 只换 target map，不刷新 base revision |
| 6 | 静默返回 terminal 对象（P1-4） | 提交"成功"但状态是 rejected，Agent 无法从返回值判断 |
| 7 | 门禁非全量（§2） | Agent 读到的图不能自证合法，无法在其上做增量推理 |

**做得对的地方**（修复时不要破坏）：

- plan / apply 分离，`plan` 严格无副作用；
- `expected_plan_hash`（promotion）把"看到的计划"和"执行的计划"绑定 —— 这个模式应推广到
  proposal accept；
- canonical hash 排除坐标/尺寸/pin，布局变更不产生 revision 噪音；
- `evidence_chain_revisions` 完整快照 + actor + source_kind；
- blocker 有稳定 code（在带 ID 的地方）对 Agent 很友好。

---

## 4. 尚未被识别的语义缺口

这些不是实现 bug，是设计缺口。系统严格保证"可复现性"，几乎不保证"论证有效性"。

**4.1 claim 没有可核验断言。** `claim` 节点只有自由文本 `title` / `body`。
没有 metric、方向、效应量、基线、置信区间、seed 数。
"我们的方法好 12 个点"与 Run 实际 metrics 之间没有任何交叉校验。
Agent 可以写下与 Run 数据相反的 claim 并通过所有门禁。

**4.2 release gate 与 claim 无绑定。** `runHasReleasedEvidenceSnapshot` 判断的是
"该 Run 在该 Project 下**存在任意一个** released snapshot"。
于是一个 Run 一旦 release 过一次，就能永久支撑**任意数量、任意内容**的 claim，
包括与该 snapshot 完全无关的。建议：`supports` 边上必带
`snapshot_id` + 引用的 metric key，校验该 snapshot 确实 released 且确实含该 metric。

**4.3 图声明的输入与 Run 实际 provenance 不交叉校验。**
`dataset --uses--> run` 边不校验该 dataset 节点是否对应 `run.DatasetsJSON` 里记录的
dataset version。图上可以画着 ImageNet-1k，Run 实际跑的是别的数据。
对一个以 provenance 为立身之本的系统，这是最反直觉的缺口。
建议：`dataset`/`protocol` 节点带 `dataset_version_id`，`uses` 边校验其
∈ `run.DatasetsJSON`；反向也校验（Run 用到的 dataset 未在图中声明时给 warning）。

**4.4 n=1 即可成立。** 一条 `supports` 边就让 claim 成立。
没有重复次数、seed 数、基线对照的要求。`run_readiness.go` 要求 `seeds` 非空，
但不要求 `len(seeds) > 1`，也不要求存在对照 Run。
建议：不要硬阻断，而是给 claim 计算并持久化一个 `evidence_strength`
（支撑 Run 数、seed 数、是否有基线对照、是否有 weakens 边），导出时必须带上。

**4.5 负结果与正结果成本相同 → 系统性偏置。**
`weakens` 和 `does_not_prove` 与 `supports` 走完全一样的重型阶梯
（formal grade + verified dataset + released snapshot）。
记录"这个实验反驳了我们的假设"和记录"证明了"一样贵，
而"什么都不记"是免费的。对研究系统这是错误的激励方向。
建议：`weakens` / `does_not_prove` 指向**本项目自己的** claim 时降低门槛
（succeeded + git commit + seeds 即可），保留完整门槛给对外发布的正向 claim。

**4.6 supersede 不改变旧 claim 的可引用性。** `c_new --supersedes--> c_old`
只是一条边，`c_old` 仍可被新的 `supports` 边指向，导出时也不会自动标注已废弃。
建议：accept 时给被 supersede 的节点写入 `superseded_by`，
并禁止新增指向已被 supersede 的 claim 的语义边。

**4.7 accepted 图可被删节点。** 与 P0-2 同源：全量 PUT 可以删掉已 accepted 的
`supports` 边或改写 claim body。历史 revision 留痕，但当前图无 append-only 保证。

---

## 5. 复杂度评估：是否过于复杂

### 现状数字

| 维度 | 数量 |
| --- | --- |
| MCP 工具总数 / 默认暴露 | 87 / 34 |
| 默认暴露里 evidence 相关 | **17（占默认工具集 50%）** |
| evidence 相关 SQLite 表 | 8 |
| node 类型 / edge 类型 | 11 / 9（其中 4 个 node 类型走独立的宽松方向规则） |
| proposal 状态 | 7 |
| 并行的 proposal 生命周期 | 2（`project_run_cards` + `evidence_proposals`，各有独立 plan/review/accept 写图） |
| 一条 claim 进主图的最少步骤 | ~14 步、2 次人工 review |
| `internal/store/evidence_*.go` | 1236 行（含 1233 行测试） |

### 判断

**概念数量不是问题。** `prd-system-simplification.md` 的
`Project / Asset / Run / Evidence Map` 四概念冻结是对的，应保留，不要再减。
过载出在三个具体位置：

**(a) 同一意图 3 条写路径，且唯一可用的那条被标为 legacy。**
`aexp_create_evidence_proposal`（规范，坏的）、
`aexp_propose_evidence_graph` + Run Card（legacy，能用）、
`PUT /evidence-chains/{id}/graph`（无门禁）。
三条路径的校验规则各不相同（§1 的 P0-1 / P0-2 / P1-3 全部源于此）。
**这是最该减的一处，而且减完 bug 也一起消失。**

**(b) 单条 claim 的阶梯过重。** asset publish → submit run → output publish →
manifest final → snapshot create → release evaluate → topic map → proposal →
plan → accept → promotion plan → promotion create → plan → accept。
两次人工 review 之间没有新增信息量：topic 那次已经审过内容，
promotion 那次审的是同一批 node 的摘要。

**(c) 类型系统分叉。** `hypothesis` / `experiment` / `conclusion` / `note` 四个
legacy node 类型在 `allowedEvidenceDirection` 里有一套独立的宽松规则，
和 V1 的 7 个类型共存。同一个 `supports` 边的合法性取决于端点是不是 legacy 类型。

### 减负动作（按 收益/成本 排序）

| # | 动作 | 收益 | 成本 |
| --- | --- | --- | --- |
| D1 | `PUT .../graph` 降为仅布局；语义写只走 proposal | 消除 P0-2、P1-3 的写入源；门禁成为真不变量 | 小：判据就是 `hash == 当前 hash`；UI 需要把语义编辑改为提 proposal |
| D2 | 删除 Run Card 的 proposal 生命周期，只保留读投影 | 少一套并行 plan/review/accept；`propose_evidence_graph` / `propose_evidence_graph_patch` 两个工具下线 | 中：必须先修好 P0-1，否则删掉唯一能用的路径 |
| D3 | 合并 promotion 的第二次 review | ~14 步降到 ~11 步，1 次 review | 中：需要重新定义"topic accept 是否即授权进主图" |
| D4 | 4 个 legacy node 类型一次性迁移，删掉双套方向规则 | `allowedEvidenceDirection` 单一规则；P1-3 的严/松分叉消失 | 中：需要存量数据迁移脚本 + 无损校验 |
| D5 | evidence 默认工具 17 → 8 | Agent 默认工具集从 34 降到 25，evidence 占比 50% → 32% | 小：D2 完成后大部分自然消失 |

D5 的目标工具集：`create_topic_evidence_graph`、`list_project_evidence_graphs`、
`get_evidence_chain`、`create_evidence_proposal`、`plan_evidence_proposal`、
`review_evidence_proposal`、`create_evidence_snapshot`、`evaluate_evidence_release`。
其余（list/get proposal、reroute、promotion plan/create、snapshot list/get、release list）
转为按需暴露或合并进上述工具的返回值。

**注意 D2 与 P0-1 的顺序依赖**：先修 P0-1，确认 v2 路径可用并有正向测试覆盖，
再删 Run Card 写路径。反序会让证据链彻底不可写。

---

## 6. 建议实施顺序

**Stage 0 —— 止血（约半天）**

1. 修 P0-1：`appendEvidenceEligibilityBlockers` 改显式 `projectID` 参数，三个调用点补齐。
2. 补 **GATE-04 正向测试**：完全就绪的 Run + released snapshot，
   v2 路径 `run --supports--> claim` 必须 `eligible == true` 且 accept 成功。
3. 加固 GATE-02 测试：断言 blocker **code 集合**
   （`dataset_missing` / `seeds_missing` / `EVIDENCE_RELEASE_MISSING`），
   而不只是"存在带 ID 的 blocker"。
4. 加一条测试：`projectID == ""` 传入 `formalRunEvidenceBlockers` 必须是编译期不可能，
   或运行期 panic/error，而不是静默产出 `RUN_PROJECT_MISMATCH`。

**Stage 1 —— 门禁收口（1～2 天）**

5. D1：`PUT .../graph` 仅布局 + `expected_revision` 必填。
6. `formalRunEvidenceBlockers` 下移到所有写路径共用的校验点。
7. P1-4：hash 去重限定 `draft/pending`，terminal 状态返回结构化错误。
8. P1-5：矛盾边互斥组 + `CONTRADICTORY_EVIDENCE_EDGE`。
9. `AuditEvidenceChain` 全图校验 + `aexp_doctor` 集成 + 存量数据体检报告。

**Stage 2 —— 语义补强（按价值取舍，不必全做）**

10. 4.2：`supports` 边必带 `snapshot_id` + metric key，并校验。
11. 4.3：`dataset`/`protocol` 节点绑 `dataset_version_id`，与 `run.DatasetsJSON` 交叉校验。
12. 4.5：负结果降门槛（这条对研究质量的边际收益可能最高，成本最低）。
13. 4.4：`evidence_strength` 计算 + 导出必带。
14. 4.6：`superseded_by` + 禁止指向已废弃 claim。

**Stage 3 —— 减负**

15. D2 → D4 → D3 → D5，每步都要求存量数据无损迁移 + 现有 revision 可读。

---

## 7. 验收补强

现有套件对 §1 的 5 个缺陷全部为绿，说明验收标准写了但没有对应的可执行断言。
最少需要补上：

- **GATE-04 正向路径**在 v2 proposal 上的测试（当前完全缺失，只有 promotion 路径有）。
- 每条 blocker code 的**精确断言**，禁止用"存在任意 blocker"作为门禁通过的证据。
- **写路径等价性测试**：同一个非法图经 proposal / 直接写 / legacy Run Card 三条路径，
  必须得到相同的拒绝结论。这一条能一次性覆盖 P0-2 和 P1-3。
- **图不变量测试**：任意 accepted 图执行 `AuditEvidenceChain` 必须 0 blocker。
- **Agent 循环测试**：对每个 blocker code，断言存在一个 Agent 可执行的修复动作
  使其消失。`RUN_PROJECT_MISMATCH（project ""）` 这类不可修复的错误应该被这条测出来。
