# Evidence Map 历史数据修复记录

日期：2026-07-27

状态：Production verified

## 目标

处理完整性收口审计发现的 9 张历史 Map，同时遵守以下边界：

- 不修改 Run 指标、状态、产物或实验结论；
- 不补造 dataset、seed、Git、protocol 或 EvidenceSnapshot provenance；
- 语义修改必须通过 proposal、plan 和新 revision；
- 修复前保留数据库和逐图 JSON；
- 无法确定 Project 的归档图不猜测归属。

## 修复前备份

```text
/Users/ziwu/.aexp/backups/aexp-before-evidence-repair-20260727-011903.db
SHA-256 2f1bd481f16ae10896ea1eab29076bd5296169924ef1c60b83094e01954bcd38
```

逐图 detail、audit 和 revision 导出：

```text
.tmp/evidence-repair-20260727-011903/maps/
```

## 执行内容

### Project 归属补录

以下历史 Run 的 `project_id` 原为空，但名称与 `cwd` 明确指向唯一已注册 Project，因此只补录
Project 归属：

- `multimodal-defect-detection`
  - `run_8WMxaNpWieCW`
  - `run_y8naTkvhjRES`
  - `run_zzmMov535Uig`
  - `run_FAH9NoepPC0P`
  - `run_YSPnVQlgD6h1`
  - `run_zwtvkmCrNos0`
- `dam-displacement-imputation`
  - `run_g5c2AglVS5te`
  - `run_EfR9z56hJKh4`

没有为这些 Run 补造任何 provenance。

### Proposal 与 revision

| Map | Proposal | 处理 |
| --- | --- | --- |
| `chain_703lim1kisvd` | `proposal_01bf7782d4409b43f175` | canonical hash 重封；节点和关系不变 |
| `chain_plw7ds5mfh1l` | `proposal_3b23515a5f687ea3e316` | current good810 Topic canonical hash 重封 |
| `chain_l0321ol7jvza` | `proposal_08bd1d415cea3dae0a12` | 合并重复 Run；移除跨 Project Run；旧时间线边降级为 `related_to` |
| `chain_primary_2eb6b708f2e90e46` | `proposal_0874343234b7d1853919` | 初始化空 Primary 的 canonical hash，不添加研究内容 |
| `chain_09tuet9672p2` | `proposal_070ea50fb1b7a90e766b` | 重封 Primary，并更新 current good810 Map Reference 的 revision/hash |

每个 proposal 均先执行无副作用 plan，确认 `eligible=true` 后再由
`integrity-repair` 接受。旧 revision 保留。

### 归档和删除

- `chain_60nayjho6gqy` 已归档。
  - 它属于旧 area90/去裂缝协议；
  - 相关 Run 缺 dataset、seed、Git、config hash、split/eval protocol 和 released
    EvidenceSnapshot；
  - 没有伪造这些字段，也没有继续让它承担正式 claim。
- `chain_bpdua3is9ucb`、`chain_ewmxqbza2f7w` 已永久删除。
  - 两者均为 revision 0、无节点、无边、无 proposal、无 revision、无 Map Reference 的空 Topic。
- `chain_mclum6vrfdd2` 保持归档。
  - 它没有 Project，且只是旧版通用占位图；
  - 不为重算 hash 临时激活，也不猜测 Project。

## 验证

修复后：

```text
active Evidence Maps: 9
eligible: 9
blocked: 0
PRAGMA integrity_check: ok
PRAGMA foreign_key_check: no rows
```

核心计数：

| 表 | 行数 |
| --- | ---: |
| `runs` | 548 |
| `project_run_cards` | 161 |
| `evidence_chains` | 12 |
| `evidence_chain_nodes` | 74 |
| `evidence_chain_edges` | 68 |
| `evidence_chain_revisions` | 51 |
| `evidence_proposals` | 12 |

Map 从 14 减为 12、节点从 76 减为 74、边从 70 减为 68，全部来自两个空 Topic
删除以及 `chain_l0321ol7jvza` 中两个错误节点和关联边的规范化；旧 revision 和修复前备份可
恢复完整历史。

修复前后的两个活跃 Run ID 相同，状态均保持 `running`：

- `run_Gf2OUOEpFATC`
- `run_9B9b7CFdB7Yx`

修复后数据库备份：

```text
/Users/ziwu/.aexp/backups/aexp-after-evidence-repair-20260727-012332.db
SHA-256 edd96a0a19eee039a86e313f6dc3ba1313532edb9cb6de948e48749603c17b4b
```

