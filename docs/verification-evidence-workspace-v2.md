# Evidence Workspace V2 验收记录

日期：2026-07-25  
范围：`docs/prd-evidence-workspace-v2.md` 与
`docs/acceptance-evidence-workspace-v2.md` 中的 P0、P1。

本记录验证的是 aexp 的系统能力，不是科研结果。文中创建的首图只包含研究问题、协议、已知
缺口和下一步计划；没有把 smoke、pilot 或缺失 provenance 的历史 Run 解释成正式结论。

## 1. 完成结论

P0 的 Project-scoped Evidence Workspace 已形成真实闭环：

1. 无 Run、无 Dataset 时可以创建 Topic Map 和独立 Proposal；
2. Plan 无副作用，Proposal 经审核后生成不可变 revision 与 graph hash；
3. 未指定目标 Map 的 Proposal 保持 draft，不回退到 Primary；
4. Primary 写入需要显式 `project_level_impact` 或 Promotion 来源；
5. Agent 草稿以半透明只读 overlay 显示，不进入用户保存的画布布局；
6. 旧 Run Card Proposal 仍可读取和规划。

P1 的 Primary–Topic 模型已形成真实闭环：

1. Primary 可保存固定 Topic revision/hash 的 `map_ref`；
2. Map Reference 仅允许同 Project、Primary → Topic、无环；
3. Promotion 先 Plan，再生成独立 Primary Proposal，不能自动接受；
4. 接受 Promotion 只增加摘要、Map Reference 和必要关系，不复制 Topic 的 Run/seed/超参数；
5. Topic 更新或归档不会改写已固定的引用，UI 显示 current/stale/archived/missing；
6. Map Reference 可进入 Topic，浏览器 Back 返回 Primary，`?map=<id>` 深链接刷新后仍保留。

旧兼容 `DELETE /evidence-chains/{id}` 已收口为幂等软归档：它只把 Map 标记为
`role=archive,status=archived`，不再物理删除节点、边或 accepted revision。

## 2. Formal evidence 共享门禁

正式关系和 Promotion 现在共用同一条 Run 证据门禁。

以下任一入口引用 Run 时，都会执行门禁：

- `supports`
- `weakens`
- `does_not_prove`
- 直接 Promotion Run 节点
- Promotion 一个由上述关系支持的 claim

门禁要求：

- Run 属于当前 Project；
- Run 状态为 succeeded，evidence grade 为 formal；
- DatasetVersion 已登记、不可变且 verified；
- seeds、Git、config hash、split protocol、evaluation protocol 完整；
- 至少有一个属于同 Project 且状态为 `released` 的 EvidenceSnapshot release。

Blocker 保留准确的 `node_id`、`edge_id` 和 `run_id`。同一 Run 在一次计划中只检查一次，
输出顺序稳定。没有 Run 的协议、问题、计划和工作命题仍可用于首图与上下文 Promotion，
不会被错误要求伪造 Dataset 或 Snapshot。

新增回归覆盖：

- 纯上下文 Promotion eligible；
- 历史/不完整 Run 返回对象级 provenance blocker；
- 完整 Run 但没有 released Snapshot 返回 `EVIDENCE_RELEASE_MISSING`；
- 加入 released Snapshot 后关系和 Promotion eligible；
- 跨 Project Run 返回 `RUN_PROJECT_MISMATCH`。

## 3. 真实首图与 Promotion

Project：`dam-imputaion-downstream`

Topic Map：

- ID：`chain_w83nyr06fwfm`
- 标题：`插值数据与下游预测协议`
- revision：1
- graph hash：`fd0745db73a89fc01d016da624ae56a43f85ef663f86e6c4c52271835cd7a61a`
- Proposal：`proposal_69efd7a0619f6c0084e9`，accepted
- 内容：5 个节点、3 条边，均为问题、协议、工作命题、缺口和计划

Primary Map：

- ID：`chain_primary_0c05fc5ae4058441`
- revision：1
- graph hash：`1af1800ebf5bedc2506dd410096f4dcf826c0eb15f6ec1c7c033b486932bdeb3`
- Promotion Proposal：`proposal_cd5142e504d01a6ea533`，accepted
- 固定来源：Topic revision 1 及上述 graph hash
- 内容：1 个项目级 issue 摘要、1 个 Map Reference、1 条 `related_to`

服务重启后，两张图的 revision 与 hash 均保持不变。

## 4. 数据库迁移

生产数据库：

- `/Users/ziwu/.aexp/aexp.db`

迁移前备份：

- `/Users/ziwu/.aexp/backups/aexp-before-evidence-v2-20260725-170409.db`
- SHA-256：`5aa376b059c380eb358952f29234307a4df2269043c6b604ccdc855a3fbba7a5`

显式迁移映射：

- `chain_60nayjho6gqy` → `multimodal-defect-detection`
- `chain_plw7ds5mfh1l` → `multimodal-defect-detection`
- `chain_l0321ol7jvza` → `dam-displacement-imputation`
- 无法无歧义归属的 `chain_mclum6vrfdd2` 保留并归档，没有删除

迁移完成时，Project、Run、DatasetVersion、Artifact、Snapshot、Release 和 Project Run Card
核心计数未减少。新增首图使 Map 由 12 增至 13；节点由 59 增至 64；边由 59 增至 62；
revision 由 25 增至 26。后续真实 Promotion 与后台索引继续以 append-only 方式增加记录。

最终检查：

- `PRAGMA integrity_check`：`ok`
- `PRAGMA foreign_key_check`：0 条
- active orphan Map：0
- 迁移前 25 个 Evidence revision 在当前库中缺失或被改写：0
- 迁移前 Run 缺失：0
- 迁移前 Artifact 缺失：0

## 5. 自动化与人工验证

自动化：

- `go test ./...`：通过
- UI-v2 Vitest：22 个测试文件、86 项测试通过
- `npm run build`：通过
- 100 节点自动布局：坐标有限、唯一、可序列化
- Map Reference current/stale/archived/missing：均有单元测试

人工浏览器验证：

- 空项目显示可操作首图入口，不是白屏；
- pending Proposal 在画布显示半透明 overlay；
- 审批面板显示目标 Topic、routing reason 和 blocker；
- 接受后 overlay 变为正式图；
- Promotion overlay 与 Map Reference 可见；
- 点击 Map Reference 进入 Topic；
- Back 返回 Primary；
- Topic 深链接刷新后仍选择指定 Map。

最终服务继续提供与构建一致的 UI 资产：

- `/ui-v2/assets/index-t0Z62YPc.js`

## 6. 生产部署

launchd 服务：`com.ziwu.aexp`

最终二进制：

- `/Users/ziwu/.local/bin/aexp`
- SHA-256：`908e88adcd79438265d7bb3eea7722e8a7f1a828bdbd54e05b398275a6ddab9d`
- 架构：Mach-O arm64

回滚二进制：

- 初始备份：
  `/Users/ziwu/.local/bin/backups/aexp-before-evidence-v2-20260725-170409`
- 最终门禁替换前备份：
  `/Users/ziwu/.local/bin/backups/aexp-before-final-evidence-gate-20260725-1830`
- 软归档收口前备份：
  `/Users/ziwu/.local/bin/backups/aexp-before-soft-archive-20260725-1839`
- 最后一次替换前 SHA-256：
  `c307adae039381333551c18272a22f18f3eac7f4f3197cc46c0444d2771f4659`
- 门禁替换前 SHA-256：
  `694d73e355f4939cb3c9f43262a91eb307c4893ea95761675b3648b0662dd772`

二进制回滚命令：

```bash
cp /Users/ziwu/.local/bin/backups/aexp-before-soft-archive-20260725-1839 \
  /Users/ziwu/.local/bin/aexp
launchctl kickstart -k gui/$(id -u)/com.ziwu.aexp
```

数据库回滚只能在服务完全静止且用户明确确认后使用迁移前备份，不能在运行中的服务上覆盖。

最终运行态：

- launchd state：running
- 服务 PID：20647（最终复核时）
- 远端 pilot `run_TrmRtsxuXfZ3`：succeeded、reachable、fresh
- 控制面重启没有把 pilot 当作 formal 科研结果，也没有改变其远端生命周期事实
