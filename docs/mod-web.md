# mod-web — Web Dashboard

## Design Philosophy

- Single HTML file, no build step
- Tailwind CSS via CDN
- Vanilla JavaScript (no framework)
- Embedded in Go binary via `embed.FS`
- Dark theme (developer/researcher friendly)

## Pages

### 1. Dashboard (/)

Overview of all containers and active runs.

```
┌────────────────────────────────────────────────────────────┐
│  aexp                                            [dark/light] │
├────────────────────────────────────────────────────────────┤
│                                                             │
│  Containers                    Active Runs                  │
│  ┌─────────────────────┐      ┌─────────────────────────┐  │
│  │ dam-tslib-0    [idle]│      │ r_Yn7p  ECL-iTrans...  │  │
│  │ GPU0: 12%  2.1/24GB │      │ dam-tslib-0  running    │  │
│  │ CPU: 23%  Mem: 45%  │      │ 12:34 elapsed           │  │
│  │                     │      │ [logs] [cancel]          │  │
│  │ szu-exp-0    [busy] │      ├─────────────────────────┤  │
│  │ GPU0: 89%  18/24GB  │      │ r_Km3q  Weather-Trans.. │  │
│  │ CPU: 78%  Mem: 67%  │      │ szu-exp-0  running      │  │
│  │                     │      │ 05:21 elapsed            │  │
│  └─────────────────────┘      └─────────────────────────┘  │
│                                                             │
│  Recent Runs (table)                                        │
│  ┌──────────┬──────────┬─────────┬──────────┬──────────┐   │
│  │ Run ID   │ Name     │Container│ Status   │ Duration │   │
│  ├──────────┼──────────┼─────────┼──────────┼──────────┤   │
│  │ r_Yn7p   │ ECL-iTr..│ tslib-0 │ ●running │ 12:34    │   │
│  │ r_Km3q   │ Weather..│ szu-exp │ ●running │ 05:21    │   │
│  │ r_Px2w   │ ILI-36..│ tslib-0 │ ✓done    │ 45:12    │   │
│  └──────────┴──────────┴─────────┴──────────┴──────────┘   │
└────────────────────────────────────────────────────────────┘
```

### 2. Run Detail (/runs/{id})

```
┌────────────────────────────────────────────────────────────┐
│  ← Back    Run r_Yn7pL2wE                                  │
├────────────────────────────────────────────────────────────┤
│                                                             │
│  ECL-iTransformer-run1                                      │
│  Container: dam-tslib-0  |  Status: ●running               │
│  Command: python train.py --data ECL --model iTransformer   │
│  Started: 2026-06-07 14:30:00  |  Elapsed: 12:34           │
│  PID: 12345  |  tmux: aexp_r_Yn7pL2wE                      │
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

### 3. Container Detail (/containers/{id})

Resource history charts + run history for that container.

## WebSocket Integration

On the run detail page:
1. Open WebSocket to `/ws/runs/{id}/logs`
2. Append new lines to log pane
3. Auto-scroll to bottom (user can scroll up to pause)
4. Show "LIVE" indicator, click to jump to bottom

On the dashboard:
1. Open WebSocket to `/ws/resources`
2. Update resource gauges in real-time
3. Update container status badges

## Resource Visualization

Simple CSS bars (no chart library for MVP):

```html
<div class="resource-bar">
  <div class="resource-bar-fill" style="width: 45%">45%</div>
</div>
```

Color coding:
- Green: 0-60%
- Yellow: 60-80%
- Red: 80-100%

For GPU memory: absolute values shown alongside percentage.

## Status Badges

```css
.status-running   { color: #22c55e; }  /* green dot */
.status-succeeded { color: #3b82f6; }  /* blue check */
.status-failed    { color: #ef4444; }  /* red x */
.status-cancelled { color: #6b7280; }  /* gray minus */
.status-pending   { color: #eab308; }  /* yellow clock */
.status-idle      { color: #22c55e; }
.status-busy      { color: #f59e0b; }
.status-error     { color: #ef4444; }
```

## Tech Details

- All API calls via `fetch()`
- WebSocket via native `WebSocket` API
- CSS: Tailwind CDN + minimal custom CSS
- No router library — simple hash routing or just separate HTML sections shown/hidden
- Auto-refresh: WebSocket handles real-time; fallback polling every 5s if WS fails
