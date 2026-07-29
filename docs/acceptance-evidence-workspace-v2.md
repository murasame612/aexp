# aexp Evidence Workspace V2 验收标准

状态：Binding acceptance criteria  
版本：2.0  
日期：2026-07-25

本文件是
[prd-evidence-workspace-v2.md](prd-evidence-workspace-v2.md)
的完成合同。除明确标为人工视觉验收的项目外，每条标准必须有自动化测试或可重复执行的验证
记录。“接口返回 200”“页面可以打开”或“Agent 理论上会选择正确”不能单独视为通过。

## 1. 验收优先级

- `P0`：建立第一张可用图所必需，全部通过后才能宣称首图闭环完成；
- `P1`：主图引用 Topic 和结论晋升所必需，全部通过后才能宣称主从图完成；
- `P2`：兼容收口，可以在不产生新双写的前提下分阶段完成。

## 2. Project 与 Map 归属

### OWN-01 active Map 必须属于 Project（P0）

创建或更新 active Primary/Topic Map 时：

- `project_id` 为空必须被拒绝；
-不存在的 Project 必须被拒绝；
-API、CLI 和 MCP 返回稳定 blocker code；
-数据库不产生孤立 active Map。

### OWN-02 Primary 唯一（P0）

同一 Project 在并发创建下仍恰好只有一个 `active + primary` Map。第二次创建幂等返回已有
Primary 或返回结构化冲突，不产生重复记录。

### OWN-03 多 Topic（P0）

同一 Project 可以创建至少三张 active Topic Map。每张 Topic 的 title、purpose、status、
project_id 和 revision 均可读取，彼此不共享节点或 revision。

### OWN-04 严格同项目写入（P0）

Proposal 的 Project、target Map、source Runs、source Snapshots 和 Map References 必须属于
同一 Project。任一对象跨项目或缺失归属时：

- Plan 返回精确 blocker；
- Accept 被拒绝；
- accepted graph、revision 和 Proposal 状态均不改变。

### OWN-05 孤立图迁移（P0）

在生产数据库备份上：

-所有 `project_id=''` 的 active Map 都进入迁移报告；
-可唯一确认归属的 Map 通过显式映射绑定；
-不能唯一确认的 Map 被归档为 legacy；
-行数、节点、边和 revision 不丢失；
-迁移不按标题静默猜测；
-`PRAGMA integrity_check` 返回 `ok`。

## 3. 第一张图

### BOOT-01 无 Run、无 Dataset 创建 Topic（P0）

给定一个没有 Run、Dataset、Artifact 或 Snapshot 的 Project，用户或 Agent 可以创建一张
Topic Map。创建成功后：

- Map 属于该 Project；
-role 为 topic/secondary；
-revision 为 0；
-graph hash 表示空图；
-Project Evidence 页面可以定位该 Map。

### BOOT-02 Bootstrap Proposal（P0）

针对 BOOT-01 的空 Topic，提交一个不含 `source_run_ids` 的 Proposal，内容至少包括：

-一个 hypothesis；
-一个 protocol；
-一个 issue；
-一个 plan；
-至少三条合法语义关系。

Plan 必须 eligible，提交和 Plan 不修改 accepted graph。

### BOOT-03 审核产生第一版图（P0）

接受 BOOT-02 后：

-Proposal 变为 accepted；
-Map revision 从 0 变为 1；
-节点和边只写入一次；
-graph hash 非空且可重复计算；
-revision snapshot 包含 actor、source proposal 和 canonical graph；
-刷新页面、重启服务并重新查询后内容一致。

### BOOT-04 空状态不是白屏（P0，自动化 + 视觉）

空 Project 和空 Topic 均显示明确说明和操作：

-可以在没有实验或数据时建图；
-“让 Agent 起草”；
-“手动添加节点”。

画布不得只有空白背景，也不得要求先提交 Run。

### BOOT-05 真实 Project 首图（P0，记录式验收）

选择一个现有真实 Project，在不修改历史 Run provenance 的前提下：

1. 创建或绑定一张 Topic；
2. 提交 bootstrap/context Proposal；
3. 在 UI-v2 查看半透明草稿；
4. 完成 Plan 和人工审核；
5. 接受 Proposal；
6. 刷新 UI 并重启服务；
7. 再次读取相同 revision/hash；
8.截屏或保存结构化验收记录。

Smoke 数据只能用于验证系统闭环，验收记录不得把 smoke 当作科研结果。

## 4. Proposal 模型

### PROP-01 独立 Proposal（P0）

Evidence Proposal 拥有独立 ID 和持久记录，可以没有 `run_id`。删除或修改 Project Run Card
不能删除、覆盖或改变 Proposal。

### PROP-02 多来源（P0）

一个 Proposal 可以引用零个、一个或多个 source Runs。所有 Run 必须同属 Proposal Project。
来源顺序变化不改变 proposal hash。

### PROP-03 显式目标（P0）

进入 pending 状态前必须有 `target_map_id`。后端不得在 target 缺失时自动选择 Primary。

API、CLI 和 MCP 响应必须回显：

-target Map ID；
-title；
-role；
-project_id；
-base revision。

### PROP-04 Draft 与未路由状态（P0）

Agent 暂时无法选择 Map 时可以保存 draft。Draft：

-不出现在任何 Map 的画布叠加中；
-不能被接受；
-不改变任何 Map；
-路由后产生 target/base revision 和新的 proposal hash。

### PROP-05 用户指定 Map（P0）

当调用者提供明确 Map ID 时，系统使用该 Map，不进行标题替换或 Primary 回退。Map 不属于
当前 Project 时返回 `GRAPH_PROJECT_MISMATCH`。

### PROP-06 Agent 创建 Topic（P0）

MCP 工作流可以：

1. 列出 Project Maps；
2. 创建带 title 和 purpose 的 Topic；
3.向新 Topic 提交 Proposal；
4.读取 Plan。

流程中不要求浏览器操作，也不要求 Dataset 或 Run。

### PROP-07 幂等与并发（P0）

-相同 Proposal 内容重复提交返回同一 proposal ID/hash；
-同一 Proposal 只能被接受一次；
-base revision 过期返回 conflict；
-冲突不修改图或审核历史；
-两个 Proposal 并发写同一 revision 时最多一个成功。

### PROP-08 旧 Run Card 适配（P2）

旧 Run-scoped API 仍可读取和计划历史 Proposal，但新写入落到独立 Proposal 表。适配前后：

-proposal hash 不变；
-原 run_id、status、base revision 和审核时间保留；
-不自动接受、拒绝或重路由。

## 5. Agent 路由边界

### ROUTE-01 不建设隐藏自动路由（P0）

服务端不根据标题、关键词或 embedding 静默选择 target Map。测试必须证明：

-缺少 target 的 pending 请求被拒绝或保持 draft；
-不会写入 Primary；
-不会创建未知 Topic。

### ROUTE-02 路由说明（P0）

投递 Topic 时 `routing_reason` 非空。该字段是审计信息，不由服务端进行语义正确性评分。

### ROUTE-03 Primary 不是 fallback（P0）

没有合适 Topic、多个 Topic 均可能匹配或 Agent 忘记指定 target 时，accepted Primary 的
节点、边、revision 和 hash 均保持不变。

### ROUTE-04 Primary 写入意图（P1）

直接向 Primary 提交 Proposal 时必须包含 `project_level_impact` 或 Promotion 来源。普通
Run 详情 Proposal 缺少该字段时返回 `PRIMARY_SCOPE_REQUIRED`。

## 6. Provenance 门禁

### GATE-01 无 provenance 的安全内容（P0）

缺少 DatasetVersion 的历史 Run 仍可以作为 `legacy/unverified` 背景节点进入 Topic，只要
Proposal 不建立 formal claim 的 supports、weakens 或 does_not_prove 关系。

以下内容不因 Dataset 缺失被阻断：

-hypothesis；
-issue；
-plan；
-dataset/protocol 描述；
-reveals_issue；
-next_step；
-Map Reference；
-非声明性的历史背景。

### GATE-02 formal assertion（P0）

当 unverified、smoke、pilot 或 provenance 不完整的 Run 通过 supports、weakens 或
does_not_prove 指向 formal claim 时：

-Plan 返回对象级 blocker；
-blocker 包含 source node/edge ID 和 Run ID；
-Accept 被拒绝；
-安全内容也不被隐式部分写入。

### GATE-03 修订后的 Proposal（P0）

移除 GATE-02 中被阻断的 formal assertion、保留 issue/plan/context 后提交新 Proposal：

-新 proposal hash 与旧值不同；
-Plan eligible；
-接受后保留安全上下文；
-旧 Proposal 保持 rejected/expired/pending 历史，不被覆盖。

### GATE-04 正式证据通过（P1）

formal Run 只有在 Project、Dataset revision、manifest SHA-256、seeds、Git、config、split、
evaluation protocol 和 released Snapshot 满足共享 readiness policy 时，才能向 formal
claim 建立正式关系或被 Promotion 使用。

## 7. Map Reference

### REF-01 创建引用（P1）

Primary Proposal 可以创建 `map_ref` 节点，至少固定：

-target Map ID；
-target revision；
-target graph hash；
-摘要；
-可选 target node IDs。

接受后引用可读取、可导出并可在 UI 中导航。

### REF-02 同项目与引用完整性（P1）

Map Reference 指向不存在、跨项目或非预期 revision/hash 时 Plan 被阻断。不得仅在
`data_json` 中保存一个未经校验的 graph ID。

### REF-03 引用图无环（P1）

系统拒绝：

-Topic 引用 Primary 并形成回路；
-Topic A 与 Topic B 互相引用；
-Primary 引用自身。

引用校验失败不改变任何 Map revision。

### REF-04 Revision pin（P1）

Topic 从 revision N 更新到 N+1 后：

-Primary 引用仍固定 N/hash-N；
-UI 显示目标 Topic 有更新；
-用户可以查看 N 或进入当前 N+1；
-引用不会静默更新。

### REF-05 Topic 归档（P1）

Topic 被归档后，Primary 引用仍可读取，并显示 archived。归档不得删除引用、Topic revision
或历史节点。

## 8. Promotion

### PROMOTE-01 Plan 无副作用（P1）

针对已接受 Topic 节点创建 Promotion Plan，返回：

-source Map/revision/hash；
-source node IDs；
-目标 Primary revision；
-摘要节点和 Map Reference；
-所有 blockers。

Plan 不修改 Topic、Primary 或 Proposal。

### PROMOTE-02 独立审核（P1）

Promotion 创建独立 Primary Proposal。接受后：

-Primary 增加摘要和引用；
-Topic 不变；
-只增加一次 Primary revision；
-来源 revision/hash 写入 revision snapshot；
-相同输入幂等。

### PROMOTE-03 不复制细节（P1）

Promotion 默认不把 Topic 的普通 Run、seed、超参数和全部关系复制进 Primary。自动化测试
应断言 Primary 仅增加计划中的摘要、引用及必要关系。

### PROMOTE-04 来源更新（P1）

Topic 结论被 superseded 后，旧 Primary 摘要不会自动改写。系统生成“来源有更新”的状态，
由新的 Promotion Proposal 完成修订。

## 9. UI-v2

### UI-01 Project 内唯一创作入口（P0）

Evidence 画布位于 Project 全屏工作区。全局入口只做 Project/Map 索引和跳转，不提供无
Project 创作。

### UI-02 Map selector（P0）

selector 清楚区分 Primary、Topic 和 archived，显示 title、purpose、revision。创建 Topic
后无需刷新整个页面即可选中。

### UI-03 半透明 Proposal（P0）

pending Proposal 直接叠加在目标画布上：

-节点和边使用半透明草稿样式；
-不会遮挡缩放控件和节点操作；
-接受后转为 accepted 样式；
-拒绝后从画布消失；
-切换 Map 后只显示属于当前 Map 的 Proposal。

### UI-04 审核可发现性（P0）

只要当前 Project 存在 pending Proposal，界面必须显示可发现的数量和入口。数量为 0 时
控件可以折叠，但不能覆盖 React Flow 控件。打开和关闭有连续动画。

### UI-05 目标与 blocker（P0）

审核面板显示真实 target Map，而不是仅在项目列表中查不到时写“当前不可用”。如果 Map
归属错误，明确显示 Project mismatch。每个 blocker 可定位相关节点或边。

### UI-06 接受后的稳定显示（P0）

接受 Proposal 后：

-节点无需整页刷新即可成为正式内容；
-自动布局可读；
-保存/重载不丢失；
-画布不会白屏；
-长文本可折叠；
-至少 100 个节点时仍能缩放、选择和切换 Map。

### UI-07 Primary Topic 引用（P1）

Primary 的 Map Reference 以紧凑卡片显示，默认不展开 Topic 全部内容。点击后进入 Topic，
浏览器返回可回到原 Primary 视口。

## 10. API、CLI 与 MCP

### API-01 Project-scoped Proposal（P0）

API 支持创建、获取、列表、Plan、review 和 reroute 独立 Proposal。未知 Project、Map、
Proposal 返回 404；归属冲突返回结构化 409/422。

### API-02 无 Run Proposal（P0）

创建 Proposal 时 `source_run_ids=[]` 合法。服务端不得尝试创建虚假 Run 或 Project Run
Card。

### CLI-01 首图闭环（P0）

CLI 能完成 Topic create、bootstrap proposal create、plan、accept 和 graph get。JSON 模式
输出结构化字段，stdout 不混入进度文字。

### MCP-01 首图闭环（P0）

Agent 通过 typed MCP 能完成：

```text
list maps
→ create topic
→ submit bootstrap proposal
→ plan
→ get proposal
```

Review 工具保留人工审核语义，不允许一个“自动维护”工具绕过 revision-aware review。

### MCP-02 用户指定目标（P0）

当对话调用提供 Map ID 时，MCP 的响应和 Agent event 均记录该 ID、title、project_id 和
routing reason。不得替换为同名 Map。

### MCP-03 Promotion（P1）

MCP 可以计划和提交 Promotion，但不能自动接受。

## 11. 数据库、回归与部署

### DB-01 无损迁移（P0）

在真实数据库备份上迁移后：

-既有 Projects、Runs、Datasets、Artifacts、Snapshots、Releases、Maps、nodes、edges、
  revisions 和 Project Run Cards 行数不减少；
-旧 Proposal 可读；
-新表和索引存在；
-迁移重复执行幂等；
-`PRAGMA foreign_key_check` 无新增错误；
-`PRAGMA integrity_check` 返回 `ok`。

### DB-02 Append-only 历史（P0）

Map revision 和 Proposal review 历史只追加。重新路由、Promotion、归档和兼容迁移不得覆盖
既有 accepted revision。

### TEST-01 Go（P0/P1）

Go 测试至少覆盖：

-Map Project 约束；
-bootstrap Proposal；
-独立 Proposal 生命周期；
-routing 不回退 Primary；
-对象级 provenance blockers；
-Map Reference 完整性与无环；
-Promotion 幂等和 revision CAS；
-旧 Card Proposal 兼容读取。

全部既有 Go 测试继续通过。

### TEST-02 UI-v2（P0/P1）

UI 测试至少覆盖：

-空状态；
-Map selector；
-半透明 Proposal；
-审核入口；
-target mismatch；
-接受后状态转换；
-Map Reference drill-down；
-Topic revision stale 提示；
-100 节点图的交互容器与折叠。

全部既有 UI-v2 测试和 production build 通过。

### DEPLOY-01 构建与替换（P0）

只有在 DB rehearsal、Go 测试、UI 测试和 production build 通过后：

1. 备份当前数据库和已安装二进制；
2. 编译新二进制和 UI-v2；
3. 替换生产二进制；
4. 重启 `com.ziwu.aexp`；
5. 检查 API、UI、Project Map 列表和无副作用 Proposal Plan；
6. 验证旧图和旧 Proposal 仍可读取；
7. 记录回滚路径。

### DEPLOY-02 真实首图回归（P0）

部署后重新执行 BOOT-05。验收记录必须包含 Project ID、Map ID、Proposal ID、revision、
graph hash 和验证时间，不记录或宣称任何未经 formal provenance 支持的科研结论。

## 12. Definition of Done

### P0 首图闭环完成

只有当以下条件全部满足时，才能宣称“Evidence Workspace 已经能用”：

-OWN-01 至 OWN-05；
-BOOT-01 至 BOOT-05；
-PROP-01 至 PROP-07；
-ROUTE-01 至 ROUTE-03；
-GATE-01 至 GATE-03；
-UI-01 至 UI-06；
-API-01、API-02、CLI-01、MCP-01、MCP-02；
-DB-01、DB-02、TEST-01、TEST-02、DEPLOY-01、DEPLOY-02。

### P1 主从图完成

只有当 REF-01 至 REF-05、PROMOTE-01 至 PROMOTE-04、ROUTE-04、GATE-04、UI-07 和 MCP-03
全部通过时，才能宣称“Primary + Topic 主从证据图完成”。

### 不允许的完成声明

以下情况均不得宣称完成：

-只创建了空 Map；
-只在数据库写入节点但 UI 仍白屏；
-只显示 Proposal 抽屉但不能接受；
-只有 smoke 示例，没有真实 Project 回归；
-依赖手工 SQL 修复 Project ID；
-仍可能在 target 缺失时写入 Primary；
-历史 Run provenance 被无审计回填；
-新二进制尚未替换运行中的生产版本。
