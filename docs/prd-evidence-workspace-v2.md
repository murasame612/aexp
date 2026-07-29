# PRD：aexp Evidence Workspace V2

状态：Proposed  
版本：2.0  
日期：2026-07-25  
适用范围：Project、Evidence Map、Evidence Proposal、MCP、API、UI-v2、SQLite

配套验收标准：
[acceptance-evidence-workspace-v2.md](acceptance-evidence-workspace-v2.md)。

本 PRD 覆盖以下旧规则：

- `prd-system-simplification.md` 中“无法确定目标时写入 primary Map”的规则；
- `prd-research-evidence-graph.md` 中“提案必须由单个 Run 的 Project Run Card 承载”的规则；
- 任何把 primary Map 当作默认收件箱、把 secondary Map 当作默认只读历史图的规则。

旧系统的图修订、CAS、确定性 hash、不可变历史、审核边界和 provenance 安全承诺继续有效。

## 1. 摘要

Evidence Workspace V2 的目标不是增加更复杂的自动路由，也不是重新开发一套知识库。它首先
解决一个更基础的问题：

> 用户和 Agent 必须能够在没有 Run、没有 Dataset、没有 formal claim 的情况下建立第一张
> 可用研究图，并在后续实验中持续维护它。

Project 是研究范围。一个 Project 拥有一个 Evidence Workspace，其中包括：

```text
Project
└── Evidence Workspace
    ├── Primary Map
    │   ├── 项目级关键结论
    │   ├── 当前路线、主要问题和下一步
    │   └── Topic Map 引用
    └── Topic Maps
        ├── 某个研究问题
        ├── 某组协议或数据演化
        └── 某条消融、对照或失败路线
```

Primary Map 是项目决策索引，不是默认提案收件箱。Topic Map 承载详细推理。Agent 根据研究
语义和用户在对话中的指令，自行选择已有 Topic 或创建新 Topic。aexp 不建设隐藏的自动打分
路由器，但必须阻止无目标、跨项目和孤立图写入。

本版本的第一优先级是完成真实首图闭环：

```text
创建 Topic
→ Agent 提交 bootstrap proposal
→ UI 显示草稿叠加
→ 用户审核
→ 图被接受并在刷新后稳定显示
→ Primary Map 引用该 Topic
```

## 2. 当前问题

现有系统已经具备图节点、类型化边、自动排版、revision、graph hash 和 proposal review，
但仍不能稳定完成第一张图：

1. Proposal 依附单个 Project Run Card，导致没有 Run 时无法起图；
2. formal Run 的 provenance blocker 可能使整张初始图看起来完全不可用；
3. 未指定目标图时默认写入 primary，详细运行会污染项目总览；
4. Evidence Map 可以缺少 `project_id`，从而产生项目页不可见的孤立图；
5. primary 和 Topic 之间没有正式的引用节点，只能复制内容或把 graph ID 塞进自由 JSON；
6. 同一个 Run 只能拥有一份图 proposal，无法先补充 Topic，再向 primary 晋升摘要；
7. UI 能显示审批抽屉，但用户不能可靠完成“创建、预览、接受、刷新后仍存在”的完整首图流程；
8. Evidence Chain、Evidence Graph、Evidence Map 等名称混用，进一步放大了心智负担。

系统当前的问题不是 Agent 不够聪明，而是系统没有提供稳定的图所有权、独立提案和主从引用
原语。

## 3. 产品定义

### 3.1 Evidence Workspace

Evidence Workspace 是 Project 内的研究推理空间。它不是独立顶层产品，也不能替代 Project。

Project 继续负责：

- canonical `project_id`；
- Runs 和 Assets 的归属；
- 项目配置、聚合器和 release policy；
- Evidence Workspace 的边界。

Evidence Workspace 负责：

- Primary Map；
- Topic Maps；
- Evidence Proposals；
- Map 间引用和关键结论晋升；
- accepted graph revision 和审核历史。

### 3.2 Primary Map

每个 Project 恰好有一个 `active + primary` Map。

Primary Map 只保存能够帮助用户理解和决定项目方向的内容：

- 当前有效的项目级 claim；
- 已确认的重大 issue；
- 当前路线和 next step；
- 被新协议或新数据取代的关键结论；
- Topic Map 的摘要引用。

Primary Map 不保存：

- 每条普通 Run；
- 每个 seed；
- 完整超参数；
- 全部实验产物；
- 只对单个局部问题有意义的中间推理；
- 无法概括为项目级变化的草稿。

系统不得因为 Agent 未指定 Map 就自动把 proposal 投入 Primary Map。

### 3.3 Topic Map

一个 Project 可以拥有多张 active Topic Map。每张 Topic Map 必须声明：

- `title`：用户可读标题；
- `purpose`：这张图回答什么研究问题；
- 可选 `routing_hints`：recipes、keywords、datasets、protocols 或 claims；
- `status`：active 或 archived；
- `project_id`：不能为空。

Topic Map 可以承载：

- dataset 和 protocol 演化；
- baseline 和 treatment 的公平比较；
- 关键 ablation；
- 负对照和失败路线；
- issue、修复、结论和下一步；
- 有限数量、真正改变决策的 Run。

Topic Map 不是一个 Run 文件夹。Agent 不应把所有 Run 自动加入图中。

### 3.4 Evidence Proposal

Evidence Proposal 是 Agent 或用户对某张 Map 的待审核语义变更。它成为独立一等记录，不再
依附单个 Project Run Card。

Proposal 必须包含：

- `proposal_id`；
- `project_id`；
- `target_map_id`；
- `base_revision`；
- `actor`；
- `summary`；
- `nodes` 和 `edges`；
- 可选 `source_run_ids`；
- 可选 `source_snapshot_ids`；
- `status`；
- proposal hash、创建时间和审核信息。

一个 Proposal 可以：

- 在没有 Run 时建立 dataset、protocol、hypothesis、issue、plan 或 claim 草稿；
- 引用一个或多个 Run；
- 为 Topic 增加详细证据；
- 为 Primary 增加项目级摘要或 Topic 引用。

Project Run Card 降为兼容投影。旧 Card 中的 graph patch 必须迁移或适配为独立 Proposal，
不能继续成为新写入的唯一入口。

### 3.5 Map Reference

Primary Map 通过一等 `map_ref` 节点引用 Topic Map。引用至少包含：

- `target_map_id`；
- `target_revision`；
- `target_graph_hash`；
- 可选 `target_node_ids`；
- 用户可读摘要；
- source status：current、stale 或 archived。

点击 `map_ref` 必须进入目标 Topic。目标 Topic 更新后，旧引用仍保留其审核时固定的 revision
和 hash，并显示“有更新可查看”，不能静默漂移。

Map 引用只允许发生在同一 Project 内。引用关系必须无环；Topic 不得反向引用 Primary 形成
循环。

### 3.6 Promotion

Promotion 是从已接受的 Topic 内容向 Primary 提交项目级摘要的动作。

Promotion：

- 创建新的独立 Proposal；
- 引用 Topic Map 的确定 revision/hash 和关键节点；
- 默认只创建一个摘要 claim/issue/plan 与一个 `map_ref`；
- 不复制 Topic 中的全部 Run 和细节；
- 仍需用户审核；
- Topic 原内容不被修改。

`should_promote` 不能只是展示字段，必须对应一个可计划、可审核、可幂等执行的动作。

## 4. Agent 与用户的分工

### 4.1 Agent 自主维护 Topic

Agent 在准备 Evidence Proposal 时：

1. 获取当前 Project 的 Map 列表、用途和状态；
2. 根据当前研究问题选择最合适的 Topic；
3. 没有合适 Topic 时创建新 Topic；
4. 用户在对话中指定 Map 时使用该 Map；
5. 提交时记录简短 `routing_reason`；
6. 无法判断且创建新 Topic 也不合理时，保留未路由草稿并询问用户。

aexp 不需要对 Map 做隐藏的 embedding 检索、关键词评分或自动分类。Agent 的语义判断和对话
上下文是主要路由机制。

### 4.2 系统必须保证的边界

Agent 自主不等于系统放弃约束。aexp 必须保证：

- Proposal 必须显式包含 `target_map_id`；
- target Map 必须属于 Proposal 的 Project；
-所有引用的 Runs、Cards、Snapshots 必须属于同一 Project；
- active Map 不允许 `project_id` 为空；
- Primary Proposal 必须声明项目级影响；
- Topic Proposal 必须记录路由原因；
- 不存在默认写入 Primary 的后端兜底；
- 用户能够在审核前更改目标 Map，但系统必须重新计划 revision 和 blockers。

## 5. 第一张图的启动规则

### 5.1 不要求先有数据或实验

创建 Project 后，用户或 Agent 可以立即：

- 创建 Topic；
- 添加研究问题、hypothesis、已知 protocol、issue 和 plan；
- 建立初始关系；
- 审核并接受第一版图。

上述操作不要求：

- DatasetVersion；
- Run；
- Artifact；
- Snapshot；
- Release。

### 5.2 Provenance 门禁的作用范围

Provenance 门禁只保护证据强度，不阻止研究过程本身被记录。

无需 formal provenance 即可接受：

- hypothesis；
- issue；
- plan；
- dataset/protocol 的描述性节点；
- 未与 formal claim 建立支持关系的历史 Run；
- 明确标记为 unverified 或 legacy 的背景节点；
- `reveals_issue`、`next_step` 和非声明性引用。

必须通过 formal provenance 才能接受：

- `supports` formal claim；
- `weakens` formal claim；
- `does_not_prove` formal claim；
- Promotion 为当前项目级正式结论；
- Release 或论文 claim 所消费的证据关系。

如果一个 Proposal 同时包含安全内容和被阻断的 formal assertion，Plan 必须逐项指出阻断对象。
Agent 或用户可以生成一个移除被阻断关系的新 Proposal；V2 不要求在单次事务中进行隐式部分
接受。

### 5.3 Bootstrap Proposal

空 Topic 的第一份 Proposal 称为 bootstrap proposal。它可以由 Project 上下文或用户对话
产生，`source_run_ids` 可以为空。

Bootstrap Proposal 接受后必须产生 revision 1。刷新 UI、重启服务和重新读取数据库后，
节点、关系、hash 和 revision 必须保持一致。

## 6. 状态与不变量

### 6.1 Map 状态

```text
active → archived
```

Archived Map 只读。恢复 archived Map 不在 V2 P0 范围内。

每个 Project：

- 恰好一个 active Primary；
- 零到多张 active Topic；
- 零到多张 archived Map；
- 不得存在无 Project 的 active Map。

### 6.2 Proposal 状态

```text
draft → pending → accepted
                → rejected
                → expired
                → conflicted
```

- `draft` 可以没有 target Map，用于 Agent 尚未完成路由的本地或持久草稿；
- 进入 `pending` 前必须确定 target Map 和 base revision；
- accepted/rejected/expired Proposal 不可覆盖；
- target Map revision 变化后，旧 pending Proposal 进入 conflicted 或在 Plan 中返回冲突；
- 重新路由必须产生新的 proposal hash 和新的 base revision，不得静默修改审核历史。

### 6.3 写入不变量

- accepted graph 只通过 revision-aware review 或用户 revision-aware 编辑写入；
- Proposal 提交和 Plan 无副作用；
- acceptance 原子更新 Map、revision 和 Proposal；
-同一 proposal hash 幂等；
- Map semantic hash 不包含布局；
- Project、Map、Proposal 和引用对象的归属必须一致；
- 删除 Project 或 Map 不得级联删除 Runs、Snapshots 或历史 revision。

## 7. UI-v2

### 7.1 Project 内唯一入口

Evidence Workspace 只从 Project 的“证据图”页签进入。全局 Evidence 入口只能作为项目索引
和跳转，不提供脱离 Project 的创作画布。

### 7.2 Map 切换

顶部 Map selector 显示：

- Primary；
- active Topics；
- archived 数量；
- 新建 Topic；
- Map purpose 和当前 revision。

Primary 与 Topic 必须有清晰文字标签，不能只依靠颜色区别。

### 7.3 空状态

空 Project 或空 Topic 必须显示一个明确操作：

```text
还没有研究图
可以在没有实验和数据的情况下先建立研究问题、协议和下一步。
[让 Agent 起草] [手动添加节点]
```

不能只显示空白画布。

### 7.4 Proposal 预览与审批

画布上直接以半透明方式预览 pending Proposal。审核面板显示：

- 目标 Map；
- Agent 的 routing reason；
- base/current revision；
- 新增节点和关系；
- 每个 blocker 对应的节点或边；
-接受、拒绝、重新路由。

接受后，草稿样式消失，节点成为 accepted graph 的正常内容。不得要求用户离开项目页再进入另
一个审批入口。

### 7.5 Map Reference

Primary 中的 Topic 引用采用紧凑卡片显示：

- Topic 标题和 purpose；
-固定 revision；
-关键节点数量或摘要；
-是否有更新；
-“进入 Topic”操作。

引用卡片默认折叠详细节点，避免 Primary 被 Topic 内容撑大。

## 8. API、CLI 与 MCP

### 8.1 API

建议的规范操作：

```text
POST /projects/{project_id}/evidence-proposals
POST /evidence-proposals/{proposal_id}/plan
POST /evidence-proposals/{proposal_id}/review
POST /evidence-proposals/{proposal_id}/reroute
GET  /projects/{project_id}/evidence-proposals
POST /evidence-maps/{map_id}/promotions/plan
POST /evidence-maps/{map_id}/promotions
```

现有 Run-scoped proposal API 在迁移期作为适配器保留，但内部写入独立 Proposal。

### 8.2 MCP

Agent 的最小工具集：

- `aexp_list_project_evidence_graphs`
- `aexp_create_topic_evidence_graph`
- `aexp_propose_evidence_graph`
- `aexp_plan_evidence_graph_proposal`
- `aexp_review_evidence_graph_proposal`
- `aexp_promote_topic_summary`

`aexp_propose_evidence_graph` 接受 `project_id` 和可选 `source_run_ids`，不再要求必须提供
`run_id`。用户指定 Map 时，Agent 使用明确 `map_id`；工具响应必须回显目标 Map 标题、
project_id 和 revision，降低误投风险。

### 8.3 CLI

CLI 至少支持：

```bash
aexp evidence topic create --project PROJECT --title TITLE --purpose PURPOSE
aexp evidence proposal create --project PROJECT --map MAP --file patch.json
aexp evidence proposal plan PROPOSAL_ID
aexp evidence proposal review PROPOSAL_ID --accept
aexp evidence topic promote MAP_ID --nodes NODE_ID,...
```

## 9. 迁移

### 9.1 孤立图

现有 `project_id` 为空的 active Map 必须进入迁移清单：

- 能通过 Runs、Project Cards 或用户确认唯一确定 Project 的，显式绑定；
- 无法确定归属的，归档为 legacy，不出现在普通项目画布；
- 不得按标题进行静默归属；
- Proposal 指向孤立图时保持 pending/conflicted，不得自动改投 Primary。

### 9.2 旧 Project Run Card Proposal

旧 Card proposal 迁移为独立 Proposal，并保留：

- 原 run_id；
- proposal hash；
- target Map；
- base revision；
- status 和审核时间；
-原始 patch JSON。

迁移不得自动接受、拒绝或重路由任何 Proposal。

### 9.3 兼容边界

旧 Evidence Chain API 和 UI 数据保持可读。新写入只使用 Project-scoped Map 和独立 Proposal。
兼容适配层应有移除日期，不继续增加新功能。

## 10. 非目标

V2 不包含：

- embedding 或 LLM 自动 Map 分类服务；
- 自动替用户接受 Proposal；
- 把所有 Run 自动转成图节点；
- 通用知识库或 Obsidian 替代品；
-跨 Project Evidence 引用；
-自动修改历史 Run provenance；
-隐式部分接受一个 Proposal；
-论文写作和全文 claim 抽取；
-重新设计 NAS、Transfer、Dataset 或 Run 执行层。

## 11. 实施顺序

### P0：先做出第一张图

1. 强制 active Map 的 Project 归属；
2. 增加 Project-scoped、Run-optional Proposal；
3. 支持 bootstrap proposal；
4. 按节点/边语义执行 provenance gate；
5. UI 完成空状态、半透明预览、审核和接受闭环；
6. 使用真实 Project 完成首图验收。

### P1：主从图

1. 增加 `map_ref`；
2. Primary 引用 Topic；
3. 增加 revision pin、更新提示和 drill-down；
4. 增加 Promotion Proposal。

### P2：收口兼容入口

1. Project Run Card 变为只读投影；
2. 归档或绑定孤立图；
3. 关闭无 Project 的新 Evidence 写入口；
4.统一 UI、API 和文档中的 Evidence Map 术语。

## 12. 成功指标

V2 成功不是以新增多少节点类型衡量，而以以下结果衡量：

- 新 Project 可以在没有数据和 Run 时建立第一张图；
- 一个真实 Project 的首图可以完成 proposal、预览、审核、接受和重载；
- Agent 可以维护多张 Topic，但不会因遗漏目标而污染 Primary；
- Primary 只显示关键结论、路线和 Topic 引用；
- formal provenance blocker 只阻止正式证据声明，不阻止 issue、plan 和研究上下文；
- 项目页不再出现“目标图不可用但数据库里其实存在”的孤立状态；
- 用户能从 Primary 一次点击进入详细 Topic。

## 13. Definition of Done

本 PRD 只有在以下条件全部满足时才算完成：

1. 配套验收标准中的 P0 和 P1 条目全部通过；
2. 使用生产数据库备份完成无损迁移演练；
3. 使用一个真实 Project 建立并保存第一张可用图；
4. Primary 成功引用至少一张 Topic；
5. Go、UI-v2、API、CLI 和 MCP 测试通过；
6. 重新编译并替换生产二进制，服务重启后完成回归检查；
7. 未修改任何历史 Run provenance 或远端实验数据；
8. 未完成的兼容清理被明确列入后续计划，且不再暴露新的双写入口。
