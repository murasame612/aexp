# ResearchOS Zotero 开放发现与引用钉 v1 验收标准

依据：[prd-zotero-open-discovery-v1.md](prd-zotero-open-discovery-v1.md)

## A. 边界与兼容性

- [x] 不新增数据库表、Zotero MCP、PDF/embedding 存储或外部检索代理。
- [x] 既有 Project、Run、Journal、Evidence 和 frozen corpus 数据无需迁移。
- [x] 既有 `aexp_literature_status/query` 行为和 PaperQA2 provenance 合同保持兼容。
- [x] LightRAG 仍不进入默认路径。

## B. Agent 可发现性

- [x] Project Research Context 明示 `project_first`、`zotero_live` 可用和 binding 不是检索边界。
- [x] 默认 MCP 文案允许使用现有 Zotero MCP 得到的 live reference。
- [x] aexp skill 指导 Agent 先查 frozen corpus，不足时自由查 Zotero live，正式写入前 pin。
- [x] Agent 不需要知道 Zotero PDF 路径、PaperQA 索引路径或服务 token。

## C. 引用约束

- [x] `frozen_corpus` 缺 revision/hash 时继续拒绝写入。
- [x] `zotero_live` 缺正整数 item/library version 时继续拒绝写入。
- [x] Journal API/MCP 能完整往返两种引用。
- [x] UI 折叠态显示引用数量，展开态区分“冻结语料”和“Zotero 实时”，展示 pin 并可打开 Zotero。
- [x] 文献引用不能满足任何实验 provenance 或发布门禁。

## D. Project 设置

- [x] 已有 Project 可在 ResearchOS 中编辑 Collection key 与 literature service profile。
- [x] UI 明示该 binding 只控制 frozen corpus，不限制 Zotero 全库检索。
- [x] 保存成功后 Project API 往返一致，其他 Project 字段不丢失。
- [x] UI 明示修改 binding 不会自动重建 corpus。

## E. 测试与部署

- [x] Go store/API/MCP 测试通过。
- [x] UI-v2/ResearchOS 类型检查、单元测试和生产构建通过。
- [x] 新前端静态产物嵌入新 aexp 二进制。
- [x] 替换前备份现役二进制；重启后 health、ResearchOS 页面和 Project 数据正常。
- [x] 不把 smoke/连通性检查描述成研究结果。

## 验收记录（2026-08-04）

- `go test ./...`：通过。
- `pnpm -C web test`：27 个测试文件、153 项测试通过。
- `pnpm -C web build`：通过，静态产物写入 `internal/api/static/ui-v2`。
- 现役二进制 SHA-256：`d0c6cfabfd2af668b20a504b8c2315dde940c8b4963a3a6b4b55eccfc8c35ed0`。
- 部署备份：`/Users/ziwu/.aexp/backups/zotero-open-discovery-20260804-170452/`。
- launchd：`com.ziwu.aexp` 重启后为 `running`，PID `22102`；`/api/v1/health` 返回 `status=ok`。
- 数据库：`integrity_check=ok`；Project 4，Journal 38；部署前后计数一致。
- Project Context 实测返回 `discovery_policy=project_first`、`zotero_live_allowed=true`、`binding_limits_search=false`。
- 本节仅记录软件验收与连通性，不构成任何研究结果。
