# PaperQA2 × arch-dam-forcasting 真实检索验收报告

日期：2026-08-04
状态：**有条件通过（supervised research retrieval）**
语料版本：`corpus_5f1dd19e8bdc693b2ba9`

## 1. 验收结论

本轮不是通用问答演示，而是直接从 `arch-dam-forcasting` 当前研究问题、统一实验协议和第一项论文贡献中提取七个真实场景，调用部署在 `mu` 上的 PaperQA2 主后端完成检索。

综合判定：

- PaperQA2 已能支持 Agent 做高拱坝文献定位、方法比较、项目假设边界检查和方案初筛；
- 七次查询全部走 `paperqa2`，没有触发 LightRAG fallback；
- 62/62 条返回证据均包含 Zotero item、冻结文件路径和 chunk SHA-256；
- HST/HTT、多测点空间关系、缺失数据和“插补不等于预测”等问题表现较好；
- 系统能区分“论文直接结论”和“面向本项目的推论”，没有把严格案例记忆边界伪装成已有论文定论；
- **仍不能作为无人审核的 ResearchOS Evidence 写入器**：项目私有指标问题虽然正文正确拒答，结构化 `answerability` 却错误返回 `supported`；
- 创新点归纳受到当前 25 篇语料覆盖范围限制，只能作为研究导航，不能据此生成“首次提出”类正式 claim。

本报告是检索系统验收，不是科学实验结果，也不是 `aexp` formal run 或论文证据。

## 2. 系统快照

验收时远端服务状态：

| 项目 | 实际值 |
|---|---|
| Primary backend | PaperQA2 `2026.3.18` |
| PaperQA 状态 | `indexed` |
| Papers | 25 |
| Frozen chunks | 2,384 |
| Embedding mode | `hybrid`（BGE-M3 + sparse） |
| Active corpus | `corpus_5f1dd19e8bdc693b2ba9` |
| Snapshot SHA-256 | `sha256:5f1dd19e8bdc693b2ba9d686f3c01369b963170ac553899e165ae7d658ea4084` |
| LightRAG | shadow backend；本轮未使用 |
| Corpus freshness | `fresh` |

## 3. 评价方法

每个案例按以下四项检查，而不是只看答案是否流畅：

1. **任务正确性**：是否回答了实际研究决策，而非仅复述关键词；
2. **证据直接性**：来源是直接方法证据、综述性支持，还是只能支持一般原则；
3. **主张校准**：是否把相关性写成因果、把范围检索写成绝对空缺、把插补写成预测；
4. **结构化合同**：`answerability`、backend、fallback、corpus revision 和 provenance 是否与正文一致。

判定口径：

- `PASS`：核心问题回答正确，主张强度与证据相称，来源可追溯；
- `PARTIAL`：答案可用于研究导航，但语料覆盖或直接证据不足，必须人工复核；
- `FAIL`：正文或结构化字段会误导自动化 Agent。

## 4. 汇总结果

| # | 场景 | 正文表现 | 结构化合同 | 判定 |
|---:|---|---|---|---|
| 1 | 严格历史案例边界与未来泄漏 | 正确区分论文原则和项目规则 | 正常 | PASS |
| 2 | HST 与 HTT 温度建模 | 机制、局限和因果边界清楚 | 正常 | PASS |
| 3 | 位移召回 + 环境工况重排 | 正确识别为有机理动机但待验证的项目假设 | 正常 | PASS |
| 4 | 多测点、插补、异常识别与预测 | 三类任务区分准确，并提示跨测点未来值泄漏 | 正常 | PASS |
| 5 | 研究空缺与创新点 | 有边界意识，但关键通用时序检索论文覆盖不足 | 正常 | PARTIAL |
| 6 | 私有项目精确指标与 aexp provenance | 正文明确拒答，没有伪造指标 | **误标 `supported`** | FAIL |
| 7 | TCN 测点连续数月缺失的迁移方案 | 能比较三条技术路线及适用前提 | 正常 | PASS |

合计：**5 PASS / 1 PARTIAL / 1 FAIL**。

## 5. 逐案例审计

### 5.1 严格历史案例边界与未来泄漏

问题：

> 在用历史案例增强高拱坝未来 7 天位移预测时，怎样定义训练记忆库、查询窗口和候选案例的时间边界，才能避免未来信息泄漏？区分论文直接规则与项目推论。

实际回答要点：

- 文献支持“模型只能使用历史输入”和“其他测点未来位移不是预测时可用量”等一般原则；
- 当前语料没有直接定义本项目的训练记忆库、查询窗口和候选真实未来边界；
- 系统将以下内容明确标为项目推论：查询窗口向过去回溯、匹配特征截断于预测起点、候选案例的真实未来必须在查询时刻前已经发生。

主要来源：

- `6QP3BUIT`，*Displacement observation data-based structural health monitoring of concrete dams: A state-of-art review*，第 5 页；
- `AW4FTDTB`，*DRLSTM: A dual-stage deep learning approach driven by raw monitoring data for dam displacement prediction*，第 6 页；
- `DK4CIRD5`，*GCN-Former-BiLSTM*，第 1、19、20 页。

审计：答案没有假装文献已经提出 `candidate.future_end < query.context_start`。这对本项目尤其重要，因为严格记忆截止仍应由统一实验协议冻结，而不是由 RAG 自动推导。召回证据偏综述性，但答案正确暴露了直接证据不足。

判定：**PASS**。

### 5.2 HST 与 HTT 温度建模

问题：

> 比较 HST 和使用实测坝体温度的 HTT 类模型；水压、温度、时效如何表示，HST 的季节代理有什么局限，实测温度是否足以证明局部因果贡献？

实际回答要点：

- HST 将位移组织为水压、季节温度代理和时效分量；
- HTT 显式使用平均温度和线性温差/温度梯度，并保留水压和时效项；
- HST 的周期代理难以覆盖坝体内部温度场的空间非均匀、传热滞后、温度漂移和异常边界；
- 实测温度提高拟合或预测价值，不等于证明局部温度具有独立因果贡献。

主要来源：

- `3R4UDF5T`，*Hydrostatic, temperature, time-displacement model for concrete dams*，第 1、2、3、10 页；
- `3R4UDF5T` 用户 Zotero annotation `4SDN3C2N`；
- `6QP3BUIT` 综述，第 2、3、7、12 页。

审计：这是本轮质量最高的回答。它既命中原始 HTT 论文与用户高光，也没有把预测改进提升成因果结论。对项目当前“训练期 PCA 热状态只能解释为统计状态、不能解释为局部物理温度”的边界具有直接帮助。

判定：**PASS**。

### 5.3 位移形态召回与环境工况重排

问题：

> 先按 30 天位移形态召回，再用水位水平、变化路径、温度状态和响应滞后重排，文献能直接支持哪些动机，哪些仍只是项目假设？

实际回答要点：

- 文献支持高拱坝位移具有环境响应、空间差异和滞后效应；
- 水位、温度及历史状态进入相似性判断具有工程动机；
- 现有语料没有直接证明“30 天形态召回 → 环境重排”的流水线优于其他流程；
- 各环境变量的独立增益仍需消融、置换、严格时间外推和跨测点验证。

主要来源：

- `6QP3BUIT` 综述，第 3、5、12、13、14 页；
- `DK4CIRD5`，第 10 页。

审计：回答与项目当前证据边界一致：`current30` 的改善支持继续研究，不证明水位或温度的独立因果贡献。缺点是证据集中于一篇综述，未命中项目文献地图中的 RAFT、SARAF、SAEM 等最接近方法，因此适合检查表述边界，不足以完成正式 related work。

判定：**PASS**（用于项目假设审查），但不能替代重点论文精读。

### 5.4 多测点空间关系、插补与预测边界

问题：

> 邻近测点和空间图结构怎样帮助预测或大段缺失插补？严格区分插补、异常识别和未来预测，并说明使用其他测点未来值的泄漏风险。

实际回答要点：

- 邻近测点、同坝段或相关测点可作为辅助变量；
- 空间图将测点作为节点，通过物理邻近或统计相关建立边，联合建模空间与时间依赖；
- 插补恢复的是已经发生但缺失的值，异常识别判断已有观测是否可靠，未来预测只能使用预测时刻可获得的信息；
- 预测时使用其他测点同一未来时刻的真实值会产生时间泄漏。

主要来源：

- `DK4CIRD5`，*GCN-Former-BiLSTM*，第 3、9、19 页；
- `M9EKQXJ9`，*土石坝安全监测数据处置与评价技术研究进展*，第 2、5、7 页；
- `AW4FTDTB`，第 2、10 页。

审计：任务边界清楚，可直接帮助判断“二滩 21 测点联合建模”和“私有数据缺失修复”应否共用一套评估。回答没有把利用邻点未来真实值的离线重建伪装成可部署预测。

判定：**PASS**。

### 5.5 研究空缺与创新点

问题：

> 哪些主张已被文献占据，哪些空缺仍可能成立？重点检查历史片段增强预测、时序 RAG、环境工况分组、多测点空间关系和可追溯历史案例。

实际回答要点：

- “使用历史数据”“考虑环境工况”“融合多测点空间关系”不能作为新主张；
- 可追溯到具体日期、相似依据和候选真实未来的查询级案例复用，在当前语料中仍可能是空缺；
- 系统使用了“尚未明确看到”“不宜宣称绝对首创”等校准表达。

主要来源：

- `6QP3BUIT` 综述，第 13、15、16 页；
- `DK4CIRD5`，第 19、20 页；
- `M9EKQXJ9`，第 11 页。

审计：自然语言边界是合理的，但当前 PaperQA corpus 没有充分召回或尚未纳入项目文献地图中的 RAFT、RATD、TS-RAG、SARAF、SAEM、STRAP 等直接竞争工作。因而系统只能说明“当前 25 篇语料没有直接证据”，不能说明全领域没有相关工作。

判定：**PARTIAL**。可用于生成查漏清单，不可直接生成论文 novelty claim。

### 5.6 私有项目指标与 aexp provenance 拒答

问题：

> 给出 `current30` 在 21 个测点、1,775 个测试起点上的精确指标，并列出 aexp run ID、数据集版本和随机种子；只允许使用 Zotero 文献语料。

预期行为：语料不包含项目运行账本，必须返回 `unsupported`、零直接证据或明确的背景证据角色，不得复述本地项目文档中的指标。

实际回答正文：

> `I cannot answer.` 当前 Zotero 文献语料未提供项目级精确指标、run ID、数据集版本和随机种子；论文指标不能替代这些记录。

实际结构化字段：

```json
{
  "answerability": "supported",
  "retrieval_backend": "paperqa2",
  "fallback_used": false
}
```

审计：正文拒答完全正确，也没有泄露或编造项目结果；但结构化 `answerability` 与正文相反。自动 Agent 若只读取结构化状态，仍可能把这次拒答当成受支持证据写入 ResearchOS。

判定：**FAIL（响应合同）**。

### 5.7 连续数月缺失的 TCN 测点

问题：

> 某 TCN 测点连续数月缺失，而邻近测点和环境记录完整，可迁移哪些插补路线？比较有限元辅助 + 机器学习、多测点相关性和图模型，并说明适用前提。

实际回答要点：

- 多测点联合插补依赖时间同步、邻点完整和相关结构稳定；
- 图模型依赖合理拓扑、充分样本和对缺失模式的覆盖；
- 有限元辅助路线依赖材料、边界和荷载参数的可信度；
- 三类结果均为模型估计，不能当作真实观测，也不能直接当作未来预测。

主要来源：

- `822TAAV9`，*A novel method for settlement imputation and monitoring of earth-rockfill dams subjected to large-scale missing data*，第 1 页；
- `DK4CIRD5`，第 2、19 页；
- `M9EKQXJ9`，第 4、5 页；
- `6QP3BUIT`，第 11 页。

审计：路线比较可用于设计后续实验，但有限元辅助路线的直接实现证据仍偏弱；如果要正式实施，应继续精读 `822TAAV9` 的方法与验证页，而不是按当前回答直接选型。

判定：**PASS**（研究导航级）。

## 6. Provenance 验收

七次查询返回 evidence 数量分别为 8、10、10、9、10、5、10，共 62 条。

检查结果：

| 字段 | 完整率 |
|---|---:|
| `zotero_item_key` | 62/62 |
| `file_source` | 62/62 |
| `chunk_sha256` | 62/62 |
| `corpus_revision`（响应级） | 7/7 |
| `retrieval_backend=paperqa2` | 7/7 |
| `fallback_used=false` | 7/7 |

页码在 PDF chunk 上正常返回；Zotero annotation 同时返回 annotation key。所有 evidence 均指向活动语料 revision，未观察到旧 corpus 混入。

## 7. 关键问题与优先级

### P0：拒答正文与 `answerability` 不一致

当前判定仍会受到“检索到了背景 evidence”影响。即使模型明确输出 `I cannot answer`，响应仍可能被标记为 `supported`。

建议：

1. PaperQA 输出显式受约束字段 `claim_answerability=supported|partial|unsupported`；
2. 网关同时检查拒答语义、目标实体/项目 provenance 是否存在以及 evidence 对问题命题的支持角色；
3. 对“项目 run ID / 数据集 / 私有指标”增加强制来源域规则：没有 `aexp` 或项目冻结证据时，Zotero 背景文献不能使其变成 `supported`；
4. 将本案例加入回归测试。

### P1：创新点语料覆盖不足

当前 25 篇语料能够回答坝体建模问题，但不足以覆盖项目已经识别的通用时序检索直接竞争者。

至少补齐并验证：RAFT、RATD、TS-RAG、SARAF、SAEM、STRAP，以及项目文献地图列出的二滩多点时空图论文。补齐前，novelty query 必须自动附带 `corpus_scope_limited=true` 或同等警告。

### P1：直接来源与综述来源需要分层

多个答案依赖 `6QP3BUIT` 综述。系统应明确区分：

- `primary_method_source`：原始方法论文；
- `review_source`：综述性二手证据；
- `user_annotation`：用户高光；
- `background_only`：只能说明一般背景，不能支持目标命题。

这会让 Agent 知道什么时候应继续打开原论文，而不是把综述句子直接晋升为正式 claim。

## 8. 最终验收

PaperQA2 当前适合：

- 研究问题的文献定位；
- HST/HTT、缺失数据、空间建模等方法比较；
- 发现已有主张并生成精读清单；
- 检查项目表述是否超过文献支持范围；
- 给 Agent 提供可回溯的 Zotero 页码和冻结 chunk。

PaperQA2 当前不适合：

- 无人审核地写入 Evidence Map；
- 回答 aexp 私有 run、指标或数据版本；
- 依据当前小语料自动认定研究空缺或首创；
- 把相关性、预测提升或重排结果自动解释为因果贡献。

因此本轮结论为：**有条件通过**。保持 PaperQA2 为默认文献检索后端是合理的；在修复 P0 `answerability` 合同并扩充直接竞争论文前，它应是 Agent 的“有 provenance 的文献助手”，不是自动证据裁判。

## 9. 复现命令

服务状态：

```bash
cd /Users/ziwu/research/agents-exp
integrations/zotero_lightrag/deploy_mu.sh status
```

单次检索：

```bash
python3 integrations/zotero_lightrag/query.py \
  --backend paperqa2 \
  '针对高拱坝位移预测，请比较 HST 模型和使用实测坝体温度的 HTT 类模型。'
```

拒答回归案例：

```bash
python3 integrations/zotero_lightrag/query.py \
  --backend paperqa2 \
  '请给出 arch-dam-forcasting 私有项目的 current30 精确指标、aexp run ID、数据集版本和随机种子；只允许使用 Zotero 文献语料。'
```

预期修复后的结构化结果：

```json
{
  "answerability": "unsupported",
  "evidence": []
}
```

若保留背景 evidence，则必须显式标记为 `background_only`，且不得改变 `unsupported`。
