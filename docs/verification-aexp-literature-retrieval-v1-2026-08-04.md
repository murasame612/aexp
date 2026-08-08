# ResearchOS / aexp 文献检索 v1 交付验收 — 2026-08-04

## 结论

Project → PaperQA2 → Zotero → Journal 的薄集成已部署。aexp 只保存 Project
绑定和 Journal 引用，不保存 PDF、chunk、embedding 或索引；文献响应固定标记为
`evidence_domain=literature` 与 `claim_scope=background_only`，不能替代实验 provenance。

## 现役状态

- Project：`dam-displacement-imputation`
- Zotero collection：`SHUMTSPS`（`damxer投稿`，递归 85 篇）
- Service profile：`mu-paperqa`
- Corpus revision：`corpus_6d74f2ef2d75c06be154`
- Manifest：`sha256:6d74f2ef2d75c06be154dfd9746c2c92df9ee561fd3149b8a44809eab15d6e23`
- PaperQA2：`2026.3.18`，85 documents / 3650 chunks，hybrid index
- NAS mirror：`storage://ziwudenas/researchos/literature/corpora/corpus_6d74f2ef2d75c06be154`
- LightRAG：默认入口关闭；显式请求返回 `LIGHTRAG_NOT_ENABLED`

## 真实端到端查询

命令：

```bash
aexp literature query dam-displacement-imputation \
  --query 'Which published mechanisms are plausible to transfer into dam displacement imputation, and what evidence limitations should be preserved?' \
  --evidence-k 8 --answer-max-sources 5 --json
```

结果为 `partial`，返回 8 条冻结引用，没有 fallback。首条来源为 Zotero item
`J7WPVH54`、page 1，chunk SHA-256
`2d8437dc51575fdbbd44ddf16b2dcdfbb7a28b450b836eb25c080bb6f9b93742`。
该分类是预期行为：检索命中和生成答案存在，但 PaperQA 没有给出明确成功信号，系统不把它升级为
`supported`。

## 数据库与服务替换

- 迁移前先使用 SQLite `.backup` 演练；48 张既有业务表计数一致，
  `integrity_check=ok`。
- 生产备份：`~/.aexp/aexp.db.backup-literature-20260804-034238`。
- 旧二进制：`~/.local/bin/aexp.rollback-literature-20260804-034238`。
- 重启后 4 Projects、38 Journal entries、719 Runs、16 Evidence Maps、145 nodes、
  145 edges 保持不变；后台 resource snapshots 正常继续增长。
- `/api/v1/health`、`/ui-v2/` 与新 hashed JS asset 均返回 HTTP 200。

## 自动验证

- `go test ./...`
- UI：26 test files / 151 tests passed，production build passed
- Python：17 PaperQA/gateway contract tests passed
- mismatched collection：status 返回 `LITERATURE_COLLECTION_MISMATCH`，query 非零退出
- NAS：上传到稳定 incoming 目录，按 manifest 重算两个 payload SHA-256，随后原子提升并只读化

## 保留项

尚未删除现役 mu PaperQA 索引再做灾难恢复。语料 revision 已在 NAS 完整镜像且哈希一致，
但在线索引删除/重建应作为独立维护窗口中的恢复演练，不在本次交付中破坏现役服务。

## Project 文献工作台增量验收（18:10）

- ResearchOS Project 顶部新增“文献”页，真实读取 Zotero 25 个递归 collection 路径；
- `dam-displacement-imputation` 自动识别 `mu-paperqa` 为 ready，显示 85 documents / 3650 chunks；
- 真实 HTTP 查询返回 `partial`、同一 corpus revision、5 条逐页引用，首条为 item
  `822TAAV9`、page 2、chunk `8a23be9f2954cd08891ec2acde48e5eb0cb8ca9ca2892d39e820a98c05855d5f`；
- 默认 MCP 只提供 status/query；人类在 UI 选择文献文件夹，catalog/bind 仅保留在 advanced 维护配置；
- Go 全量测试通过；UI 27 test files / 152 tests passed，production build passed；
- 生产备份目录：`~/.aexp/backups/literature-workbench-20260804-181030`；
- 新二进制 SHA-256：`6874c5fa04824619b37c663f3a6964758de4e2377a420149c0067eb22a70eb88`；
- 重启后数据库 `integrity_check=ok`，无 running/queued 实验。

## 父文件夹多索引与 UI 查询验收（19:54）

- 根因：`arch-dam-forcasting` 绑定 Zotero 父文件夹 `EG7L6KJZ`（大坝位移预测），
  但此前唯一在线索引属于子文件夹 `SHUMTSPS`（damxer投稿）；文件夹已选中不等于已有可检索投影。
- 为父文件夹递归发布 `corpus_c5fb0e0685d4ec75fbe3`：91 documents / 3992 chunks，
  manifest `c5fb0e0685d4ec75fbe31ca6ce13fca07ec8c4a42d48fb56f2f91cffbfd7d539`。
- Gateway 与 aexp profile 改为按 Project 固定 corpus revision；父索引和子索引可同时查询，
  不再共享一个会互相覆盖的 active revision。
- `arch-dam-forcasting` 已绑定 `mu-paperqa-arch-dam`；
  `dam-displacement-imputation` 继续绑定 `mu-paperqa`，两者 status 均为 `ready`。
- 真实 CLI 查询返回 `partial` 和 8 条逐页证据；真实 UI 查询返回答案与 10 条 Zotero
  条目/页码证据。UI/API 查询 deadline 从 90 秒改为 5 分钟，覆盖 PaperQA2 的实际合成延迟。
- UI 不再把“只有 collection、没有 index profile”显示成绿色“已关联”；新索引出现后会自动选择
  对应 profile，仍由用户保存绑定。
- UI 27 test files / 155 tests passed，production build passed；相关 Go 测试通过；数据库
  `integrity_check=ok`。新二进制 SHA-256：
  `fd91338b92bae4c2fdf80a209b32c623b6641ed85c23faac9fc69213180dd317`。
- 替换控制面时有一个远端 pilot 正在运行；launchd 重启只短暂停止本机 API，未终止远端 tmux Run。
