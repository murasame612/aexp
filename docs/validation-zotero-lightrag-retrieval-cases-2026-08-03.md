# Zotero → LightRAG 真实检索案例验收 — 2026-08-03

状态：Shadow exploratory validation completed

本报告记录五个真实查询的工程表现。它不是科学实验结果，也不替代
[`acceptance-zotero-lightrag-v1.md`](acceptance-zotero-lightrag-v1.md) 中冻结的
40 题正式评测。

## 固定环境

- Corpus revision：`corpus_5f1dd19e8bdc693b2ba9`
- Profile：`shadow`
- Source documents：2384
- Selected / indexed documents：209 / 208
- Papers：25
- Zotero annotations：110
- 查询模式：LightRAG `mix` + BGE-M3 + bge-reranker-v2-m3
- 所有响应均保留 `PARTIAL_INDEX_COVERAGE`

## 评价口径

- `PASS`：问题核心得到正确回答，目标来源命中，且没有明显越界陈述。
- `PARTIAL`：主要内容可用，但存在来源层级、召回噪声或表达合同问题。
- `FAIL`：语义或结构化响应会误导自动化调用者。

## 案例 1：精确单篇方法与评估

问题：论文 *A novel method for settlement imputation and monitoring of
earth-rockfill dams subjected to large-scale missing data* 提出了什么，如何评估？

观察：

- 正确归纳 FEM + SVR + IPSO 的方法组合及其评价方式；
- 命中目标 Zotero item `822TAAV9`；
- 返回的目标证据包括附件 `5HQQXTMN`、第 1 页和第 11 页；
- 第一个 chunk SHA-256 为
  `91d12a204f0607568fd5505b6ed7f5cb9f7900935909c47709f3973cbab1f052`；
- 20 条 evidence 全部可解析，缺失 provenance 为 0。

判定：`PASS`。适合 Agent 做单篇快速定位，但默认返回 20 条来源偏多。

## 案例 2：用户 Zotero 高光

问题：在 *Hydrostatic, temperature, time-displacement model for concrete
dams* 的个人高光中，HST 的局限是什么，HTT 有何不同？需要区分个人高光和作者
更广泛的主张。

观察：

- 正确区分 HST 使用季节项代理温度与 HTT 使用坝体实测温度；
- 命中目标 item `3R4UDF5T`；
- 20 条 evidence 中有 2 条带 `kind=annotation` 和 annotation key；
- 其余 18 条是 PDF 正文，说明 annotation 已进入检索，但尚未得到足够高的优先级；
- provenance 缺失为 0。

判定：`PARTIAL`。高光检索链路成立，但需要 annotation-aware reranking，并在回答中
把“用户标注”与“作者正文”绑定到各自的 evidence，而不只靠语言模型表述。

## 案例 3：两篇论文的机制对比

问题：对比 GCN-Former-BiLSTM 与 frequency-division noisy displacement
prediction 在时间依赖、空间依赖和传感器噪声上的处理方式；证据不足时必须说明。

观察：

- 两个目标 item `DK4CIRD5` 和 `N9ZRP82N` 都命中；
- 正确归纳前者的图空间建模和多尺度时间建模；
- 正确归纳后者的频率分解、TCN 和噪声处理；
- 没有硬凑对称结论：明确指出没有检索到后者显式空间建模证据，也没有检索到前者
  显式降噪模块证据；
- 20 条 evidence 中包含 3 条 annotation，缺失 provenance 为 0。

判定：`PASS`。这是当前最能体现图检索价值的案例。

## 案例 4：跨文献缺失数据方法综合

问题：按机制归纳语料中用于监测数据缺失时的大坝位移插补/预测方法，并排除普通
完整数据预测。

观察：

- 成功组织出多个机制组，并命中 `822TAAV9`、`IMN6U8X7`、`CUSPSCZA` 等三个
  直接相关的库内条目；
- 能把 FEM + SVR、multi-point correlation、multi-graph attention 等方法放入不同
  机制类别；
- 20 条 evidence 来自 10 个 Zotero item，缺失 provenance 为 0；
- 答案还从综述正文中提取了综述所引用的外部论文标题，但没有明确标为
  “secondary mention / 不在当前 Zotero collection 中”。这会让 Agent 误以为拿到了那些
  论文的直接证据。

判定：`PARTIAL`。综合能力可用，但必须区分 `direct_corpus_source` 与
`secondary_citation_mention`。

## 案例 5：语料外、不可回答问题

问题：语料中是否有随机对照试验证明部署这些模型降低真实溃坝率或伤亡，并要求效应量和
置信区间；若没有，不得用预测误差研究替代。

观察：

- 正文正确回答“没有 RCT、没有因果效应量和置信区间”，并明确 MAE/RMSE 不能替代
  溃坝或伤亡结局；
- 但结构化字段错误返回 `answerability=supported`，因为网关当前只以“是否检索到 evidence”
  判断 answerability，而不是判断 evidence 是否支持所问命题；
- 英文问题得到西班牙文正文，发生语言漂移；
- 仍返回 20 条背景文献，虽然 provenance 全部可解析，但它们只能支持“这些研究测的是预测
  误差”，不能支持用户要求的 RCT 命题。

判定：`FAIL`（响应合同），但自然语言拒答内容本身正确。

## 汇总结论

| 维度 | 结果 |
|---|---|
| 单篇定位 | 好 |
| 跨论文机制比较 | 好 |
| Zotero 高光利用 | 可用，但正文噪声偏多 |
| 跨文献综合 | 可用，但直接/二手来源未分层 |
| 不可回答拒答正文 | 正确 |
| `answerability` 结构化判定 | 不合格 |
| provenance 完整性 | 100/100 evidence 可解析 |
| 引用数量控制 | 过宽；五个案例均固定返回 20 条 |
| 输出语言稳定性 | 不合格；一次英文问题输出西班牙文 |

当前 shadow 后端适合 **Agent 有监督探索、定位和比较文献**，不适合直接成为无人审核的
ResearchOS 证据写入器或默认权威回答层。

## 下一轮最小修复

1. answerability 改为由模型输出的受约束判定与 evidence entailment 共同决定，禁止
   “有检索结果 = supported”。
2. 查询合同增加 `evidence_role=direct_source | user_annotation | secondary_mention`。
3. 对 annotation 意图启用 annotation-aware reranking。
4. 允许按问题动态收缩 evidence，默认不固定返回 20 条。
5. 在 prompt 和网关中固定响应语言为查询语言，并添加语言一致性回归测试。
