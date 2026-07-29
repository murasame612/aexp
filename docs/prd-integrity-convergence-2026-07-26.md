# PRD：aexp 数据完整性与写入口收口

状态：Implemented and production-verified

版本：1.0

日期：2026-07-26

依据：

- [review-runtime-audit-2026-07-26.md](review-runtime-audit-2026-07-26.md)
- [review-evidence-chain-2026-07-26.md](review-evidence-chain-2026-07-26.md)
- [prd-system-simplification.md](prd-system-simplification.md)

绑定验收标准：
[acceptance-integrity-convergence-2026-07-26.md](acceptance-integrity-convergence-2026-07-26.md)

## 1. 摘要

本轮不增加新的顶层产品概念，也不重写 SSH、rsync、TransferJob、Run、Asset 或 Evidence
底层。目标是让已经冻结的四概念系统具备一致、可验证的写入语义：

```text
Project
├── Assets
├── Runs
└── Evidence Maps
```

当前主要风险不是功能缺失，而是同一行或同一事实存在多个不等价写入口：

- Run 状态、provenance 与 finalization 共享一次 57 列全行更新；
- Run interpretation 与 Evidence proposal 共享一次 22 列卡片 UPSERT；
- Evidence 语义既可走 proposal，也可由全量 graph PUT 直接覆盖；
- 运行时事件可能反向中断训练；
- Project 删除与历史兼容入口可以制造孤儿身份。

本轮统一原则：

> 一个状态子域只有一个权威写入口；跨阶段转换使用 CAS；观测系统永不成为实验故障源；
> Evidence 的布局编辑与语义写入严格分离。

## 2. 产品不变量

### 2.1 Run 不变量

1. 权威终态不得被旧快照覆盖。
2. `status` 转换必须带 expected status 或等价 revision。
3. `data_finalization_*` 只能由窄字段接口更新，不得回写整行 Run。
4. 同一 Run 的 finalization 同时最多一个执行者；重复触发必须幂等。
5. Cancel 与自然结束竞争时，自然终态优先；已完成的 Run 不得被改回 cancelled。
6. 所有状态写入失败必须可观察，不能静默丢弃。

### 2.2 Runtime Event 不变量

1. `aexp_events.emit()` 对任意 Python 对象和任意 I/O 故障都不得向训练代码抛异常。
2. Tensor、NumPy、Decimal 与常见容器必须转换为稳定 JSON 值。
3. 未设置 `AEXP_UI_EVENTS` 时首次写入必须产生一次节流告警。
4. 指标名称默认保持调用者输入，不进行不可预测的隐式拆分。
5. 多进程写入必须具备 rank 身份；默认只有 rank 0 写入。
6. 事件缓存必须识别文件 generation，截断或重建后不得混入旧内容。

### 2.3 Run Interpretation 不变量

Run Card 仅作为兼容读投影保留，内部拆成两个互不覆盖的写域：

```text
interpretation:
  question / verdict / evidence_level / key_metrics / next_action / important / ...

graph proposal:
  graph_status / proposal_hash / base_revision / patch_json / reviewed_at / ...
```

1. 保存 interpretation 不得改变 proposal 状态。
2. 提交 proposal 不得清空 interpretation。
3. 更新现有记录必须使用 revision 或 `expected_updated_at`。
4. Project 归属只能在创建时继承；后续变更必须显式指定并校验。

### 2.4 Evidence 不变量

1. Agent 的语义写入只走 Project-scoped Evidence Proposal。
2. `PUT /evidence-chains/{id}/graph` 只允许布局字段变化：
   `x/y/width/height/pinned`。
3. graph PUT 必须携带 `expected_revision`；缺失返回 400，过期返回 409。
4. accepted graph 的每条 formal relation 都必须通过同一套 readiness 校验。
5. Primary 与 Topic 使用同一套结构/DAG/方向校验；历史不合法 Topic 先审计迁移。
6. 同一 `(run, claim)` 的 `supports / weakens / does_not_prove` 互斥。
7. proposal hash 只对 draft/pending 提供幂等；rejected/expired 允许新 attempt。
8. accepted proposal 的同内容重试返回明确的 already-accepted 结果，不伪装成新提交。
9. 必须提供全图只读审计，报告既存 blocker 的 node/edge/run ID。

### 2.5 Project 不变量

1. 存在 Run、Run Card 或 active Evidence Map 引用时，Project 删除必须被拒绝并返回 blocker。
2. 所有新 Run 必须绑定存在的 canonical Project；历史未归属 Run 保留可读，并仅通过显式、
   CAS 防冲突且有审计的操作修正归属。
3. manual category 与 project profile 只保留兼容读投影，不再作为规范写入口。
4. 任何归属迁移必须显式、可审计且不按名称猜测；只改变当前组织归属，不改写不可变历史。

## 3. 正常工作流

```text
Run submit
→ runtime events（best effort，永不影响训练）
→ authoritative terminal status（CAS）
→ idempotent output finalization
→ released Asset revisions
→ Evidence Snapshot
→ Evidence Proposal
→ review
→ accepted Evidence Map revision
```

用户可以自由记录假说、问题、计划和草稿。只有形成 formal relation 或进入 Release 时才执行
完整 provenance gate。V1 不要求所有自由文本 claim 结构化，也不降低负结果进入正式图的
可信门槛。

## 4. 实施范围

### Stage 0：数据丢失与训练中断止血

1. Event helper 值转换、异常隔离、缺环境告警。
2. Run Card interpretation/proposal 窄写入与 CAS。
3. Cancel 使用 expected status CAS。
4. Finalization 使用窄字段更新。
5. 修复 v2 Evidence Proposal 丢失 `project_id`。
6. 所有项目必须先落失败回归测试，再修改实现。

### Stage 1：权威写入口收口

1. graph PUT 降为 layout-only，`expected_revision` 必填。
2. 增加 `AuditEvidenceChain`，对 accepted graph 执行全量校验。
3. 统一 Topic/Primary validator，并为存量异常提供修复报告。
4. terminal proposal hash 语义修复。
5. formal polarity edge 互斥。
6. finalization singleflight + 持久状态幂等。
7. 状态写入错误统一记录。

### Stage 2：身份和兼容层收口

1. Project 删除引用检查和显式 reassign。
2. 关闭 manual project category 新写入。
3. 修复 CLI card 随 cwd 漂移。
4. 在存量数据审计完成后增加必要 FK 或等价 store guard。
5. 从 Store 公共接口逐步移除全行 `UpdateRun`。
6. 下线 Run Card proposal 生命周期，保留兼容读投影。

### Stage 3：可信度增强

1. Event cache generation、rank、seq 与尾部 flush。
2. API token 加固。
3. 在 promotion/release 层增加可选的 snapshot/metric citation。
4. 计算 Evidence strength 作为展示和导出信号，不作为普通研究记录硬门槛。

## 5. 非目标

- 不新增第五个顶层研究概念；
- 不把 NAS 变成第二控制面；
- 不重做 TransferJob、Placement、SHA-256 或远端执行；
- 不要求普通草稿 claim 立即结构化；
- 不把 smoke 结果升级为正式证据；
- 不删除历史 revision、Run、Card 或 Evidence 数据；
- 不在未完成真实数据库副本演练前执行破坏性 schema 迁移。

## 6. 兼容和迁移策略

1. 所有 schema 变化先对真实数据库 `.backup` 副本演练。
2. 迁移前后记录核心表计数，并执行 `PRAGMA integrity_check` 与 `foreign_key_check`。
3. 历史不合法 Topic、孤儿 Project 引用和空 project_id 必须生成审计报告，不静默修复。
4. 旧 CLI/MCP 写入口先转发到新权威服务，再隐藏，最后按 deprecation ledger 删除。
5. 任何迁移重复执行必须幂等。

## 7. 完成定义

只有同时满足绑定验收文档中的：

- `EVENT-*`
- `RUN-*`
- `CARD-*`
- `EVIDENCE-*`
- `PROJECT-*`
- `MIGRATION-*`
- `PRODUCTION-*`

全部必选项，才可将本 PRD 标为 Implemented。单元测试全绿但缺少并发、正向 formal
Evidence 或真实库副本演练时，不得宣称完成。
