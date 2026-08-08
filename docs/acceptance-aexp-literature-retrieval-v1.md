# ResearchOS / aexp 文献检索收口 v1 验收标准

依据：[prd-aexp-literature-retrieval-v1.md](prd-aexp-literature-retrieval-v1.md)

## A. 数据与迁移

- [x] 旧数据库迁移前后所有既有业务表计数不变，`integrity_check=ok`（后台资源快照在服务运行期间正常增加）。
- [x] `project_definitions` 仅新增 collection key 与 service profile，默认空值不影响旧 Project。
- [x] `project_journal_entries` 仅新增 `literature_refs_json`，旧条目读取为无引用。
- [x] Project 绑定保存、读取、API 往返和 UI 创建表单均保留两个字段。

## B. Agent 可发现性

- [x] Project Context v2 显示 literature configured 状态、collection key、profile 和证据边界。
- [x] 已绑定 Project 的 `next_reads` 包含 `aexp_literature_status`；未绑定 Project 不产生无效查询提示。
- [x] 默认 MCP 只暴露 status/query；人类绑定留在 UI，catalog/bind 只在 advanced 维护配置中可见。
- [x] MCP 工具描述明确 literature 不能满足实验 provenance。

## B2. Project 文献工作台

- [x] Project 顶部导航直接提供“文献”入口；全局项目设置不再要求手填 collection key/profile。
- [x] Collection 选择器读取本机 Zotero 的递归 collection 路径，并展示 library revision。
- [x] UI 区分 live-only、未索引、profile 不匹配和 frozen RAG ready，不把绑定等同于索引完成。
- [x] 只有 Project 绑定与 ready profile 的 collection 一致时才允许查询。
- [x] PaperQA 回答与引用分区显示，引用包含题名、页码、Zotero deep link 和定位文本。
- [x] 勾选引用可一键写入 Project Journal，写入后能够打开对应日志条目。

## C. 查询合同

- [x] status 返回 active corpus revision、freshness、collection key 和 PaperQA2 状态。
- [x] Project collection 与服务 collection 不同，status/query 返回 `LITERATURE_COLLECTION_MISMATCH`。
- [x] query 只使用 PaperQA2；PaperQA 故障不自动回退 LightRAG。
- [x] 显式 LightRAG 请求返回 `LIGHTRAG_NOT_ENABLED`。
- [x] 查询响应包含 `evidence_domain=literature`、`claim_scope=background_only`、corpus revision、manifest hash、Zotero URI 和 chunk hash。
- [x] 引用存在但 PaperQA 没有明确成功回答信号时只能是 `partial`，不能是 `supported`。

## D. Journal 与 Evidence 边界

- [x] `frozen_corpus` 引用缺 corpus revision 或 chunk hash 时拒绝写入。
- [x] `zotero_live` 引用缺 item/library version 时拒绝写入。
- [x] Journal 可同时保存同 Project Run IDs 和 Literature refs，并完整往返。
- [x] 文献工具、PaperQA 服务和 NAS 镜像均没有 accepted Evidence Map 写权限。
- [x] Literature refs 不满足 Run DatasetVersion、Snapshot、Freeze 或 Release 门禁。

## E. 真实 Corpus 验收

- [x] 当前 damxer Zotero collection 递归发布 85 篇来源，不使用测试抽样上限。
- [x] active PaperQA index 与同一 immutable corpus revision 绑定，所有 chunk provenance 可解析。
- [x] 真实 arch-dam 查询报告至少覆盖：单篇定位、跨论文比较、可迁移机制、无法回答四类问题。
- [x] 报告逐题保存 answerability、引用 item/page、corpus revision、人工支持性判定和误导风险。
- [x] smoke 只标为系统连通性验证，不作为研究结论。

## F. NAS 与部署

- [x] NAS 只保存 immutable corpus revision 与 manifest；Mac 本地和 NAS 重算 SHA-256 一致。
- [ ] 删除 mu 的 PaperQA 派生索引后，能够从 NAS/发布 revision 重建。
- [x] 新二进制和 UI-v2 构建成功，数据库备份迁移演练通过后再替换现役服务。
- [x] 服务重启后 Project Context、MCP 工具与查询状态正常，现有 Run/Journal/Evidence 数据不变。
