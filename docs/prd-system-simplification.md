# PRD：aexp 系统精简与概念冻结

状态：Implemented and accepted（2026-07-24）

版本：1.0

日期：2026-07-24

实施记录与兼容删除期限见
[deprecation-ledger.md](deprecation-ledger.md)。

生产验收记录见
[acceptance-system-simplification-2026-07-24.md](acceptance-system-simplification-2026-07-24.md)。

适用范围：CLI、REST API、MCP、UI-v2、SQLite、Run、Dataset、Artifact、NAS、Freeze、Evidence Map

验收性质：本文件第 13 节为绑定验收标准；未满足时不得宣称精简完成

## 1. 摘要

aexp 的目标不是成为通用数据平台、文件管理器、传输编排器或论文审批平台。它是一个面向
人和 Agent 的科研实验执行与证据系统：

> 在 GPU 资源上可靠执行实验，并将输入、输出和研究结论以可验证、可引用的方式保存在
> NAS。

用户侧只保留四个一等研究概念：

```text
Project
├── Assets
├── Runs
└── Evidence Map
```

其中 `Project` 是研究范围，`Asset` 是不可变文件版本，`Run` 是一次执行，`Evidence Map`
是研究推理。Resource、Storage、Placement、TransferJob、Binding、FreezeFile 等仍可作为
内部实现或管理员诊断对象存在，但不再要求普通用户或 Agent 理解和编排。

统一主流程为：

```text
Asset revision
→ Run
→ published output Asset revisions
→ Evidence Snapshot
→ Release decision
→ formal Evidence claim
```

本 PRD 的首要任务是冻结边界和减少独立写入口，不是重写已经可用的 SSH、rsync、
Placement、TransferJob、SHA-256 或 Evidence Graph 底层。

### 1.1 决策与 supersede 范围

本 PRD 接受后，作为产品表面和心智模型的最新约束。它不推翻旧 PRD 已实现的数据完整性、
传输可靠性、图修订和并发安全规则，但明确覆盖下列旧表面决策：

| 旧决策/现状 | 本 PRD 的最终决策 |
| --- | --- |
| `/projects`、ProjectDefinition、manual category 分别表达项目 | ProjectDefinition ID 成为唯一规范 Project 身份；其他两者迁移为读模型或显式映射 |
| LogicalPath、Placement、Transfer 可作为日常 Agent 工作流 | 保留为内部运输与管理员诊断，不属于普通研究工作流 |
| `aexp://` 与 `storage://` 都可能被解释为默认文件身份 | `asset@revision` 是科研内容身份，`storage://` 是用户可见位置，`aexp://` 只用于内部/高级逻辑路径 |
| Project Run Card 是独立研究结论记录 | 降为 Run interpretation / Evidence proposal 的兼容投影 |
| Agent 可通过 add-node/add-edge 直接改 accepted graph | 默认 Agent 只能提交 proposal；accepted graph 继续经过 revision-aware review |
| Run Freeze 自己发现、选择、传输并发布证据 | 旧命令保留兼容，但新 Evidence Snapshot 只引用已经发布的输出 revision |
| 首页和 Project 都能进入独立 Evidence 工作区 | Project Evidence tab 是唯一创作上下文；全局入口只做索引和跳转 |

若旧文档与本表冲突，以本表为准。旧文档中的 SHA-256、staging、原子 promotion、不可变
revision、CAS、无损迁移和“NAS 不是第二控制面”等安全承诺继续有效。

## 2. 背景与问题

aexp 已经具备远端运行、日志、指标、Artifact inventory、Dataset registry、NAS 直传、
持久 TransferJob、Run Freeze 和 Evidence Map。底层能力并不缺失，主要问题是内部机制
逐渐成为并列的产品概念：

- Dataset upload、register、ingest、materialize、verify、repair 和 evict 分别暴露；
- LogicalRoot、LogicalPath、Placement、TransferPlan 和 TransferJob 可以被独立管理；
- RunInputBinding 和 RunOutputBinding 形成第二套输入输出心智模型；
- Artifact inventory、Run Freeze、workspace projection、aggregation 和 release gate
  被组合成一个过重流程；
- Project Run Card 与 Evidence Map 同时保存研究解释；
- 首页和 Project 内都有 Evidence Map 入口，Agent 也不能自然判断应写入哪张图；
- 普通文件运输与正式科研数据混在一起，用户不知道何时必须使用 aexp；
- UI 把运输状态、科研状态和资源状态混合展示，错误信息既复杂又不透明。

结果是：

1. 用户需要先理解实现细节，才能表达一个简单意图；
2. Agent 面对多个等价入口，容易选择错误命令或形成双写；
3. Dataset、Run、Freeze 和 Evidence 分别重复做 provenance 检查；
4. 每增加一种状态，就倾向于再增加页面、API、MCP 工具和数据库生命周期；
5. 系统在功能上更强，但在使用上更难预测。

## 3. 产品定义与设计原则

### 3.1 一句话产品定义

aexp 是科研实验执行与证据系统：在受控计算资源上运行实验，并把可复现输入、输出和结论
沉淀为 NAS 上的不可变证据。

### 3.2 四个一等研究概念

只有以下概念可以拥有完整的普通用户创建、查看和生命周期入口：

1. `Project`
2. `Asset`
3. `Run`
4. `Evidence Map`

Resource 和 Storage 属于系统管理概念，只进入 Settings 与系统诊断，不进入研究主导航。
Snapshot 和 Release 是证据生命周期动作或状态，不是第五、第六个顶层工作区。

### 3.3 意图优先

用户和 Agent 表达：

- 发布这份数据；
- 用这个数据运行实验；
- 确认实验输出已安全保存；
- 记录它支持或削弱了什么结论。

系统负责解析副本、路径、传输发起者、staging、重试、校验和缓存。

### 3.4 一个事实只有一个可写来源

- Project 元数据由 Project 记录负责；
- Asset 内容身份由不可变 manifest 负责；
- Run 执行事实由 Run 与 final RunManifest 负责；
- 研究关系由 Project 的 primary Evidence Map 负责；
- Placement、Transfer 和 Binding 是派生的操作状态，不得成为第二份科研事实。

### 3.5 透明不等于暴露

正常界面显示用户意图和结果：

```text
正在把 facade-good810@v1 准备到 RTX6000
185 MB / 186 MB · 正在校验
```

展开诊断后才显示 initiator、SSH、rsync、物理路径、Placement 和 TransferJob。内部机制
必须可检查，但不能成为完成正常任务的前置知识。

### 3.6 草稿宽松，正式声明严格

Agent 可以自由记录 Evidence 草稿、问题和下一步计划。只有把结论提升为 formal claim 时，
才要求引用已 Release 的 Evidence Snapshot。门禁阻止不可信声明，不阻止研究过程记录。

### 3.7 不把 smoke 当结果

smoke 只验证运输、启动、状态同步或代码路径，不得作为 formal claim、论文数值或科学结论。

### 3.8 独立写入口判据

如果一个概念拥有独立创建、编辑或删除入口，却不属于 Project、Asset、Run 或 Evidence
Map，应优先把它合并为字段、动作、投影或管理员诊断。不能证明独立生命周期必要性的对象，
不得新增为顶层概念。

## 4. 规范概念

### 4.1 Project

Project 是研究问题、Runs、Assets 和 Evidence Map 的共同范围。

每个 Project：

- 必须有稳定 `project_id`；
- 以 ProjectDefinition ID 作为新写入的规范身份；
- 至多有一个 `active + primary` Evidence Map；
- 新建 Project 时自动创建 primary Evidence Map；
- Agent 根据 Run 的 `project_id` 自动定位 primary Evidence Map，不要求额外选择；
- 可保留 secondary 或 archived 历史图，但它们默认只读，不参与 Agent 自动写入；
- Project 页面不展开所有 Run 卡片，只展示摘要和分页/虚拟化列表。

旧 `/projects` 聚合结果和 manual project category 不按名称自动合并。迁移必须生成显式映射
并保留冲突报告。已有 Project 若没有 primary Map，首次进入 Evidence 时要求一次明确绑定或
创建；一旦绑定，后续 Agent 目标必须确定且无需猜测。

Project Run Card 不再是独立可编辑事实源。它的 question、verdict、metrics 和 next action
逐步变为 Run interpretation 草稿或 Evidence proposal 的展示投影。

Run Mark 仅是可选轻量备注，不承担跨 Run 研究结论，也不能替代 Evidence proposal。

### 4.2 Asset

Asset 是用户对“可被实验使用或产生的文件内容”的统一称呼。它包括：

- Dataset；
- 配置包；
- checkpoint；
- metrics；
- predictions；
- masks；
- figure/table evidence；
- 其他由 Project 明确发布的文件树。

Asset 由逻辑身份和不可变 revision 组成：

```text
asset identity + revision manifest SHA-256
```

用户侧 URI 规则固定为：

- `asset-name@revision`：科研内容身份；
- `storage://<storage>/<path>`：当前可见物理位置；
- `aexp://<workspace>/<path>`：内部或高级逻辑别名，不作为默认 Agent 输入。

同名 Asset 可以产生新 revision，但已发布 revision 不可覆盖。文件发生变化时必须发布新
revision；不能修改旧 manifest 来保持原版本名。

DatasetVersion 在迁移期继续作为兼容模型存在，但用户侧语义是 Dataset 类型的 Asset
revision。Run outputs 也通过同一发布语义成为 Asset revisions。

可信来源是经过完整校验的 manifest，不是路径名、文件修改时间、调用者填写的 hash 或 NAS
目录“看起来存在”。NAS 上副本缺失属于可用性问题，不会改变 revision 身份。

### 4.3 Run

Run 是一次不可变执行记录。它消费固定 Asset revisions，并产生新的 Asset revisions。

formal/ablation Run 在启动前必须固化：

- Project；
- Dataset/输入 Asset revision 及 manifest SHA-256；
- seeds；
- Git commit、dirty state 和 diff hash；
- project config hash；
- split/evaluation protocol；
- 运行命令和解析后的环境。

RunInputBinding 和 RunOutputBinding 保留为执行层记录，但成为 Run 的内部字段/派生状态。
用户不单独创建、编辑或删除 Binding。

Run 结束不等于输出已持久化。UI 必须分别显示：

```text
执行：succeeded
输出：publishing | verified | failed
```

只有输出发布并校验完成后，才可创建 Evidence Snapshot。

### 4.4 Evidence Map

Evidence Map 是 Project 内唯一的研究推理图。它只选择会改变研究决策的内容，不镜像全部
Runs。

V1 继续使用既有语义节点和关系，例如：

- dataset / protocol / run / claim / issue / plan；
- uses / supports / weakens / reveals_issue / supersedes / next_step。

Agent 完成重要 Run 后必须留下以下之一：

1. Evidence proposal 草稿；
2. `no_graph_impact` 和简短原因。

Agent 默认向 Run 所属 Project 的 primary Evidence Map 提交 proposal。用户不需要选择
Map ID。用户仍可在 UI 中审核、修改、接受或拒绝 proposal。

旧的 direct add-node/add-edge 和 generic `aexp_cli` 不得绕过 proposal review 或 formal
claim gate。它们在兼容期只能转发至受控服务，或明确标为 expert/manual 操作并保留同样的
revision、CAS 和权限边界。

### 4.5 Evidence Snapshot

Evidence Snapshot 是对一个 Run 已发布输出 revisions 的不可变引用，不是新的文件运输
系统。

最小语义为：

```text
snapshot_id
run_id
output_revision_manifest_sha256
created_at
```

实现可保存必要的审计字段，但 Snapshot 不得：

- 从远端即时发现或猜测文件；
- 自己选择 artifact glob；
- 创建独立运输协议；
- 自动修复输入或输出；
- 跨多个 Run 打包；
- 执行聚合或 release gate；
- 保存可变 workspace 路径作为内容身份。

旧 `aexp run freeze` 保留为兼容别名，最终映射为：

```text
ensure outputs published
→ create Evidence Snapshot
```

### 4.6 Release

Release 是附加在 Evidence Snapshot 上的不可变判定事件：

```text
Snapshot
→ project aggregate
→ release gate
→ released | blocked
```

- `released`：允许 formal claim 引用；
- `blocked`：原始输出仍安全存在，但不允许成为正式论文证据；
- 操作故障与规则不通过必须分开报告；
- Release 只追加，不原地修改；修正后产生新的 Release event，必要时产生新 Snapshot；
- 每个 Project 只允许配置一个 aggregate command 和一个 gate command；
- 不发展 hook marketplace、插件依赖系统或多阶段审批流。

### 4.7 Resource、Storage 与 NAS

Resource 和 Storage 是管理员概念：

- Resource 描述 Mac、NAS 或 GPU 节点及连接能力；
- Storage 描述 durable storage root；
- NAS 是默认容量中心和可信副本位置，不是第二套控制面；
- aexp 数据库、策略和调度继续位于 Mac；
- GPU 与 NAS 直接传输，Mac 默认不进入大 payload 路径。

普通用户可看到“NAS 可用”“RTX6000 到 NAS 不可达”等可操作结论。完整连接矩阵、SSH
错误和 initiator 选择放入 Settings / System Activity。

## 5. 普通文件与实验文件的边界

aexp 不接管 NAS 上的全部文件。

### 5.1 Unmanaged 通道

临时文件、个人归档和手工整理可以使用 SSH、rsync 或轻量 storage CLI：

```text
local/NAS path
→ unmanaged copy
```

这类文件：

- 可以被 Agent 查看和复制；
- 不自动生成科研 revision；
- 不能直接满足 formal Run 或 formal Evidence provenance；
- 需要用于实验时，必须显式 publish 为 Asset revision。

### 5.2 Managed 通道

Dataset、formal Run 输入、Run 输出和论文证据使用：

```text
publish Asset revision
→ verified manifest
→ managed use
```

Agent 可以使用只读 SSH 排查 NAS；危险写入、覆盖和删除仍遵守 aexp 的确认与引用保护。

## 6. 产品信息架构

### 6.1 主导航

主入口以 Project 为中心：

```text
Projects
Active Runs
Settings
```

进入 Project 后固定为：

```text
Overview | Runs | Assets | Evidence
```

- Overview：当前 claim、issue、next step、最近重要 Run；
- Runs：分页/虚拟化执行记录；
- Assets：Project 数据和输出 revision；
- Evidence：该 Project 的 primary Evidence Map。

全局 Evidence 页面不再作为并列创作入口。若保留，只提供跨 Project 搜索与只读跳转。

### 6.2 Settings

以下内容移入 Settings：

- Resources；
- Storage/NAS；
- connection health；
- System Activity；
- Transfer diagnostics；
- printer；
- service/runtime diagnostics。

### 6.3 UI 状态语言

用户状态必须先回答“现在能否继续”：

```text
可运行
正在准备数据
实验运行中
输出正在保存
证据可创建
证据已冻结但不可发布
可用于正式结论
需要处理
```

技术状态和错误码放在展开详情中，不用一个笼统的 `degraded` 覆盖多条数据通路。

## 7. Agent 接口

Agent 默认只使用意图级工具：

- publish/get/list Asset；
- submit/get/list/cancel Run；
- create/get Snapshot；
- draft/plan/accept Evidence proposal；
- inspect Project；
- inspect system readiness。

低层 transport/placement 接口：

- 不进入默认 MCP 工具列表；
- 不由 Agent 为正常工作流直接编排；
- 保留在管理员诊断 namespace；
- 返回低噪、分页的摘要；
- 技术详情必须按需获取，不能在 run list 中携带几十 KB。

期望的 Agent 心智模型：

```text
这份数据是否已有可信 revision？
这个 Run 消费了哪个 revision？
输出是否已安全发布到 NAS？
这个结果能否支持正式结论？
```

## 8. 内部边界与兼容策略

### 8.1 保留的底层

以下实现继续保留并复用：

- SSH executor 和连接池；
- LogicalPath 解析与安全边界；
- Placement observation；
- TransferPlan / TransferJob；
- staging、断点恢复、完整 SHA-256 和原子 promotion；
- RunInputBinding / RunOutputBinding；
- DatasetVersion / DatasetMaterialization 兼容表；
- Evidence Graph revision、CAS 和 proposal review。

本 PRD 不授权为了“名称统一”而立即迁移所有表或重写这些模块。

### 8.2 收回的产品表面

以下概念不再拥有普通用户独立工作流：

| 现有概念 | 目标处理 |
| --- | --- |
| StorageTarget / LogicalRoot | 移入 Settings；由系统解析 |
| LogicalPath / Placement | 仅诊断显示，不要求用户创建 |
| TransferPlan / TransferJob | System Activity 中只读/重试诊断 |
| RunInputBinding / RunOutputBinding | Run 内部状态 |
| DatasetVersion | Dataset Asset revision 的兼容实现 |
| DatasetMaterialization | “准备输入”内部阶段 |
| Artifact inventory | Run outputs 的发现信息 |
| RunFreeze / RunFreezeFile | Snapshot 兼容层，停止扩展 |
| Project Run Card | Evidence proposal / Run interpretation 投影 |

### 8.3 兼容期限

- 第一阶段保留旧 CLI/API/MCP，标记 deprecated 并指向新入口；
- 新 UI 和 Agent 工具不得继续写旧模型；
- 第二阶段旧写入口变为只读兼容视图；
- 在新模型连续两个版本通过迁移与回归验收后删除旧写入口；
- 不允许长期双写；每个阶段必须明确唯一写路径。

## 9. 关键工作流

### 9.1 发布 Dataset

```text
选择本地或 NAS 目录
→ 生成完整 manifest
→ staging
→ 目的端完整校验
→ 原子发布 Asset revision
→ 返回 dataset@version + manifest SHA-256
```

同名同内容幂等成功；同名不同内容必须发布新 revision/version，不得覆盖。

### 9.2 提交 formal Run

```text
选择 Project + command + input Asset revisions
→ provenance readiness plan
→ 自动准备输入到 compute cache
→ 校验 revision
→ 启动 Run
```

缺少数据、seed、协议或 Git provenance 时返回结构化 blocker；不得从命令字符串猜测。

### 9.3 保存 Run 输出

```text
Run process exits
→ discover declared outputs
→ publish output Asset revisions to NAS
→ verify manifest
→ Run output state = verified
```

执行成功但输出发布失败时，不得显示为“全部完成”。

### 9.4 形成正式证据

```text
verified Run outputs
→ create Snapshot
→ aggregate + gate
→ released
→ Evidence proposal can promote a formal claim
```

草稿 proposal 可以在 Release 前存在，但 UI 必须标记“尚不可作为正式证据”。

## 10. 明确不做

本轮以及后续实现不得：

1. 把 aexp 扩张成通用 NAS 文件管理器；
2. 新建第二套传输、队列、数据库或 NAS MCP；
3. 让 Freeze 继续承担传输、文件选择、修复和跨 Run 打包；
4. 建立 aggregation/gate 插件市场或任意多阶段 workflow engine；
5. 为 Evidence Map 建立第二份可写 YAML/JSON 事实源；
6. 把所有 Run 自动加入 Evidence Map；
7. 向用户暴露独立 Placement/Binding 编辑器；
8. 允许 formal Run 使用未验证路径或调用者自填 hash；
9. 允许 formal claim 绕过 released Snapshot；
10. 自动删除 NAS、GPU 或 Mac 上的源文件；
11. 引入 FUSE、SMB 数据面或透明远程 POSIX I/O；
12. 把 smoke/pilot 包装成正式实验结果；
13. 为每个内部状态新增顶层页面、导航入口或 MCP 工具；
14. 在精简迁移期间长期维护新旧双写。

## 11. 分阶段实施

### Phase 0：概念冻结

交付：

- 接受本 PRD；
- 更新核心概念文档和用户术语；
- 建立现有 CLI/API/MCP/UI 写入口清单；
- 为任何新顶层概念增加架构审查门禁；
- 暂停扩展 Freeze、Data Center、LogicalRoot 和 Transfer UI。

停止条件：

- 团队能用四个研究概念解释所有主工作流；
- 每个现有对象都有“保留、隐藏、兼容、删除”结论；
- 不再新增与本 PRD 冲突的写入口。

### Phase 1：产品表面收敛

交付：

- UI-v2 以 Project 为主入口；
- 每个 Project 自动定位唯一 primary Evidence Map；
- Project 内使用 Overview/Runs/Assets/Evidence；
- Transfer/Placement/Storage 进入 Settings/Activity；
- MCP 默认工具只保留意图级操作；
- Run 明确区分执行状态和输出发布状态。

停止条件：

- 用户完成 publish → run → snapshot → evidence 草稿，不接触 Placement、Transfer 或 Binding；
- Project 中大量 Runs 不阻塞浏览其他 Project；
- 从 Project 进入 Evidence 时只显示该 Project 的 primary Map。

### Phase 2：Snapshot 与 Release 解耦

交付：

- Run outputs 使用统一 Asset revision 发布语义；
- 新 Snapshot 只引用已发布 output manifest；
- `run freeze` 变为兼容别名；
- aggregation/gate 从 Snapshot 创建中拆出为 append-only Release event；
- formal claim 门禁只依赖共享 provenance readiness 与 Release 状态。

停止条件：

- Snapshot 创建不启动新的文件发现或自有 rsync；
- Release 失败不改变原始 Snapshot；
- Dataset、Run submit、Snapshot 和 Evidence 共用一套 readiness 规则。

### Phase 3：兼容清理

交付：

- 旧写入口改为只读兼容视图；
- 停止双写；
- 完成真实数据库迁移演练；
- 连续两个版本无旧写依赖后删除废弃入口；
- 更新 README、CLI help、MCP schema 和 UI 文案。

停止条件：

- 所有新写入只经过四概念模型；
- 旧记录仍可读；
- 生产数据库、Run、Dataset、Freeze 和 Evidence 数据无丢失。

## 12. 风险与控制

### 12.1 Asset 范围再次膨胀

Dataset 往往少而大，Run outputs 往往多而小。两者可以共享身份语义，但 UI 不得提供一个
无限增长的全局 Asset 列表。Asset 必须按 Project、类型、Run 和时间分页。

### 12.2 Snapshot 重新长成 Freeze

任何新增 Snapshot 字段都必须回答：它是否只描述已经发布的内容？若字段用于传输、选择、
聚合、修复或审批，应放回相应服务，不得加入 Snapshot。

### 12.3 Release 变成审批平台

Release 只保存一次不可变 aggregate/gate 判定，不提供角色流转、会签、可变审批状态或
插件编排。

### 12.4 兼容层永久存在

deprecated 入口必须记录使用量、删除版本和替代命令。没有删除期限的兼容层不予接受。

### 12.5 精简变成大规模重写

Phase 1 优先增加 facade、导航和默认行为，不先合并数据库表。只有证明旧模型妨碍唯一写
路径时，才进行数据模型迁移。

## 13. 绑定验收标准

以下标准必须由自动测试或可重复验证记录证明。页面能打开、请求返回 200、CUPS 接受任务
或 rsync 退出 0，均不能单独证明语义正确。

### 13.1 概念与信息架构

#### IA-01 四概念边界

用户文档、UI-v2 主导航、CLI help 和默认 MCP 工具中，研究工作流只使用 Project、
Asset、Run 和 Evidence Map。Resource/Storage 只作为 Settings 管理概念；Placement、
TransferJob 和 Binding 不作为普通用户任务入口。

#### IA-02 Project 唯一 primary Map

新建 Project 自动创建或绑定唯一 `active + primary` Evidence Map。同一 Project 不能存在
两个 active primary Map。Agent 只凭 `project_id` 即可提交 proposal。

#### IA-03 Project 浏览

Project 列表不内嵌全部 Run。一个拥有至少 1000 个 Runs 的 Project：

- 首屏不请求或渲染全部 Runs；
- 可在固定高度区域分页或虚拟滚动；
- 用户无需滚过其全部 Runs 即可进入下一个 Project；
- 切换 Project 后不会混入前一 Project 的 Run 或 Evidence。

#### IA-04 单一 Evidence 创作入口

Project 的 Evidence tab 打开该 Project 的 primary Map。全局 Evidence 页面若保留，只提供
搜索和跳转，不提供另一个默认创作上下文。

### 13.2 Asset 与数据可信性

#### ASSET-01 不可变 revision

相同名称和相同 manifest 重复 publish 幂等返回同一 revision。相同名称但内容变化不会
覆盖旧 revision，而是要求或创建新 revision。旧 manifest 和文件 hash 保持可验证。

#### ASSET-02 完整验证

文件和目录 revision 由排序、规范化、完整 SHA-256 manifest 计算。大于 512 MiB 的文件也
完整哈希，不允许按大小跳过。

#### ASSET-03 路径不是真实身份

仅提供 NAS 路径、调用者填写 hash、mtime 或 `registered` 元数据不能满足 formal readiness。
必须存在 registry 中匹配且 `verified` 的 Asset/Dataset revision。

#### ASSET-04 managed/unmanaged 边界

SSH/rsync/storage CLI 产生的 unmanaged 文件可以浏览和复制，但 formal Run 引用它时得到
结构化 `asset_unpublished` 或等价 blocker。publish 后才可使用。

#### ASSET-05 副本缺失

删除 cache 或 replica 后，revision 身份不改变，系统显示副本缺失并可重新 materialize。
不得把已完成的旧 TransferJob 当作当前副本仍存在的证明。

### 13.3 Run

#### RUN-01 formal provenance

formal/ablation Run 缺少 Project、verified input revision、seed、Git provenance、config
hash、split 或 evaluation protocol 时，在远端启动前被结构化阻止。

#### RUN-02 输入准备

Run submit 自动完成 plan、materialize 和 destination verification。正常用户无需创建
LogicalRoot、Placement、TransferPlan 或 Binding。

#### RUN-03 状态分离

执行和输出发布分别建模和显示。至少覆盖：

- process running；
- process succeeded + outputs publishing；
- process succeeded + outputs verified；
- process succeeded + output publish failed；
- process failed。

不得把第二种或第四种显示为“实验全部完成且证据可用”。

#### RUN-04 项目隔离

Project Runs API/UI 只返回该 Project 的 Runs。分页、筛选或缓存切换不得产生重复项目、
空白前三页、第四页才有数据或跨 Project 混入。

### 13.4 Snapshot 与 Release

#### SNAP-01 Snapshot 纯引用

创建 Snapshot 只读取 final RunManifest 和已发布 output revisions，写入不可变 manifest
引用。该操作不执行远端 artifact discovery、不构造自有 rsync、不选择 glob、不修改输出。

#### SNAP-02 幂等与不可变

相同 `run_id + output revision set` 返回同一 Snapshot。输出 revision 变化产生新 Snapshot。
既有 Snapshot 不可覆盖或修改。

#### SNAP-03 Freeze 兼容

旧 `run freeze` 对已发布输出调用时得到与 Snapshot 相同的身份结果；对未发布输出先明确
进入 output publishing，而不是在 Freeze 内建立第二套运输状态机。

#### REL-01 append-only Release

每次 Release 产生不可变 event。重新运行 aggregate/gate 不修改旧 event。Gate 不通过
产生 `blocked`，操作故障产生独立失败类型，原 Snapshot 始终保持可读。

#### REL-02 聚合边界

每个 Project 最多声明一个 aggregate command 和一个 gate command。系统不存在通用 hook
市场、任意步骤编排或隐藏的 gate 绕过。

#### REL-03 formal claim

草稿 claim 可以引用尚未 released 的 Run/Snapshot，但必须标记未验证。formal claim 必须
引用 released Snapshot；smoke/pilot、blocked Release 或缺 provenance 的历史 Run 被拒绝。

### 13.5 Evidence Map

#### EVID-01 默认目标

Agent 提交 Evidence proposal 时只需 Project/Run，不需 Map ID。系统稳定解析到 Project 的
primary Map，并在响应中返回目标 map ID 和 base revision。

#### EVID-02 草稿与审核

proposal 不直接修改已接受图。用户可以 plan、accept、reject；stale revision 返回冲突且
无部分写入。

#### EVID-03 Project Run Card 收敛

新 Agent 写入不再分别更新 Project Run Card 和 Evidence proposal 两份研究结论。兼容
Project Run Card 为只读投影或 proposal envelope；测试证明不存在结论双写漂移。

#### EVID-04 图选择性

完成普通 Run 不自动创建 graph node。重要 Run 必须留下 proposal 或 `no_graph_impact`；
smoke Run 不能支持或削弱 formal claim。

### 13.6 NAS、传输与诊断

#### NAS-01 NAS 定位

NAS 不运行第二套 aexp 数据库、队列或 MCP。控制面仍在 Mac；默认大 payload 路径为
NAS ↔ GPU，不经过 Mac。

#### NAS-02 双通路健康

UI/API 分别显示：

- Mac ↔ NAS control path；
- 每个 compute ↔ NAS data path；
- 最近检查时间和过期状态。

某一 compute 不可达不能被描述为“NAS 故障”，NAS 可用也不能掩盖该 compute 的数据通路
失败。

#### TRANSFER-01 单一运输实现

Dataset prepare、Run inputs、Run outputs 和兼容 Freeze transport 均复用同一
TransferJob/staging/verify/promotion 实现。代码审计和测试证明不存在第二套私有 rsync
状态机。

#### TRANSFER-02 平台能力

传输根据实际 initiator 探测 rsync、flock/lockf 和参数能力。降级不得绕过最终完整 manifest
SHA-256。无安全锁实现时返回结构化错误，不得无锁继续。

#### TRANSFER-03 诊断可达

普通工作流不显示 Placement/TransferJob 编辑器。Settings / System Activity 可查看
transfer stage、bytes、initiator、路径、错误、重试和 cancel，并提供面向用户的首要解释。

### 13.7 API、CLI 与 MCP

#### API-01 唯一写路径

每个四概念操作只有一个规范写端点。旧端点要么转发到规范服务，要么只读并返回 deprecation
metadata；不得双写两个事实源。

#### CLI-01 主流程

CLI 可以在不调用 filespace/placement/transfer 子命令的情况下完成：

```text
publish Asset
→ submit Run
→ inspect output publishing
→ create Snapshot
→ submit Evidence draft
→ plan Release/formal promotion
```

JSON 模式只输出结构化结果，进度和诊断不污染 stdout。

#### MCP-01 低噪默认

默认 Agent 工具按 Project、Asset、Run 和 Evidence 命名。列表默认有 limit/cursor，并
返回紧凑摘要；manifest、完整 command、git status 和 transport details 必须显式获取。

#### MCP-02 管理员隔离

Storage、Placement 和 Transfer 诊断工具不出现在默认科研工具集合中。需要时通过明确的
system/admin namespace 调用，并保留审计事件。

### 13.8 数据库与迁移

#### DB-01 无损迁移

真实数据库备份迁移后：

- 所有既有核心表行数不减少；
- Run、DatasetVersion、Artifact、RunFreeze、Project Run Card 和 Evidence Graph 可读；
- `PRAGMA integrity_check` 返回 `ok`；
- 重复运行 migration 幂等。

#### DB-02 单写验证

迁移阶段测试记录每个对象的规范写路径。除明示的事务内兼容投影外，不存在长期 dual-write。

#### DB-03 兼容删除期限

每个 deprecated 写入口都有替代入口、弃用版本、观测方式和计划删除版本。连续两个版本
验证无调用后才可删除；删除前旧数据仍可读。

### 13.9 UI-v2、回归与部署

#### UI-01 状态可理解

eligible、preparing、running、publishing、verified、snapshot、blocked、released 和 failed
均有独立文本含义；不能只靠颜色。错误首先说明影响和下一步，再提供技术详情。

#### UI-02 无白屏

API 慢、单项失败、缓存过期、WebSocket 断线、大 manifest 和旧 schema 数据均不能导致
UI-v2 白屏。Error Boundary 提供可恢复操作，刷新后状态不倒退。

#### TEST-01 自动测试

完整 Go 和 UI-v2 测试通过。新增测试覆盖本节所有 P0 标准；transport smoke 明确标记为
smoke，不进入科学结果或 Evidence。

#### BUILD-01 生产构建

先完成 UI-v2 production build，再构建嵌入新 assets 的 Go binary。不得使用全局包安装，
不得把旧前端嵌入新二进制。

#### DEPLOY-01 安全部署

替换服务前：

1. 备份 SQLite 数据库；
2. 在备份上迁移并核对行数；
3. 运行 integrity check；
4. 完成 Go/UI 测试和 production build；
5. 保留旧二进制 rollback 副本。

替换后重启 `com.ziwu.aexp`，验证 API health、UI-v2 assets、Project 隔离、Run 状态同步、
NAS 通路和僵尸进程数量。失败则恢复旧二进制，不删除任何远端数据。

## 14. Definition of Done

本 Goal 只有在以下条件全部满足时才能标记完成：

- Phase 0 至 Phase 3 的停止条件均满足；
- 第 13 节所有 P0 验收项有自动测试或可重复记录；
- 普通用户和 Agent 可在不理解 Placement、TransferJob、Binding 或 LogicalRoot 的情况下
  完成完整科研主流程；
- Snapshot 不再拥有独立运输和 artifact 选择逻辑；
- Project 自动确定唯一 primary Evidence Map；
- formal Run 和 formal claim 共用统一 provenance readiness；
- 旧数据库迁移无损、旧数据可读、无长期双写；
- UI-v2、CLI、API 和 MCP 使用一致术语；
- 已构建并部署的二进制与验收通过的源码状态一致；
- 未完成的后续工作明确列出，且不违反本 PRD 的概念边界。

## 15. 后续变更门禁

任何新增表、CLI 命令、API、MCP 工具或 UI 页面，如果引入新的独立写生命周期，必须回答：

1. 它为什么不能是 Project、Asset、Run 或 Evidence Map 的字段、动作或投影？
2. 它是否会产生第二份事实源？
3. 普通用户是否必须理解它才能完成主流程？
4. 它的删除或合并条件是什么？

不能明确回答时，不接受该功能进入实现。
