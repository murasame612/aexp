# PRD：ResearchOS Zotero 开放发现与引用钉 v1

状态：Implementation target（2026-08-04）

配套验收：[acceptance-zotero-open-discovery-v1.md](acceptance-zotero-open-discovery-v1.md)

## 1. 目标

Agent 可以自由检索和精读用户的 Zotero 文献库；项目绑定的 Collection 只决定默认冻结语料，
不构成知识围墙。低成本、可逆、只读的发现不逐次请示；约束发生在引用进入持久记录、修改
Zotero/Project 状态和重建 corpus 时。

```text
Project frozen corpus（默认、可复现）
        ↓ 信息不足时
Zotero live 全库（自由检索与精读）
        ↓ 仍不足且用户允许外部查询时
公开学术检索

任意候选 → 引用落笔时 pin → Journal / reviewed Evidence
```

## 2. 核心决策

### 2.1 检索自由

- Agent 优先查询 Project 绑定的 PaperQA2 frozen corpus。
- frozen corpus 不足时，可直接使用现有 Zotero MCP 检索全库、笔记、高光与 PDF。
- 外部公开学术检索遵循宿主的网络与隐私授权，不由 aexp 另建代理。
- Project Collection 是相关性先验和可复现默认值，不是读取权限边界。

### 2.2 使用时固定

- `frozen_corpus` 引用固定 `corpus_revision + chunk_sha256`。
- `zotero_live` 引用固定 `zotero_item_key + item_version + library_version + zotero_uri`。
- 缺少所需 pin 的引用不得写入 Project Journal。
- 文献始终是 `evidence_domain=literature`、`claim_scope=background_only`，不能替代
  Run、DatasetVersion、Snapshot、Freeze 或 Release provenance。

### 2.3 持久修改受控

- 复用现有 Zotero MCP 的导入、Collection membership、标签和笔记写入能力；写操作使用 Zotero
  自带的 review/undo，不创建第二套 Zotero MCP。
- Agent 可以提出导入或纳入 Collection，用户在现有 Zotero 审核卡中确认。
- Project 的 Collection binding 由 ResearchOS UI 显式编辑。
- binding 变化不会静默重建 PaperQA2；重建产生新的 immutable corpus revision，旧 revision 保留。

## 3. 产品改动

### 3.1 Agent contract

- Project Research Context 明示：`project_first`、允许 `zotero_live` 发现、binding 不限制检索。
- `aexp_create_project_journal_entry` 的 MCP 描述同时接受 PaperQA2 frozen citation 和现有 Zotero
  MCP 返回的 live citation，不再暗示所有引用都来自 `aexp_literature_query`。
- aexp Agent skill 说明正确顺序：frozen discovery → Zotero live → 必要时外部；正式落笔前 pin。

### 3.2 ResearchOS UI

- Project 设置可编辑 frozen corpus 的 Collection key 与 service profile。
- UI 明示该设置“控制可复现项目语料，不限制 Agent 检索 Zotero 全库”。
- 变更 binding 时提示“不会自动重建 corpus”。
- Project Journal 折叠态显示文献引用数量；展开态显示每条引用的来源类型、item key、页码和
  pin 摘要，并提供 Zotero deep link。

## 4. 不新增的组件

- 不新增 Zotero MCP、文献数据库、导入器或 PDF 仓库。
- 不把 Zotero 全库复制进 aexp 或 NAS。
- 不让 aexp 服务调用 Codex MCP；Agent 同时编排现有 Zotero MCP 与 aexp MCP。
- 不自动把检索结果写入 Journal、Evidence Map 或 Project Collection。
- 不新增每次检索授权弹窗、三环权限数据库或复杂配额系统。

## 5. 正常心智模型

- Zotero MCP：实时搜索、精读和经审核的文献库写入。
- PaperQA2：Project Collection 的冻结、可重建检索投影。
- aexp/ResearchOS：Project binding、Journal 引用 pin 和实验/证据边界。
- 用户：确认持久写入与 corpus rebuild；不需要批准普通只读检索。
