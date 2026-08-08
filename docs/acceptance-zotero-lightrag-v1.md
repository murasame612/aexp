# Zotero → LightRAG v1 验收标准

状态：Superseded（2026-08-04）。当前验收标准见
[acceptance-aexp-literature-retrieval-v1.md](acceptance-aexp-literature-retrieval-v1.md)。

依据：[prd-zotero-lightrag-v1.md](prd-zotero-lightrag-v1.md)

这些标准同时约束功能、可追溯性和研究可信度。未全部通过时，只能称为 prototype 或 shadow，
不得称为默认研究检索系统。

## 1. 验收夹具冻结

正式验收前必须冻结：

- 一个包含 20–30 篇可合法读取全文的 Zotero collection；
- 至少 40 个问题，其中：
  - 至少 12 个单篇定位问题；
  - 至少 12 个跨论文/多跳问题；
  - 至少 8 个高光或笔记问题；
  - 至少 8 个语料中无法回答的问题；
- 每题的 gold source item、允许页码范围、答案要点和 answerability；
- 5 个新增、5 个修改、5 个删除对象的增量回归夹具；
- BM25 与 dense baseline 的固定版本和参数。

验收问题不得用于抽取 prompt 调参。需要改变题集时必须生成新评测 revision，并保留原因。

## 2. 来源与快照完整性

- [ ] Mac Zotero 是唯一可写事实源；mu 未运行 Zotero GUI/profile。
- [ ] 发布不读取或复制 live `zotero.sqlite`。
- [ ] Corpus manifest 记录 `library_version`、发布器、PDF 解析器和逐文件 SHA-256。
- [ ] 100% chunk 具有 item key、chunk hash、source revision 和 Zotero URI。
- [ ] PDF chunk 100% 具有 attachment key 和可验证页码/页标签。
- [ ] annotation/note chunk 100% 具有 annotation/note key；若关联 PDF，则具有附件和页码。
- [ ] manifest 自身 SHA-256 在 Mac 和 mu 重新计算一致。
- [ ] 在收集过程中 Zotero version 变化时，发布失败为 `SOURCE_CHANGED`，不得生成半新半旧 revision。
- [ ] 相同输入重复发布幂等；任一文件内容变化产生新 revision。

## 3. LightRAG 配置可复现性

- [ ] 固定并记录官方 LightRAG commit/release。
- [ ] 固定并记录 Luna 模型名、抽取 prompt hash、entity types、gleaning、温度。
- [ ] 固定并记录 BGE-M3 与 bge-reranker-v2-m3 权重 revision 和运行设备。
- [ ] 固定并记录 chunk 大小、overlap、PDF parser 版本和文本规范化规则。
- [ ] Derived Index 声明唯一 Corpus Revision；不允许跨 revision 混写。
- [ ] 从空目录重建后，文档计数、chunk 集合和 provenance manifest 完全一致。

## 4. 索引状态、原子切换和回滚

- [ ] 新 revision 只在独立 staging workspace 构建。
- [ ] 索引中途故障时当前 active revision 继续可查询。
- [ ] 只有所有文档为 processed/verified 且回归通过后才能切 active pointer。
- [ ] active pointer 切换为原子操作；并发查询只看到旧或新完整版本，不看到混合状态。
- [ ] 自动演练一次切换和一次回滚；回滚后答案与上一 revision 基线一致。
- [ ] 服务重启后 active revision、manifest hash 和文档状态不丢失。

## 5. 新增、修改、删除

- [ ] 新增 item/附件/annotation 后，新 revision 可以检索到且 provenance 正确。
- [ ] 修改文本后，新 revision 不再返回旧 chunk hash。
- [ ] 删除 item 后，其 chunk、entity、relation 和答案引用均为零命中。
- [ ] 删除附件或 annotation 后同样零幽灵命中。
- [ ] 删除回归不得靠查询层黑名单掩盖；active index 本体中不存在已删除来源。

## 6. 检索与回答质量

在冻结题集上同时运行 BM25、dense、LightRAG mix：

- [ ] LightRAG `Recall@10 >= 0.85`。
- [ ] 单篇定位题 `Recall@5 >= 0.90`。
- [ ] 多跳题的 gold paper-set recall 比最佳 baseline 至少提高 10 个百分点，或在 95% bootstrap
  区间内证明非劣且人工有效性评分更高；二者均不满足则不得转默认。
- [ ] 引用 precision `>= 0.95`：引用段落确实支持对应句子。
- [ ] 回答中的引用 100% 能解析到响应 `evidence`，且 hash 与 active revision 一致。
- [ ] 可回答问题的答案要点覆盖率 `>= 0.80`。
- [ ] 不可回答问题的正确拒答率 `>= 0.90`。
- [ ] unsupported hallucination rate `<= 0.05`。
- [ ] 高光/笔记问题必须区分“作者原文”和“用户批注”，不得混写归因。

统计报告必须保留逐题结果；不能只报告总平均。验收数据是系统测试，不是科学实验结果。

## 7. Freshness 与故障语义

- [ ] Mac 在线且 snapshot 与 active revision 一致时返回 `freshness=fresh`。
- [ ] Mac 离线或 Zotero library_version 更新但尚未发布时返回 `freshness=stale` 和具体版本差。
- [ ] stale 模式继续服务最后 verified revision，不降级到无来源的模型回答。
- [ ] Local API、Web API、附件或 LLM 任一不可用时错误层次明确，且不破坏 active revision。
- [ ] 任何缺页、解析失败或无全文论文进入 manifest warnings，并可按 item key 定位。

## 8. 安全与秘密

- [ ] 服务只监听 mu 的 WireGuard/Tailscale 地址，不监听公网地址。
- [ ] 未认证请求返回 401；使用错误 token 不泄露服务状态或语料。
- [ ] relay key、服务 token 不出现在 snapshot、LightRAG graph、日志、Git 或查询 evidence 中。
- [ ] mu 上的 relay key 权限最小化；服务停止后不影响 Zotero Source。
- [ ] API 访问日志不记录完整敏感笔记正文。

## 9. 资源、性能与成本

- [ ] 在 mu 的 RTX 5070 12 GB / RAM 14 GB 上完成 30 篇语料全量索引，无 OOM 和系统失稳。
- [ ] embedding 与 reranker 的实际常驻/串行策略写入配置，不发生静默模型切换。
- [ ] 不含 LLM 生成的检索阶段 p95 `<= 3 s`；完整答案 p95 `<= 20 s`。
- [ ] 每次索引记录 prompt/completion tokens、官方估价和 `0.05` 倍率估价。
- [ ] 超出单次全量索引的预设成本预算时任务在调用前给出 blocker，而非事后告警。

## 10. Agent 与 ResearchOS 边界

- [ ] Agent 查询工具返回结构化 evidence、revision、freshness 和 warnings，而非仅返回答案文本。
- [ ] Agent 可以把 Retrieval Evidence 引用写入 Journal。
- [ ] Agent 创建 Evidence Map 关系时仍走 Evidence Proposal，且保留 corpus/chunk provenance。
- [ ] LightRAG service 没有 accepted Evidence Map 的直接写权限。
- [ ] 删除 LightRAG index 不删除 Journal、Evidence Proposal、Zotero item 或 PDF。
- [ ] RAG 检索结果不能满足 aexp formal Run 的 dataset、seed 或 artifact provenance 门禁。

## 11. 发布判定

状态定义：

- `prototype`：自研原型或不足 20 篇的连通性验证；
- `shadow`：官方 LightRAG 已部署，接受真实 Agent 查询，但不作为默认；允许使用显式
  `shadow` profile 的部分 chunk 覆盖，响应必须报告覆盖范围；
- `accepted`：第 1–10 节全部通过；
- `rejected`：质量、provenance、删除或安全任一硬门禁失败；
- `stale`：已 accepted 的 active revision 落后于 Zotero Source。

只有 `accepted + fresh` 可以成为默认检索后端。`accepted + stale` 可以继续只读使用，但所有
回答必须显示 stale。现有 prototype-hybrid 服务只能作为 baseline，不能计入官方 LightRAG 验收。

当前固定版本为 LightRAG `v1.5.5` / `22ea2d0…`、BGE-M3 `5617a9f…`、
bge-reranker-v2-m3 `953dc6f…`；改变任一项必须生成新的索引配置身份并重跑质量验收。

## 12. 生产验收记录模板

```text
Corpus revision:
Manifest SHA-256:
Library version:
Paper / attachment / annotation / chunk counts:
LightRAG version:
LLM / embedding / reranker / parser versions:
Recall@5 / Recall@10:
Citation precision:
Unanswerable abstention:
Delete ghost hits:
Switch / rollback result:
Index tokens / official cost / 0.05x cost:
Service address:
Result: shadow | accepted | rejected
Blockers:
```
