# aexp 系统精简验收记录

日期：2026-07-24  
依据：`docs/prd-system-simplification.md`

## 结论

本轮精简已通过代码、迁移副本和本机生产服务三层验收。默认研究工作流现在收敛为：

```text
Project → Asset revision → Run → Snapshot → Release → Evidence proposal
```

传输、放置、Storage、Freeze 和底层资源操作仍保留为兼容/管理能力，但不再出现在默认 CLI、MCP 和 UI-v2 主流程中。

## 用户侧验收

- UI-v2 主导航只保留 Project、运行中实验和设置。
- 进入 Project 后固定显示概览、实验、数据与产物、Evidence 四个项目内入口。
- Project 实验页由服务端先按项目和类型过滤，再分页；每页 20 条。
- `dam-displacement-imputation` 实测为 86 条、5 页，首屏恰好 20 张卡片。
- Project 内不再重复显示项目筛选器和每张 Run 上的项目胶囊。
- Project Evidence 只打开该 Project 的唯一 primary Evidence Map，不显示全局白板列表。
- 从侧栏点击“运行中实验”会明确退出 Project 范围。
- Project Assets 只展示已经校验的输入 revision 和发布输出；路径仅用于定位，不作为身份。
- 未被历史 Run binding 引用、但 canonical `aexp://<project>/...` URI 已归属 Project 的 verified DatasetVersion 也会显示，避免 Agent 把“未绑定”误解为“数据不存在”。
- Run Detail 以 Snapshot 为主入口，旧 Freeze 收入兼容区。

## Agent / CLI 验收

- 根命令帮助只展示 Project、Asset、Run、Snapshot、Release、Evidence 等研究工作流。
- `dataset`、`storage`、`sync`、`transfer`、`freeze` 等旧命令仍可按原名调用，但已从默认帮助隐藏。
- MCP `tools/list` 默认仅暴露研究工作流和只读诊断工具；低层传输与通用 CLI 工具仍保留兼容调用。
- Formal Run 必须绑定 canonical Project 和 verified provenance。
- Evidence proposal 未指定 map 时，由 Run 的 `project_id` 自动解析到 primary Evidence Map。

## 自动化验证

- `go test ./...`：通过。
- UI-v2 `pnpm typecheck`：通过。
- UI-v2 Vitest：21 个测试文件、77 个测试用例全部通过。
- UI-v2 production build：通过。
- ECharts/MetricChart 和 CompareModal 已拆为按需加载 chunk，Project/Run 首屏不会下载 1.1 MB 图表 chunk。

## 迁移副本验收

迁移前核心计数：

| 表 | 数量 |
| --- | ---: |
| runs | 511 |
| dataset_versions | 1 |
| artifacts | 366 |
| run_freezes | 0 |
| project_run_cards | 153 |
| evidence_chains | 6 |
| evidence_chain_nodes | 59 |
| evidence_chain_edges | 59 |

迁移并重复启动后：

| 表 | 数量 |
| --- | ---: |
| runs | 511 |
| dataset_versions | 1 |
| artifacts | 366 |
| run_freezes | 0 |
| project_run_cards | 153 |
| evidence_chains | 9 |
| evidence_chain_nodes | 59 |
| evidence_chain_edges | 59 |
| project_definitions | 4 |
| evidence_snapshots | 0 |
| evidence_releases | 0 |

- 四个 legacy Project 被无损导入为 canonical Project。
- 每个 canonical Project 自动得到一个 primary Evidence Map。
- 只有单一、无歧义 Project Card 归属的历史 Run 才回填 `project_id`。
- 重复迁移计数不再变化，`PRAGMA integrity_check=ok`。
- 关键 Project、Run、Assets、Evidence API 连续 5 轮、20 次请求全部返回 HTTP 200。

## 生产替换验收

- 新二进制 SHA-256：`b1cca022ac7bcb0df31da92637d8e94c71a8973edb2a7e9b184233bf258d2139`
- launchd 服务：`com.ziwu.aexp`
- 替换前 PID：`29251`
- 最终替换后 PID：`38415`
- 监听：`127.0.0.1:8080`
- 生产数据库迁移后：517 Runs、1 DatasetVersion、366 Artifacts、9 Evidence Maps、4 Projects。
- 核心历史计数未减少，`PRAGMA integrity_check=ok`。
- 替换时两条远端 pilot 已进入 `remote_tmux`；重启后均继续显示 `running`，且检查时间持续更新。
- `multimodal-defect-detection` 的 verified DatasetVersion 已在 Project Assets 返回 1 条 canonical dataset revision。
- aexp 服务自身无僵尸子进程；系统剩余两个僵尸分别属于 FlClash 和 sshd-session。
- NAS `ziwudenas` control plane 为 `healthy`，可用空间约 3.28 TB；当前 1/5 条计算节点数据路径健康，该限制已按路径单独展示，不再把 NAS 本体误报为故障。

生产备份与回滚：

- 数据库备份：`/Users/ziwu/.aexp/backups/aexp-before-system-simplification-final-20260724-190012.db`
- 旧二进制：`/Users/ziwu/.local/bin/aexp.rollback-system-simplification-20260724-190012`

## 已知非阻塞项

- 主 UI bundle 仍约 729 KB，Vite 会给出 chunk-size warning；图表大包已经延迟加载，后续可继续按页面拆分。
- 旧浏览器标签页在刷新前仍可能运行旧 JS，并继续发起旧的 100 条列表请求；新页面已使用 20 条服务端分页和可见 Run mark 批量查询。
- NAS 到四个旧计算资源的数据路径仍不可达；`szumfy-rtx6000-naswg` 路径健康。该问题不影响 NAS control plane，也不属于本轮概念精简迁移。
