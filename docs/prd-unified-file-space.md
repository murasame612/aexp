# PRD：aexp 统一逻辑文件空间与跨 Resource 数据流

状态：Accepted for implementation  
版本：v1.1  
日期：2026-07-15  
目标版本：aexp 下一主版本  
适用范围：CLI、REST API、MCP、UI-v2、SQLite、executor、Run、Artifact、Dataset compatibility、Run Freeze

## 1. 摘要

> v1.1 产品表面决策：统一逻辑文件空间继续作为 aexp 的内部运输与 Run I/O
> 底座，但不要求用户或 Agent 在日常 NAS 使用中先配置 LogicalRoot。默认心智模型只有
> “文件位置、冻结版本、后台传输”；路径映射、Placement 和 transfer initiator 属于高级
> 诊断信息。

aexp 将增加一个统一逻辑文件空间。日常文件以 `storage://<name>/<path>` 表达当前
位置；正式实验需要固定身份时，以不可变数据版本及 manifest SHA-256 表达。只有需要
跨资源稳定别名或排查副本时，才使用 `aexp://<workspace>/<path>` 和 Placement。所有
受管理的数据移动统一由持久化 TransferJob 执行，但正常使用不要求理解该状态机。

NAS 是默认的 durable placement，但不是另一套控制平面。Mac、NAS 和训练节点继续统一
建模为 Resource；它们可按有向连接能力成为传输发起者。Mac 保留 aexp 数据库、策略、UI
和调度权，不进入训练数据的默认 payload 路径。

Dataset、Artifact 和 Run Freeze 不再各自拥有文件运输实现：

- Dataset 是逻辑路径的可选不可变名称与 revision 标签；
- Artifact 是 Run 输出文件的 inventory 与 role；
- Run Freeze 是 Artifact 选择、provenance、聚合与 release gate；
- TransferJob 是唯一的受管理文件运输状态机。

本需求不引入 `.naslink`、`.aexplink`、FUSE、SMB 数据面或通用文件管理器。

## 2. 背景与问题

当前系统已经具备 StorageTarget、dataset registry、dataset materialize、Artifact
inventory 和 Run Freeze，但文件运输分散在三套实现中：

1. `aexp sync` 在 Mac 上执行 rsync 或 tar fallback；
2. `dataset materialize` 在 NAS push 与 compute pull 之间选择；
3. Run Freeze 在 NAS pull 与 compute push 之间选择，并单独实现 staging、SHA-256 和
   promotion。

这造成以下问题：

- Agent 必须理解 DatasetVersion、Materialization、Artifact 和 Freeze 的不同运输入口；
- registry 中记录的位置可能存在，但远端文件已经缺失，Agent 无法区分事实与缓存；
- 同一个目录被表达成多个机器路径，缺少稳定身份；
- materialize、freeze 和 sync 重复构造 SSH/rsync、fallback 和 staging；
- 普通 sync 没有持久任务，进程退出后无法统一恢复；
- materialize 在 rsync 退出 0 后直接标记 Ready，没有填充现有的校验字段；
- Dataset register 可以更新同名版本，不能承担不可变科研身份；
- NAS 自身健康与 NAS 到每台训练节点的路径被压成一个 `degraded` 状态；
- 正式 Run 仍可能缺少输入 manifest hash 和 seeds。

当前真实数据库中尚无 DatasetVersion、DatasetMaterialization 或 Run Freeze 记录。这允许
我们在不破坏既有受管理数据的前提下建立统一底座，同时保留已有 CLI/API 兼容入口。

## 3. 产品目标

### G1：一个逻辑文件身份

Agent 使用一个稳定 URI 表示文件树，不因副本从 NAS 移动到训练节点而改变引用：

```text
aexp://dam-displacement/data/raw
aexp://dam-displacement/checkpoints/run_123/best.pt
aexp://dam-displacement/runs/run_123/results
```

### G2：位置透明但不隐藏

Agent 必须能看到每个已知 Placement 的 Resource、物理路径、角色、当前观察状态、revision、
检查时间和错误。逻辑 URI 不能把 `missing`、`stale` 或 `unreachable` 伪装成 `present`。

### G3：统一、可恢复的运输

Dataset、Artifact、Freeze 和 Run inputs/outputs 通过同一个 TransferJob planner、worker、
staging、验证、promotion 和 reconciler 移动数据。

### G4：NAS 默认保存权威副本

输入数据、checkpoint、结果和论文证据默认在 NAS 保存 durable placement。训练节点保存
可重建 cache；Mac 默认只保存控制面元数据和 profile 明确要求的小文件。

### G5：Agent 决定数据流，策略约束危险操作

Agent 可以检查位置、选择目标 Resource、查看计划、指定或接受连接发起者并启动传输。
复制和创建缓存可以自动执行；覆盖、删除和 evict 必须经过安全规则，且 v1 不自动删除源。

### G6：正式实验锁定输入事实

formal/ablation Run 必须在启动前锁定逻辑输入 URI、resolved Placement、manifest SHA-256、
seeds、Git provenance、项目配置哈希和 split/evaluation protocol。不能仅凭
`dataset@version` 名称或命令文本推断。

## 4. 非目标

v1 明确不做：

- 不将 NAS 发展成第二套数据库、MCP server 或控制平面；
- 不把 Mac 私钥复制到 NAS 或训练节点；
- 不引入 `.naslink`、`.aexplink` 或其他本地占位链接文件；
- 不提供 FUSE、NFS、SMB 挂载或透明远程 POSIX I/O；
- 不做通用文件编辑器、媒体浏览器或网盘 GUI；
- 不为 NAS 中每个文件建立 SQLite 行；
- 不实现内容寻址 blob store、去重存储或垃圾回收系统；
- 不自动删除训练节点、NAS 或 Mac 上的源文件；
- 不让 Run 进程直接跨 WAN 持续读取 NAS 作为默认训练 I/O；
- 不把 smoke/pilot 当作正式实验结果或论文证据。

## 5. 规范术语

### 5.1 Resource

一台可被 aexp 观察或操作的机器。Resource 可声明以下 role/capability：

- `control`：运行 aexp server、数据库和 UI；
- `storage`：提供 durable storage root；
- `compute`：执行 Run；
- `transfer`：可主动发起 SSH/rsync。

Mac、NAS 和 GPU 节点都属于 Resource。StorageTarget 保留为 Resource 上的命名
StorageRoot compatibility record，不再被解释为独立机器。

### 5.2 LogicalPath

稳定的逻辑文件或目录身份，URI 格式为：

```text
aexp://<workspace>/<relative-path>
```

规范化规则：

- workspace 和 path 必须非空；
- 禁止 `..`、NUL、换行和绝对路径逃逸；
- 重复 `/`、`.` 和尾随 `/` 按统一规则规范化；
- URI 不包含 host、用户名、SSH key 或 `/vol1/...` 等物理实现细节；
- URI 可以指向可变工作路径；formal Run 必须额外 pin revision。

### 5.3 LogicalRoot

Workspace 下少量受管理目录前缀，例如 `data`、`checkpoints`、`runs`、`evidence`。
LogicalRoot 定义默认 durable Resource/StorageRoot 和物理相对根目录。系统不递归登记根目录
中的每个文件。

### 5.4 Placement

LogicalPath 在一个 Resource 上的已知副本或缓存。Placement 至少包含：

```text
logical_uri
resource_id
physical_path
role              authoritative | replica | cache | projection
desired_state     present | absent
observed_state    present | missing | unknown | unreachable | conflict
manifest_sha256
bytes_present
observed_at
checked_at
observation_error
```

`desired_state` 是控制面意图；`observed_state` 是最近一次真实远端检查。两者不得混用。

### 5.5 Revision

文件为完整 SHA-256；目录为排序、规范化 file manifest 的 SHA-256。Revision 可以为空，
表示尚未 pin 的可变工作路径。formal/ablation 输入不得为空。

### 5.6 TransferPlan

无副作用的稳定计划，描述 source、destination、候选 Placement、command host、连接发起者、
fallback 顺序、文件数、字节数、校验方式、staging、promotion、local payload 路径和 blockers。
计划生成 `plan_sha256`。

### 5.7 TransferJob

TransferPlan 的持久执行实例。TransferJob 是唯一受管理运输状态机。

## 6. 目标架构

```text
Agent / Human
     │
     ▼
LogicalPath API / MCP / CLI
     │ locate + inspect + ensure + publish
     ▼
Placement Registry ────── Remote Observation
     │                           │
     └──────────┬────────────────┘
                ▼
          Transfer Planner
                │ plan_sha256
                ▼
        Durable TransferJob
                │
       ┌────────┴────────┐
       ▼                 ▼
 NAS-initiated       Compute-initiated
 SSH/rsync           SSH/rsync
       └────────┬────────┘
                ▼
      staging → verify → promote
                │
                ▼
        Placement observation
```

上层调用关系：

```text
Dataset tag ─┐
Artifact ────┼─> LogicalPath/Placement ─> TransferJob
Run Freeze ──┤
Run I/O ─────┘
```

## 7. 功能需求

### FR-001：Workspace 与 LogicalRoot

- 系统必须支持创建、读取、更新和列出 Workspace LogicalRoot；
- LogicalRoot 必须指向一个已有 Resource/StorageRoot 和安全的相对物理根；
- 一个 workspace 的 LogicalRoot 前缀不得重叠或产生歧义；
- 删除 LogicalRoot 只删除控制面登记，不删除文件；
- 有 active TransferJob 或 formal RunManifest 引用时不得删除。

建议项目配置：

```yaml
data_roots:
  data:
    uri: aexp://dam-displacement/data
    durable_on: ziwudenas
    path: projects/dam-displacement/data
  checkpoints:
    uri: aexp://dam-displacement/checkpoints
    durable_on: ziwudenas
    path: projects/dam-displacement/checkpoints
  runs:
    uri: aexp://dam-displacement/runs
    durable_on: ziwudenas
    path: projects/dam-displacement/runs
```

### FR-002：URI resolve、locate 与 inspect

- `resolve` 将 LogicalPath 映射为一个或多个候选 Placement；
- `locate` 返回 registry 中的已知 Placement，不声称这些副本当前存在；
- `inspect` 必须对指定 Placement 执行真实 `stat`，可选执行 `ls`、`du` 或 hash；
- 观察结果必须持久化 `checked_at` 和结构化错误；
- 超过 TTL 的观察显示 `stale`，不能继续作为 verified input；
- 不可达显示 `unreachable`，不能改写成 `missing`；
- 大目录的 `ls` 必须分页或限制深度，Agent 默认不能递归扫描整个 NAS。

CLI：

```bash
aexp fs roots [--workspace ID]
aexp fs resolve aexp://workspace/path
aexp fs locate aexp://workspace/path
aexp fs stat aexp://workspace/path [--on RESOURCE] [--refresh]
aexp fs ls aexp://workspace/path [--on RESOURCE] [--limit N] [--cursor CURSOR]
aexp fs hash aexp://workspace/path [--on RESOURCE] [--manifest]
```

### FR-003：TransferPlan

- Plan 必须无数据库写入、无目录创建和无 payload 传输；
- source 和 destination 可以是 `aexp://`、`resource://`、`storage://` 或 `local://`；
- LogicalPath 必须在 plan 中解析为明确 source Placement；
- 默认选择最新 verified authoritative Placement；
- 多个可用 source 时，Agent 可以显式选择；
- planner 必须显示首选 initiator 和 fallback 顺序；
- 任何 fallback 都必须出现在计划中，执行时不得隐藏增加新 route；
- plan 必须显示 `local_data_path`；NAS 与 compute 直传时必须为 `false`；
- 目标路径已存在但 revision 未知或不同必须生成 blocker；
- `plan_sha256` 对输入和文件排序稳定，route、revision 或目标变化后必须变化。

CLI：

```bash
aexp transfer plan SOURCE DESTINATION \
  [--initiator auto|nas|compute|mac] \
  [--verify manifest|sha256|none] \
  [--json]
```

### FR-004：持久 TransferJob

状态机：

```text
queued → planning → transferring → verifying → promoting → completed
                   ↘ failed
任意非终态 → cancelling → cancelled
```

要求：

- 创建 job 与保存 plan 必须在同一事务中完成；
- 创建接口立即返回 `transfer_id`，不能等待 SSH；
- staging 名称包含 transfer ID，不能与其他 job 共用；
- rsync 使用 partial/partial-dir，支持相同 job 恢复；
- worker 必须持久化 stage、attempt、bytes_done、total_bytes、files_done、heartbeat 和错误；
- 服务重启时 reconciler 必须检查非终态 job 和 staging，再安全恢复或标记 blocker；
- retry 复用相同 source revision、destination 和 staging；改变输入必须创建新 job；
- cancel 不删除 source；staging 清理由独立、确认式命令处理；
- 操作故障进入 `failed`；目标内容冲突进入结构化 `blocked`，不得覆盖；
- completed job 不可重新执行；同计划重复创建幂等返回原 job。

CLI：

```bash
aexp transfer start --plan-sha256 HASH [--wait]
aexp transfer status TRANSFER_ID [--json]
aexp transfer list [--workspace ID] [--state STATE]
aexp transfer retry TRANSFER_ID
aexp transfer cancel TRANSFER_ID
aexp transfer reconcile [TRANSFER_ID]
```

### FR-005：验证与原子提升

- 文件必须在目标端重新计算 SHA-256；
- 目录必须在目标端生成规范化 manifest 并计算 manifest SHA-256；
- `none` 只允许临时、非 formal、显式请求的普通 sync；
- 只有验证成功后才允许 staging 原子提升为 final；
- final 已存在且 revision 相同则幂等成功；
- final 已存在且 revision 不同则 `blocked/conflict`；
- manifest 必须拒绝路径逃逸、缺文件、额外文件和类型变化；
- 大于 512 MiB 的文件仍必须完整校验，不能沿用 inventory 的跳过规则；
- 完成后写入新的 Placement observation 和 revision。

### FR-006：Resource route 与凭据

- 每个传输计划显式记录 command Resource 与 payload direction；
- NAS 主动连接 compute 使用 NAS 上的专用受限 identity；
- compute 主动连接 NAS 使用 compute 上的专用受限 identity；
- Mac `auth_ref` 只用于 Mac 控制面连接，不能被复制到远端；
- route health 必须区分 `NAS itself` 与每个 `NAS ⇄ compute` edge；
- route observation 有 TTL；stale route 不能静默用于 formal input；
- Agent 可以选择 plan 中已验证的 initiator；不可用选择返回 blocker；
- fallback 的每次尝试、错误和最终 initiator 必须进入 job ledger。

### FR-007：Run inputs

Run submit 增加结构化 input binding：

```yaml
inputs:
  - from: aexp://dam-displacement/data/raw
    to: data/raw
    revision: sha256:...
    mode: copy
```

- submit 开始仍先持久化 queued Run；
- preflight 必须 inspect 目标 Placement；
- revision 已验证且匹配时直接复用 cache；
- missing 时创建/复用 TransferJob 并等待完成；
- unreachable、conflict 或 revision mismatch 阻止启动；
- RunManifest 固化 logical URI、source/destination Placement、revision 和 transfer ID；
- formal/ablation 缺 revision、seed、Git commit、project config hash 或 split/evaluation
  protocol 时必须 blocker；
- smoke/pilot 可以使用未 pin 输入，但必须明确标记为非正式证据。

### FR-008：Run outputs

Run submit 或 recipe 增加结构化 output binding：

```yaml
outputs:
  - from: checkpoints/best.pt
    to: aexp://dam-displacement/checkpoints/{run_id}/best.pt
    role: checkpoint
    required: true
  - from: results/**
    to: aexp://dam-displacement/runs/{run_id}/results
    role: metrics
    required: true
```

- 进程 exit code 决定 Run lifecycle；输出发布使用独立 `data_finalization_state`；
- Run succeeded 但 required output 发布失败时必须显示“计算成功、数据未完成归档”；
- required outputs 必须在远端 discovery 后通过 TransferJob 发布到 durable placement；
- output revision 和 transfer ID 写入 final RunManifest；
- optional output 缺失不改变 Run lifecycle，但进入结构化 warning；
- 不自动删除 compute 源文件。

### FR-009：Dataset compatibility

- Dataset 不再是传输前置条件；任意 LogicalPath 都可 inspect、ensure 和用于 Run；
- `dataset ingest` 是 `fs publish + immutable tag` 的薄包装；
- 同一 `name@version` 只允许相同 logical URI 和 revision 幂等成功；
- 不同 revision 必须拒绝覆盖；
- `dataset materialize` 调用 `fs ensure`，不得拥有独立 rsync；
- `dataset verify/repair/evict` 分别映射到 inspect、ensure 和通用 placement eviction；
- 旧 DatasetVersion/Materialization 数据和 API 保留兼容读取；迁移不删除表。

### FR-010：Artifact 与 Freeze compatibility

- Artifact inventory 继续描述 Run 输出的当前远端事实，不承担运输；
- Freeze 继续拥有 provenance、role selection、raw/release manifest、aggregate 和 gate 状态；
- Freeze raw evidence 的数据移动必须创建 TransferJob；
- Freeze worker 不再自行拼接 rsync/SSH；
- Freeze restore/materialize 调用 `fs ensure`；
- Freeze 与 TransferJob 状态分开：运输失败映射为 freeze failed stage，gate 失败仍为 blocked；
- frozen/released manifest URI 使用逻辑 URI 或 storage URI，不使用 `.naslink` 文件。

### FR-011：安全 eviction

P1 增加通用 eviction：

```bash
aexp fs evict aexp://workspace/path --from RESOURCE
```

- v1 P0 不自动调用 evict；
- evict 前必须存在另一个 revision 完全匹配的 verified authoritative Placement；
- source 是唯一 verified copy 时必须拒绝；
- 删除目录必须限制在受管理 root 内，并显示真实物理路径和预计字节数；
- 人工 CLI/UI 需要确认；Agent MCP 需要显式 `expected_plan_sha256` 和授权字段；
- eviction 结果更新 observed state，不删除 LogicalPath 身份、RunManifest 或 manifest。

### FR-012：Agent MCP

新增类型化、低噪工具：

```text
aexp_storage_stat
aexp_storage_list
aexp_storage_locations
aexp_storage_copy

# 高级诊断与两阶段运输
aexp_list_workspace_paths
aexp_resolve_path
aexp_inspect_path
aexp_list_path
aexp_plan_transfer
aexp_start_transfer
aexp_get_transfer
aexp_retry_transfer
aexp_cancel_transfer
aexp_ensure_path
```

要求：

- 日常 Agent 流程优先使用四个 `aexp_storage_*` 门面，不要求提供 revision、plan hash、
  initiator 或物理路径；
- `storage_copy` 自动完整计算并固定当前源 SHA-256，只扫描一次后创建持久 TransferJob；
- `storage_copy` 默认禁止覆盖不同 revision，冲突返回结构化 blocker；
- 默认返回 summary，不返回完整 manifest 或 SSH command；
- list 工具必须支持 limit/cursor；
- 结构化返回 present/missing/unknown/unreachable/conflict；
- plan 返回 blockers、route、initiator、payload direction、bytes 和 plan hash；
- 大 manifest 独立按需读取；
- `aexp_list_runs` 同步增加 limit/cursor/project/status/important，并默认返回 RunSummary。

### FR-013：REST API

最低 API：

```text
GET    /workspaces/{id}/paths
POST   /paths/resolve
POST   /paths/inspect
GET    /paths/list
POST   /transfers/plan
POST   /transfers
GET    /transfers
GET    /transfers/{id}
POST   /transfers/{id}/retry
POST   /transfers/{id}/cancel
POST   /paths/ensure
```

- plan 无副作用；
- create 携带 `expected_plan_sha256`，返回 202；
- 过期 plan 返回 409；
- manifest、events 和详细命令单独加载；
- transfer list 使用 summary、cursor 和 `updated_since`；
- 浏览器不代理 payload。

### FR-014：UI-v2

不新增一个大型 NAS 平台页面。Data Center 默认只呈现三个用户概念：

1. 主存储位置：大文件位于哪个 NAS、容量和可用性；
2. 冻结的数据版本：正式实验输入的版本、manifest hash 和节点缓存；
3. 后台传输：来源、目的地、状态、进度和简短失败原因。

LogicalRoot、Placement、initiator、route、payload 是否经过 Mac 等机制放入关闭的高级
诊断区域；高级区域未展开时不得继续轮询 LogicalRoot 和 Placement。StorageTarget 卡片
分别显示：

- management access（Mac→NAS）；
- NAS 自身容量与读写；
- 每个 compute edge；
- 不再因为未使用的 compute edge 不可达而把 NAS 自身标为故障。

Run Detail 显示 inputs、outputs、关联 TransferJob 和 data finalization，不增加链接文件。

### FR-015：单一运输实现与可测试边界

- 新增可注入的 `RemoteFS` 与 `Transport` 接口，分别承担远端观察和 payload 运输；
- 只有 transfer 模块可以构造受管理的 SSH/rsync 命令、staging、retry 和 promotion；
- Dataset、Freeze、Run I/O 只通过 TransferService adapter 调用运输，不得保留私有 rsync；
- `aexp sync` 可暂时保留为显式 unmanaged utility，但 UI/MCP 不得把它显示为可恢复任务；
- API server 通过依赖注入调用 TransferService，不在 handler 内 shell-out；
- fake Transport 必须能确定性模拟超时、部分进度、hash mismatch、进程崩溃和 fallback。

## 8. 数据持久化需求

v1 建议新增：

- `logical_roots`：workspace prefix 到 durable Resource/root 的映射；
- `path_placements`：受管理 LogicalPath 的副本、角色、revision 与观察状态；
- `transfer_plans`：规范化计划和 plan hash，可设置短 TTL；
- `transfer_jobs`：状态、阶段、进度、route、attempt、heartbeat 和错误；
- `transfer_attempts`：每次 initiator/fallback 的开始、结束和错误；
- `run_input_bindings` / `run_output_bindings`，或等价的规范化 manifest side table。

约束：

- 不为每个普通文件创建 placement 行；只记录 LogicalRoot、显式管理路径、Run binding 和
  transfer 涉及的路径；
- 目录 file list 保存在 manifest 文件或压缩 JSON，不塞入高频 list API；
- 所有迁移只增表/增列，不能删除或重写现有 Dataset、Artifact、Freeze 数据；
- SQLite foreign key 和必要唯一索引必须启用；
- 真实库迁移前必须 `.backup` 演练并比较核心表计数。

## 9. 兼容与迁移

### 9.1 保留

- Resource 和 StorageTarget ID/URI；
- `storage://<name>/...`；
- DatasetVersion、DatasetMaterialization API；
- Artifact inventory；
- Run Freeze scientific state machine；
- `aexp sync` 作为显式的 expert/unmanaged utility。

### 9.2 改造

- `dataset ingest/materialize` 改为 LogicalPath/TransferJob wrapper；
- Freeze raw transfer 改为 TransferJob；
- RunManifest 增加 logical input/output bindings；
- Data Center 默认隐藏空 Dataset registry 的复杂状态。

### 9.3 不做自动推断

- 不从当前目录名猜 logical URI；
- 不从命令文本猜 Dataset 或 input revision；
- 不从旧 `.naslink` 猜 placement；
- 旧 Run 缺 provenance 时返回 blocker，不补写虚假事实。

## 10. 安全与权限

- 远端 transfer identity 必须独立、可撤销、最小权限；
- 计划和日志中不得返回私钥内容；
- physical path 必须经过 Resource/StorageRoot 边界校验；
- shell 参数必须统一转义，禁止换行、NUL 和路径逃逸；
- transfer worker 不能执行调用者提供的任意 shell；
- overwrite、evict 和 staging cleanup 是独立权限；
- UI 和 MCP 默认只允许 copy/ensure，不允许隐式 delete；
- AgentEvent 记录 plan、start、retry、cancel、fallback、complete 和 evict。

## 11. 可靠性与可观察性

- TransferJob 进度更新不得依赖高频完整表查询；
- SSE/轻量变化订阅优先，30 秒 summary polling fallback；
- job 心跳超时后显示 stale，不直接标记 failed；
- worker/reconciler 错误向上返回结构化 code、stage、retryable；
- NAS 与 compute 路径状态独立；
- 日志必须区分 control-plane connection 与 payload route；
- 任何 completed job 都能回答“从哪里、由谁、经过哪条 route、传到哪里、验证了什么”。

## 12. 分阶段实施

### Phase 0：契约冻结

- 本 PRD accepted；
- canonical terminology 写入 concepts/mod-store/mod-api；
- 现有 Dataset/Freeze 入口标记 compatibility，不再扩展独立运输逻辑。

### Phase 1：LogicalPath 与 Placement

- schema/store/model；
- URI parser、root resolver、remote stat/ls/hash；
- CLI/API/MCP read-only 工具；
- UI-v2 logical roots 与 placement observation。

### Phase 2：TransferJob

- plan、持久 job、worker、reconciler；
- rsync partial、route selection、fallback ledger；
- destination verification、atomic promotion；
- progress SSE 和 UI。

### Phase 3：现有能力收口

- dataset ingest/materialize wrapper；
- freeze raw transfer 使用 TransferJob；
- restore/ensure 使用统一接口；
- generic sync 明确标注 unmanaged。

### Phase 4：Run I/O

- input/output bindings；
- preflight ensure；
- formal provenance blocker；
- output data finalization 和 durable publish。

### Phase 5：安全回收

- verified replica gate；
- evict plan/confirm；
- orphan staging inspect/cleanup；
- quota/retention 作为后续独立策略。

## 13. 验收标准

下面每项均为发布条件。`[AUTO]` 必须进入自动测试；`[REAL]` 必须在真实 NAS/训练节点
完成；`[MANUAL]` 是 UI 或运维检查。

### AC-001：URI 与 root 安全 `[AUTO]`

- 合法 `aexp://workspace/path` 可稳定 parse/normalize/round-trip；
- `..`、绝对路径、NUL、换行、空 workspace 和 root escape 全部拒绝；
- 同一规范化 URI 只对应一个最长前缀 LogicalRoot；重叠 root 保存失败。

### AC-002：Placement 事实边界 `[AUTO]`

- registry 记录 present、远端文件删除后，refresh inspect 返回 missing；
- SSH 超时返回 unreachable，不返回 missing；
- 超过 TTL 返回 stale；
- desired present 与 observed missing 同时保留；
- API、MCP、UI 同时显示 resource、physical path、checked_at、source 和错误。

### AC-003：透明 TransferPlan `[AUTO]`

- plan 返回 source/destination Placement、initiator、command host、payload direction、
  fallback、bytes/files、verification、staging、local_data_path 和 blockers；
- NAS↔compute 直传时 `local_data_path=false`；
- plan 不写数据库、不建目录、不执行 SSH；
- 文件排序不影响 plan hash；route、revision 或 destination 改变会改变 hash；
- 过期 plan 创建 job 返回 409。

### AC-004：Job 原子创建与幂等 `[AUTO]`

- plan/job 同事务写入；故障时无 orphan job；
- create 立即返回 queued transfer ID；
- 同 plan 重复请求返回同一个 job；
- completed job 不重复传输；不同 source revision 创建新 job。

### AC-005：传输与进度 `[AUTO][REAL]`

- 本地测试 fixture 完成 file 和 directory transfer；
- bytes/files 进度单调，不能只从 0 跳到 100%；
- rsync 中断后保留 job 专属 partial staging，retry 可继续；
- 一个 job 的 staging 不包含或接受另一个 job 的额外文件；
- source 不被修改或删除。

### AC-006：目标验证与 promotion `[AUTO][REAL]`

- 目标端重新计算文件和目录 manifest SHA-256；
- 缺文件、额外文件、hash mismatch、symlink/path escape 和类型变化全部阻止 promotion；
- 超过 512 MiB 的文件完整校验；
- final 不存在时原子 promotion；
- final revision 相同幂等成功；不同 revision 返回 conflict 且不覆盖。

### AC-007：崩溃恢复 `[AUTO][REAL]`

- 在 transferring、verifying 和 promoting 三阶段分别终止 worker；
- server 重启后 reconciler 能恢复、完成或返回结构化 blocker；
- 不重复创建 final，不丢失 source，不把未知结果误报 completed；
- retry attempt 和最终 initiator 可追溯。

### AC-008：route 与凭据 `[AUTO][REAL]`

- 测试 NAS-initiated 和 compute-initiated 两种方向；
- 首选方向失败时只尝试 plan 声明的 fallback；
- Mac auth_ref 不出现在远端文件、计划 JSON 或命令输出；
- 不可用 initiator 返回 blocker；
- NAS 本身健康与每个 compute edge 在 API/UI 分开显示。

### AC-009：Run input ensure `[AUTO][REAL]`

- 目标 cache revision 匹配时不创建传输；
- missing 时创建 TransferJob，completed 后才进入 remote launch；
- unreachable/conflict/mismatch 阻止 launch，并保留 queued/preflight 可观察状态；
- RunManifest 固化 logical URI、revision、source/destination Placement 和 transfer ID；
- formal/ablation 缺 revision/seeds/config/Git/split/evaluation protocol 返回精确 blocker；
  smoke 明确非正式。

### AC-010：Run output publish `[AUTO][REAL]`

- successful Run 的 required outputs 发布到 NAS durable placement；
- output publish 失败不伪造进程失败，但 data finalization 明确 failed；
- required output 缺失进入 data finalization blocker；
- final RunManifest 记录 output revision 和 TransferJob；
- compute 源文件仍保留。

### AC-011：Dataset compatibility `[AUTO]`

- 任意 LogicalPath 无 Dataset tag 也可 ensure 和用于 Run；
- ingest 生成目标 manifest 并原子发布后才写 registry；
- 相同 name/version/revision 幂等；不同 revision 拒绝覆盖；
- materialize 调用 TransferJob 且校验后填充 BytesPresent、VerifiedSHA256、VerifiedAt；
- 旧 dataset API/CLI 数据可读，迁移不改变记录计数。

### AC-012：Freeze compatibility `[AUTO][REAL]`

- freeze plan 的科学 blocker 行为保持；
- raw transfer 产生关联 TransferJob，Freeze 不再自行执行 rsync；
- TransferJob completed 且 raw hash 验证后才能 frozen；
- gate 非零仍为 blocked，不误报 failed；
- freeze restore 使用 ensure；大 payload 不经过 Mac；workspace_roles 投影例外且可见。
- 代码检查确认 Freeze、Dataset materialize 与 Run I/O adapter 不再构造私有 rsync 命令。

### AC-013：MCP 与低噪上下文 `[AUTO]`

- 所有新增 MCP 参数映射、错误 code 和异步 ID 正确；
- list 支持 limit/cursor；默认 summary 不包含完整 SSH command、manifest 或 Git status；
- Agent 能通过不超过三次工具调用回答：路径是否存在、在哪、如何传到目标；
- `aexp_list_runs` 默认 summary 且支持 project/status/important/cursor。

### AC-014：UI-v2 `[AUTO][MANUAL]`

- 覆盖 present、missing、unknown、unreachable、stale、conflict；
- 覆盖 queued、transferring、verifying、promoting、completed、blocked、failed、cancelled；
- Placement 卡直接显示 freshness/source/checked/error；
- Transfer 卡显示 initiator、route、payload direction 和字节进度；
- NAS healthy 不因无关 compute edge down 被显示成 NAS failure；
- 大 manifest 独立加载，浏览器不代理 payload；
- 页面无白屏、无运行时 console error。

### AC-015：迁移安全 `[AUTO][REAL]`

- 旧数据库副本迁移前后 resources/runs/artifacts/datasets/freezes 等核心表计数一致；
- 新表初始为空；
- `PRAGMA integrity_check=ok`；
- `PRAGMA foreign_key_check` 无返回行；
- 迁移可重复打开；
- 替换真实二进制前生成 SQLite `.backup`；rollback 能恢复旧二进制和 DB。

### AC-016：真实 NAS 端到端 `[REAL]`

使用专用 scratch namespace，不接触用户已有数据：

1. Mac 发布小型目录到 NAS；
2. NAS→RTX6000 ensure，校验 revision；
3. RTX6000 生成模拟输出并发布回 NAS；
4. 中断一次传输并验证 retry/resume；
5. 传输一个大于 512 MiB 的非实验 scratch 文件并完整校验；
6. 验证 Mac 不承载 NAS↔compute payload；
7. 清理仅限 scratch staging/final，并由用户确认或使用预授权测试 namespace。

所有测试记录必须标为 transport smoke，不得当作实验结果。

### AC-017：可测试性与故障注入 `[AUTO]`

- `RemoteFS`/`Transport` fake 能覆盖 stat、list、hash、copy、resume、verify 和 promote；
- 自动测试确定性注入 SSH timeout、partial copy、hash mismatch、worker crash 和 fallback；
- 两个 worker 并发 claim 同一 queued job 时只有一个获得执行权；
- API、MCP、Dataset、Freeze 和 Run I/O 的测试不依赖真实 SSH 才能覆盖状态机；
- 受管理传输的新代码中，除 transfer adapter 外不存在直接 `exec rsync`/`exec ssh`。

## 14. 自动测试落点与真实环境脚本

建议的最低测试落点：

- `internal/filespace/path_test.go`：URI、root escape、resolve；
- `internal/filespace/placement_test.go`：registry 与 remote observation；
- `internal/transfer/planner_test.go`：route、plan hash、blocker；
- `internal/transfer/worker_test.go`：claim、progress、retry、cancel、verify、promotion；
- `internal/transfer/reconciler_test.go`：三阶段 crash recovery；
- `internal/integration/data_flow_test.go`：Dataset/Freeze/Run adapter 闭环；
- `internal/api/server_test.go`、`internal/mcp/server_test.go`：协议与低噪输出；
- `ui-v2/src/**/__tests__`：Placement/Transfer/Run finalization 状态矩阵。

真实环境验收通过 `scripts/smoke_storage.sh` 或等价脚本执行，默认不运行，只有显式设置
`AEXP_REAL_STORAGE=1` 才允许进入专用 scratch namespace。脚本必须打印并保存 transfer ID、
source/destination revision、实际 initiator、payload route、清理范围和最终结果；任何产物均标为
transport smoke。

## 15. 发布门槛

发布前必须全部满足：

- PRD Phase 1–4 范围内的 AC-001 至 AC-017 全部通过；
- `go test ./...`、`go vet ./...`、UI-v2 test/typecheck/production build 通过；
- `git diff --check` 通过；
- 真实库 backup、迁移演练、integrity check 和核心表计数完成；
- 真实 NAS end-to-end 报告保存 transfer IDs、manifest hashes、route 与清理结果；
- 新二进制替换、daemon 重启、API/MCP/UI runtime 检查通过；
- 旧 Dataset/Freeze/Run 浏览与已有 Run 状态同步无回归；
- 没有自动删除用户数据，没有复制 Mac 私钥，没有把 transport smoke 宣称为实验结果。

## 16. 成功指标

本版本成功不是以新增页面或命令数量衡量，而以以下结果衡量：

- Agent 用一个逻辑 URI 识别同一文件树；
- Agent 能准确回答数据是否存在、在哪、何时检查、是否匹配 revision；
- Dataset、Freeze 和 Run I/O 不再包含独立 rsync 实现；
- 所有受管理运输可恢复、可验证、可追溯；
- NAS 是默认 durable placement，Mac 不承担大 payload；
- formal Run 的输入数据身份和输出归档形成闭环；
- UI 默认心智模型从“多个孤立文件位置”变成“一个路径、多个 Placement”。
