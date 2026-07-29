# 统一逻辑文件空间与 NAS 数据流验收记录（2026-07-16）

本记录对应 [PRD：aexp 统一逻辑文件空间与跨 Resource 数据流](./prd-unified-file-space.md)。所有远端操作均为 **transport smoke**，不是实验结果，也不能作为论文证据。

## 发布结论

- 统一 `LogicalPath → Placement → TransferPlan → TransferJob` 已部署到本机 aexp。
- Dataset materialize、Run input/output、Freeze raw/workspace transport 均复用 TransferJob；Freeze、Dataset 与 Run I/O 不再私自拼接 rsync。
- NAS 是默认权威副本；NAS 与 RTX6000 之间的 payload 路径不经过 Mac。
- UI-v2 深链刷新、Data Center、NAS control plane/compute edge 分离和运行时 console 已手工检查。
- 真实数据库已在线备份、迁移、修复历史孤儿外键并完成旧二进制 rollback rehearsal。

论文 Freeze 的 eligible/released 状态机、sidecar、gate、restore 与大 payload 路径由自动测试覆盖。本次真实库没有已登记 DatasetVersion/Freeze，仓库也没有项目级 `paper` profile 和 release hook，因此没有伪造一个“真实论文 freeze”；真实环境只验收其共用的 NAS transport、完整 SHA-256 和 blocker 边界。

## 自动门禁

最终发布前结果：

- `go test ./...`：通过；
- `go vet ./...`：通过；
- `pnpm --dir web test`：17 个测试文件、64 项测试通过；
- `pnpm --dir web build`：TypeScript 与 UI-v2 production build 通过；
- `git diff --check`：通过。

关键新增回归覆盖：

- transferring/verifying/promoting 三阶段恢复；
- worker 心跳租约，fresh worker 不被第二个 manager 抢占；
- 同一 transfer ID 的 initiator-side `flock`，避免孤儿 rsync 并发写同一 staging；
- completed TransferJob 的目的副本被删后重新排队，保留 transfer ID 并追加 attempt；
- Dataset verify/repair/evict、Freeze materialize、通用 ensure/evict；
- Run missing input、output publish、finalization failed 与 lifecycle 分离；
- Placement/Transfer/Run finalization 的 UI-v2 状态矩阵；
- 历史 Resource 被删除时隐藏 tombstone 保留外键，且不进入监控/UI 活动资源列表。

## 真实 NAS 端到端

第一组专用 namespace：

```text
NAS       /vol1/1000/aexp-transport-smoke/20260716-0101
RTX6000   /home/szumfy/aexp-transport-smoke/20260716-0101
```

| 场景 | Transfer | Initiator | Mac payload | Revision |
|---|---|---|---|---|
| Mac → NAS 小文件 | `transfer_95d01860ed3f` | mac | true（本地发布，预期） | `sha256:881faf…` |
| NAS → RTX6000 | `transfer_948b694d3d38` | nas | false | 32 bytes，目的端复算通过 |
| RTX6000 → NAS | `transfer_6f4850860175` | nas | false | `sha256:97bc5dfe…` |
| 513 MiB 中断恢复（初次暴露竞态） | `transfer_bf38cafda41c` | nas | false | `sha256:a3e2acbb469e4e59dde406f912e754c933c1ac0fb0092a3634d61d5073309c0c` |
| 513 MiB 加锁后重新验收 | `transfer_1b5e3b7aa6cb` | nas | false | 同上 |

大文件精确大小为 `537,919,488` bytes。加锁重跑中，CLI worker 被中断后：

- surviving initiator-side rsync 继续写 job 专属 staging；
- reconciler 等待 heartbeat lease 过期后恢复；
- 中断 attempt 被记为 `worker_interrupted`；
- 新 attempt 完成 verify/promotion；
- RTX6000 源与 NAS final 均由目的端 `sha256sum` 得到同一完整 SHA-256；
- `local_data_path=false`。

清理：NAS 与 RTX6000 上上述两个精确 namespace 均已删除并用 `test ! -e` 验证。清理事件：`exec_pti1PNYS8neQ`、`exec_TMsGTxkJao2e`。

## 真实 Run input/output 闭环

第二组专用 namespace：

```text
NAS       /vol1/1000/aexp-runio-smoke/20260716-0125
RTX6000   /home/szumfy/aexp-runio-smoke/20260716-0125
```

输入/输出 revision：

```text
sha256:f389884c889a8a1badbdfb1c8c98d0c488a968cf7f4a4025e71f7abed16339f3
```

最终 transport-smoke Run：`run_bOTJXJjCglEc`。

- 手工删除 compute input cache 后，aexp 重新探测到 missing；
- 历史 `transfer_3dc7ae3efc2e` 从 completed 重新排队，新增第 3 个 completed attempt；
- binding 在目的端 SHA-256 验证后才变为 `ready`，随后才 launch；
- Run 最终 `succeeded`，状态来源为 `remote_exit_code`；
- output 由 `transfer_2a13ce237995` 发布到 NAS；
- data finalization 为 `completed`；
- final RunManifest schema v2 hash 为 `sha256:5518a0adbe506f8de90c691dea59a31091b49d0d10d239de5d4e04ee04b6c1e1`；
- compute 源文件在 publish 后仍保留，直到受控 scratch cleanup。

这次验收先真实复现并修复了一个关键错误：控制面曾复用 completed job，把已经被删的 cache 错标为 Ready。现规则是 completed 只在当前目的 revision 仍匹配时复用；missing 时必须重新排队并追加 attempt。

NAS/RTX6000 两端第二组精确 namespace 均已删除并验证不存在。清理事件：`exec_bebYBtDaZvVa`、`exec_eG7qP9gh43Wt`。逻辑 root 仅存在于隔离验收数据库；因其 Placement ledger 被保留作验收证据，metadata 删除按设计被拒绝，未进入真实数据库。

## 数据库迁移与 rollback

迁移副本来自真实 `~/.aexp/aexp.db` 的 SQLite `.backup`。

迁移前业务计数：

```text
resources 6
runs 468
artifacts 46
dataset_versions 0
run_freezes 0
```

迁移发现三个早已存在、并非本次 schema 引入的外键孤儿：两个 `resource_snapshots` 和一个历史 failed Run 引用了已删除 Resource。迁移现在插入两个 inert/hidden Resource tombstone：

- raw resources 从 6 变为 8；
- active resources 保持 6；
- runs/artifacts/datasets/freezes 等业务计数不变；
- 新的 LogicalPath/Placement/Transfer/Run binding 表初始均为空；
- `PRAGMA integrity_check=ok`；
- `PRAGMA foreign_key_check` 无返回行；
- 重复打开后 tombstone 数仍为 2。

真实部署备份：

```text
/Users/ziwu/.aexp/backups/unified-filespace-20260716-013930/aexp.db
/Users/ziwu/.aexp/backups/unified-filespace-20260716-013930/aexp.binary
```

备份 SHA-256：

```text
DB          83dc56aa82989fd884b0e9de80c41770fd5a9a92bb3aeae66d067a2a8b74b8c5
旧二进制    b89eb2c8566ecea26f91583160132fe3491126ba21f2ec616846ef88e3ed8742
新二进制    c7a5748919c80101a1241667d979d02d2b7cfa714c644d4ac0e3b06724659cea
```

Rollback rehearsal 使用备份 DB 的 clone 和旧二进制启动只读列表：6 个活动 Resource、468 个 Run，`integrity_check=ok`。

## 部署后运行时检查

- 安装路径：`/Users/ziwu/.local/bin/aexp`；
- launchd submitted job：`gui/501/com.ziwu.aexp`；
- API：`/api/v1/health` 返回 `status=ok`；
- `/api/v1/logical-roots`、`/api/v1/transfers` 返回 JSON，不再被旧后端误当 HTML；
- `/ui-v2/` 返回 200；
- 历史 468 个 Run、46 个 Artifact 可见；
- 真实库新表为空，符合首次上线预期；
- 真实库迁移后 active resources=6、tombstones=2、`integrity_check=ok`、`foreign_key_check` 为空；
- UI-v2 概览和 Data Center 正常渲染，`/ui-v2/data-center` 深链刷新仍有内容；
- 浏览器 runtime console error 为 0；
- Data Center 明确显示 NAS control plane healthy、1/4 compute edge 可用于同步/冻结，而不是把 NAS 本身误报故障。

## v1.1 产品表面封装复验

根据实际使用反馈，统一文件空间继续保留为内部底座，Data Center 默认心智模型收敛为
“主存储位置 → 冻结的数据版本 → 后台传输”；LogicalRoot、Placement 和 initiator/route
移动到关闭的高级详情。

- 高级详情关闭时不请求或轮询 LogicalRoot/Placement；
- 默认文案不再使用“权威 NAS/权威副本”，改为“主存储位置/主存储副本”；
- `storage://ziwudenas/` 作为 Agent 可见位置，物理 SSH 路径仍保留用于诊断；
- 传输卡默认只显示状态、进度和 source→destination，内部 job/stage/initiator 放在技术详情；
- UI 18 个测试文件、66 个测试通过，TypeScript 检查和生产构建通过；
- `go test ./...`、`go vet ./...`、`git diff --check` 通过；
- `/ui-v2/data-center` 深链刷新后的可见顺序为 NAS 存储节点、冻结的数据版本、后台传输、
  高级路径映射；高级详情可正常开合，页面无水平溢出，浏览器 console error 为 0；
- 最终部署二进制 SHA-256：
  `68c0584246c7a967ded0eb4ae5d4d6dc060d9eab9628625965e0c2db7e18a4ae`；
- 替换前二进制备份：
  `/Users/ziwu/.aexp/backups/ui-encapsulation-20260716-171807/aexp.binary`；
- launchd 重启后 `/api/v1/health` 返回 `status=ok`。

本次只验证控制面与数据运输 UI，没有执行实验；任何 smoke 均不构成实验结果。

## v1.2 Agent Storage Facade 验收

在不新增存储模型的前提下，新增四个默认 Agent 工具：

```text
aexp_storage_stat
aexp_storage_list
aexp_storage_locations
aexp_storage_copy
```

- `storage://`、`resource://` 和 `aexp://` 共用 RemoteFS 的边界检查与只读观察；
- `storage://ziwudenas/` 可直接 stat、分页 ls 和 locations，无需 LogicalRoot；
- 默认列表隐藏点文件和内部 staging 名称，低层 `aexp fs ls` 仍可用于高级诊断；
- locations 会包含尚未 inspect 的默认主存储位置，不再把“未登记观察”误报为“没有位置”；
- copy 自动对未经登记的源完整计算 SHA-256，并把 revision 固化进 TransferPlan；
- Agent copy 只执行一次 Build/Hash 后入队，避免同一 HTTP/MCP 请求重复扫描大目录；
- 成功仅返回紧凑 transfer ID、状态、source/destination、revision 和字节数；
- source missing/unreachable/hash unavailable 和 destination revision conflict 返回结构化 blockers；
- 不提供 replace/force，不会覆盖不同内容；staging 和 promotion 继续使用既有完整校验与原子提升。

真实 NAS 的 stat、根目录分页 list 和 locations 已分别通过 CLI 与 REST 只读验证；列表未暴露
点目录或 `.incoming-*` staging。MCP `tools/list` 也返回上述四个类型化工具。

Agent copy 使用以下专用 transport-smoke 命名空间验收：

```text
source       storage://ziwudenas/aexp-transport-smoke/facade-20260716-195701
destination  resource://szumfy-rtx6000/aexp-storage-facade-smoke/facade-20260716-195701
revision     sha256:2fc87d91abab4c373779c95167948ac21191b7c2452e4c01fee418a92eadc56c
payload      59 bytes / 1 file
transfer     transfer_3fdc00e17003
```

第一次 attempt 真实暴露了 Synology ACL 权限映射问题：`rsync -a` 把 NAS 目录的合成 `000`
mode 应用到 Linux staging，随后无法写入子文件。传输被正确记为 `failed/rsync_failed`，没有
误报成功。修复后，copy 会在重试前恢复 staging 为 `0700`，并在 rsync 中归一化 owner、group
和 ACL-derived mode；同一 transfer 的第 2 个 attempt 完成 SHA-256 校验与原子提升，
`local_data_path=false`。对已存在相同 revision 的目标重复 copy 返回 completed no-op，随后请求
复用同一 completed transfer。

源、目标以及第一次失败遗留的两个精确 staging 均已删除；CLI stat 对源和目标都返回
`state=missing`。Transfer/attempt ledger 保留为失败恢复证据。该流程只是数据运输冒烟，不是实验，
也不能作为论文证据。

最终部署：

```text
binary  /Users/ziwu/.local/bin/aexp
sha256  f1afe1bc585fcdfd5035b7258f4058d2dd8d3890e90c216946eae5467f69975f
backup  /Users/ziwu/.aexp/backups/storage-facade-20260716-200053/aexp.binary
```

launchd `gui/501/com.ziwu.aexp` 重启后 health 为 `ok`，三个只读 REST facade 端点均返回 JSON。

## 2026-07-20 ProxyCommand 子进程回收修复

长期运行的 `aexp serve` 曾累计 3358 个僵尸进程。旧 PID 8216 被 launchd 重启后现场降至
18 个全机僵尸，但新 serve PID 89103 在无运行中/启动中实验时仍从 0 增至 8 个直属僵尸，
证明重启只清理现场，没有修复根因。

根因链为：后台 Resource monitor 周期探测使用 ProxyCommand 的计算资源；
`cmdConn.Close()` 关闭管道并 `Process.Kill()` 外部 `nc`，但从不调用 `cmd.Wait()`。每次握手失败
或连接关闭都会在长期运行的 serve 下留下一个进程表项。修复将关闭集中为幂等的
`close pipes → Kill → Wait`，并接受被终止进程的正常 `ExitError`。

回归与运行时验收：

- `TestCmdConnCloseReapsProxyCommand` 在修复前稳定失败，修复后连续运行 20 次通过；
- `go test ./...`、`go vet ./...`、`git diff --check` 通过；
- launchd 新 PID 25621 启动后跨过多个 30 秒 monitor 周期，直属 zombie 始终为 0；
- 保留两个不可达 ProxyCommand Resource，主动触发 20 次真实失败握手；
- `launchctl forks` 从初始值增长到 26，但 `PPID=25621` 的 zombie 数仍为 0；
- `/api/v1/health` 返回 `status=ok`，没有运行实验用于本次验证。

部署产物：

```text
binary  /Users/ziwu/.local/bin/aexp
sha256  6728fb55899fabc9aab94f45760488426cb0696556c0c54872026346d9236a6f
backup  /Users/ziwu/.aexp/backups/zombie-reap-20260720-164355/aexp.binary
```
