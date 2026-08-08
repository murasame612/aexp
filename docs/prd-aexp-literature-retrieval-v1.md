# PRD：ResearchOS / aexp 文献检索收口 v1

状态：Implemented and deployed（2026-08-04）

配套验收：[acceptance-aexp-literature-retrieval-v1.md](acceptance-aexp-literature-retrieval-v1.md)

## 1. 目标

让 Agent 在不知道 PDF 目录、索引目录和服务地址的情况下，从 Project 出发完成：

```text
Project Context
→ Project 绑定的 Zotero Collection
→ PaperQA2 检索
→ Zotero 原文精读
→ Project Journal 引用
→ 人工审核的 Evidence Proposal
```

系统要提高论文、模型和可迁移机制的发现效率，但不得把文献检索结果伪装成项目实验事实。

## 2. 单一职责

- Zotero（Mac）：PDF、元数据、高光和笔记的唯一可写事实源。
- PaperQA2（mu）：可删除、可重建的检索派生层。
- NAS：不可变 Corpus Revision 的耐久镜像，不运行第二套文献服务。
- aexp Project：只保存 `zotero_collection_key` 与 `literature_service_profile`。
- aexp Run/Snapshot/Freeze/Release：只管理实验执行和实验 provenance。
- Project Journal：保存工作推理、Run 引用和结构化文献引用。
- Evidence Map：只消费经审核的候选关系，不接受 RAG 直接写入。

不在 aexp 数据库保存 PDF、chunk、embedding、PaperQA 索引或“当前 corpus revision”副本。

## 3. Project 绑定

每个 Project 最多绑定一个默认文献检索入口：

```json
{
  "zotero_collection_key": "SHUMTSPS",
  "literature_service_profile": "mu-paperqa"
}
```

Service Profile 是本机管理配置，默认位于 `~/.aexp/literature-profiles.json`：

```json
{
  "profiles": {
    "mu-paperqa": {
      "endpoint": "http://100.90.101.9:8766",
      "token_file": "literature-mu-paperqa.token"
    }
  }
}
```

项目记录不保存 token。服务返回的 Zotero collection key 必须与 Project 绑定一致，否则查询以
`LITERATURE_COLLECTION_MISMATCH` 阻断。

## 4. Agent 与 ResearchOS 接口

默认 MCP 只提供 Agent 工作时需要的只读 Project 文献路径：

- `aexp_literature_status(project_id)`：返回绑定、active corpus revision、freshness 和后端状态；
- `aexp_literature_query(project_id, query, ...)`：返回答案、answerability、不可变引用和 warnings。

Collection 选择与绑定是人类 Project 配置，默认从 ResearchOS UI 完成。catalog/bind 仅在 advanced MCP
保留给明确授权的维护任务，不出现在 Agent 日常研究工具列表。

Agent 默认先读 `aexp_get_project_research_context`。只有任务依赖文献时才调用检索工具；需要逐段
理解或核对上下文时，必须通过响应中的 `zotero_uri` 打开 Zotero 原文。

ResearchOS 在 Project 内提供“文献”页，而不是把 binding 藏在全局设置：

1. 按 Zotero 文献文件夹的名称和层级路径选择范围，不要求用户理解 collection key、profile 或 corpus；
2. 明确区分“可作为 Zotero live 范围”和“已有匹配 frozen corpus 可正式查询”；
3. 查询后并排显示 PaperQA 回答与逐条 Zotero 页码/chunk 引用；
4. 用户勾选引用后，一键写入 Project Journal，并固定 corpus revision 与 chunk hash。

UI 不负责隐式构建 corpus。选择尚未索引的 collection 只保存研究范围，并明确提示需要后续发布
新的 frozen corpus revision。

## 5. Answerability 和证据域

检索命中文献不等于回答被支持：

- `supported`：PaperQA 明确产生成功回答，且引用能够解析到冻结语料；
- `partial`：有候选来源和回答，但没有明确成功信号或证据覆盖不足；
- `unsupported`：没有可定位来源，或无法形成回答。

所有响应强制包含：

```json
{
  "evidence_domain": "literature",
  "claim_scope": "background_only"
}
```

这两个字段意味着：文献可以支持背景、已有方法、公开论文报告和研究动机；不能证明本项目私有数据
上的指标、消融、机制有效性或运行成功。后者只能来自 aexp Run/Snapshot/Freeze/Release。

## 6. Journal 引用

冻结语料引用使用：

```json
{
  "source_kind": "frozen_corpus",
  "zotero_item_key": "...",
  "zotero_uri": "zotero://...",
  "page_label": "12",
  "corpus_revision": "corpus_...",
  "chunk_sha256": "sha256:..."
}
```

直接精读尚未冻结的 Zotero 内容使用 `source_kind=zotero_live`，并保存正整数
`item_version` 与 `library_version`。Journal 允许同时引用 Run 和文献，两种 provenance 不互相替代。

## 7. LightRAG 决策

LightRAG 冻结为历史试验，不属于默认检索路径：

- 默认入口、MCP、CLI 文档和健康后端列表不展示 LightRAG；
- 不允许 PaperQA 失败后自动回退；
- 显式请求 `backend=lightrag` 返回 `LIGHTRAG_NOT_ENABLED`；
- 旧索引可保留用于历史比较，但不继续扩展或作为服务依赖。

只有冻结 benchmark 证明图检索对多跳任务带来稳定、可复现增益时，才另立 PRD 重新评估。

## 8. 非目标

- 在 aexp 中实现论文管理器或文件浏览器；
- 把 Zotero collection 注册为 DatasetVersion；
- 把检索 chunk 变成 Evidence Map 节点；
- 自动生成或自动接受 Evidence Proposal；
- 用 Literature 引用绕过 formal Run provenance；
- 在 NAS 上再部署 PaperQA、LightRAG 或 Zotero GUI。
