# mod-web — Web 仪表盘

## 设计原则

- 旧版 `/` 保留为单 HTML、无构建步骤、原生 JavaScript，作为稳定回退入口。
- 新版 `/ui-v2` 使用 React + TypeScript + Vite，构建产物仍编译进 Go 二进制（embed.FS）。
- 新版优先面向大数据量控制台：服务端分页、虚拟表格、请求缓存、WebSocket 增量日志、Worker 解析 UI events。
- 视觉风格保持安静、密集、操作型，不做营销式 landing page。

## 页面

### 1. 仪表盘（/）

```
┌────────────────────────────────────────────────────────────┐
│  aexp                                              [?]     │
├────────────────────────────────────────────────────────────┤
│                                                             │
│  Resources                      Active Runs                 │
│  ┌─────────────────────┐      ┌─────────────────────────┐  │
│  │ mu-tslib       [idle]│      │ run_Yn7p  ECL-iTrans... │  │
│  │ GPU0: 12%  2.1/24GB │      │ mu-tslib  running       │  │
│  │ CPU: 23%  Mem: 45%  │      │ 12:34 elapsed            │  │
│  │                     │      │ [logs] [cancel]           │  │
│  │ szu-exp       [busy]│      ├─────────────────────────┤  │
│  │ GPU0: 89%  18/24GB  │      │ run_Km3q  Weather-Trans..│  │
│  │ CPU: 78%  Mem: 67%  │      │ szu-exp  running         │  │
│  │                     │      │ 05:21 elapsed             │  │
│  └─────────────────────┘      └─────────────────────────┘  │
│                                                             │
│  Recent Runs                                                │
│  ┌──────────┬──────────┬─────────┬──────────┬──────────┐   │
│  │ Run ID   │ Name     │Resource │ Status   │ Duration │   │
│  ├──────────┼──────────┼─────────┼──────────┼──────────┤   │
│  │ run_Yn7p │ ECL-iTr..│ mu-tslib│ ●running │ 12:34    │   │
│  │ run_Km3q │ Weather..│ szu-exp │ ●running │ 05:21    │   │
│  │ run_Px2w │ ILI-36.. │ mu-tslib│ ✓done    │ 45:12    │   │
│  └──────────┴──────────┴─────────┴──────────┴──────────┘   │
└────────────────────────────────────────────────────────────┘
```

### 2. Run 详情（/runs/{id}）

```
┌────────────────────────────────────────────────────────────┐
│  ← Back    Run run_Yn7pL2wE                                │
├────────────────────────────────────────────────────────────┤
│                                                             │
│  ECL-iTransformer-run1                                      │
│  Resource: mu-tslib  |  Status: ●running                   │
│  Command: python train.py --data ECL --model iTransformer   │
│  Started: 2026-06-07 14:30:00  |  Elapsed: 12:34           │
│  tmux: aexp_run_Yn7pL2wE                                   │
│                                                             │
│  ┌─ Resource ─────────────────────────────────────────────┐ │
│  │  GPU0: ████████░░░░░░░░░░░░  45%  8.0/24.0 GB        │ │
│  │  CPU:  ██████░░░░░░░░░░░░░░  23%                      │ │
│  │  MEM:  █████████░░░░░░░░░░░  45%  28.8/64.0 GB       │ │
│  └────────────────────────────────────────────────────────┘ │
│                                                             │
│  ┌─ Logs ─────────────────────────────────────────────────┐ │
│  │ [stdout] Epoch 1/100                                     │ │
│  │ [stdout]   train_loss=0.4521  val_loss=0.3892          │ │
│  │ [stdout] Epoch 2/100                                     │ │
│  │ [stdout]   train_loss=0.3211  val_loss=0.2987          │ │
│  │ [stdout] Epoch 3/100                                     │ │
│  │ [stdout]   train_loss=0.0234  val_loss=0.0198          │ │
│  │ ▊                                                       │ │
│  └────────────────────────────────────────────────────────┘ │
│                                                             │
│  [Cancel Run]                                               │
└────────────────────────────────────────────────────────────┘
```

### 3. 资源详情（/resources/{id}）

资源历史图表 + 该资源的 run 历史。

## WebSocket 集成

Run 详情页：
1. 打开 WebSocket `/ws/runs/{id}/logs`
2. 实时追加日志行
3. 自动滚到底部（用户上滚暂停）
4. LIVE 指示灯，点击跳回底部

仪表盘：
1. 打开 WebSocket `/ws/resources/{id}/metrics`
2. 实时更新资源条
3. 更新资源状态标签

## 资源可视化

纯 CSS 进度条（MVP 不引入图表库）：

```html
<div class="resource-bar">
  <div class="resource-bar-fill" style="width: 45%">45%</div>
</div>
```

颜色：绿（0-60%）→ 黄（60-80%）→ 红（80-100%）

## 状态标签

```css
.status-running   { color: #22c55e; }  /* 绿点 */
.status-succeeded { color: #3b82f6; }  /* 蓝勾 */
.status-failed    { color: #ef4444; }  /* 红叉 */
.status-cancelled { color: #6b7280; }  /* 灰横线 */
.status-idle      { color: #22c55e; }
.status-busy      { color: #f59e0b; }
.status-error     { color: #ef4444; }
.status-unreachable { color: #ef4444; }
```

## 技术细节

- 旧版 API 调用：`fetch()`；新版 API 调用：TanStack Query + fetch wrapper。
- WebSocket：原生 `WebSocket` API，详情页先 HTTP 拉快照，再 WS 追加增量行。
- 表格：TanStack Table + TanStack Virtual。
- 图表：ECharts。
- 状态：Zustand 保存 token、语言、对比选择。
- 自动刷新：列表 5-12s 轮询，日志/资源详情以 WebSocket 为主。

## 构建

```bash
pnpm --dir web install
pnpm --dir web typecheck
pnpm --dir web test
pnpm --dir web build
go test ./...
go build -o aexp ./cmd/aexp
```

Vite `base` 固定为 `/ui-v2/`，输出目录是：

```text
internal/api/static/ui-v2
```

因此本地二进制和容器镜像都会同时携带 `/` 旧 UI 与 `/ui-v2` 新 UI。
