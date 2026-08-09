# Acceptance — aexp Product Polish v0.3.2

Date: 2026-08-09

## AC-01 — initial bundle

- The common minified JavaScript bundle is no larger than 500 kB raw.
- The metric chart is a lazy chunk and uses the ECharts core registration path.
- Evidence, Journal, Literature, Data Center, Launchpad, Matrices, Settings,
  and Evidence Freeze are not eager imports in the common bundle.
- Hash-named static assets return `Content-Encoding: gzip` when requested.

Observed build:

```text
common JavaScript: 479.35 kB raw / 148.07 kB build gzip
metric chart:      515.04 kB raw / 175.52 kB build gzip
HTTP common asset: 148,903 bytes with gzip
```

## AC-02 — background request volume

- A selected Evidence Map with its Run tray closed makes no
  `evidence-run-candidates` request over at least one 12-second poll interval.
- Opening the tray or a Run preview still enables candidate loading.
- Dashboard requests `exec-events?limit=3`; the execution page retains its
  full page size.
- The database has an index on `exec_events(created_at DESC)`.

Observed isolated responses:

```text
exec events, limit 3:   1,382 bytes
exec events, limit 100: 71,318 bytes
```

## AC-03 — Run Detail payloads

- Run summary content is visible before the full Run request completes.
- Secondary Overview queries wait for the complete Run.
- The initial artifact request contains `limit=31`, renders 30 rows, and uses
  the extra row only to expose the full-inventory action.
- More than 30 artifacts remain discoverable when collection metadata is
  absent or reports zero files.
- The manifest request contains `summary=true` and excludes `manifest_json`.
- A user can explicitly load the complete artifact inventory.

Observed on a large Run:

```text
artifacts, limit 31: 21,482 bytes
artifacts, full:     219,861 bytes
manifest summary:   271 bytes
manifest full:      238,936 bytes
```

## AC-04 — accurate feedback

- Event loading and catch-up states are not rendered as “no progress” or “no
  metrics”.
- Cached, live, snapshot, catch-up, and unavailable states are distinguishable.
- Missing resource telemetry does not create a synthetic GPU 0% meter.
- Manual refresh and Run status refresh show busy state and local retry where
  applicable.

## AC-05 — MCP recovery identity

- A tool process that prints a Run ID and then exits with an error returns an
  `aexp-mcp-tool-error-v1` MCP tool error with structured `partial_result`,
  `run_id`, and `next_action`.
- The caller can use the preserved Run ID for a later snapshot or status read.

## AC-06 — navigation and accessibility

- A Run modal has `role=dialog`, an accessible title, and initial focus.
- Escape closes only the topmost modal and restores the previous focus target.
- Closing a Run opened from a project Run list returns to the same project Run
  list.
- Desktop and a 390 x 844 viewport show usable Project, Run list, and Run Detail
  layouts without blocking overlap.

## AC-07 — regression suite

```bash
pnpm typecheck
pnpm test
pnpm build
go test ./...
git diff --check
```

Observed result: 27 frontend test files and 156 tests passed; all Go packages
passed. Browser console logs were empty during isolated desktop and narrow-screen
acceptance.

The browser acceptance used a copied SQLite database and a service bound to
`127.0.0.1:18080`. The installed service on port 8080 was not restarted or
modified. These checks validate product behavior; they are not experiment
results.
