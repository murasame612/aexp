# Implementation — aexp Product Polish v0.3.2

Date: 2026-08-09

## Goal

This pass makes ResearchOS feel faster and more trustworthy without changing
experiment semantics or the portability recovery contract. It focuses on four
user-visible boundaries: initial payload size, background request volume,
progress and error feedback, and information hierarchy on desktop and narrow
screens.

## Request and payload control

- Evidence Run candidates are loaded only while the Run tray or a Run preview
  is open. Closing the tray stops the 12-second candidate poll.
- Dashboard requests three execution events, matching the three rows it shows.
  The full execution page keeps its normal pagination.
- `stats` uses indexed Run counts instead of loading and decoding every Run.
- Run Detail renders 30 artifacts and requests one sentinel row to detect a
  larger legacy inventory even when collection metadata is absent. The full
  artifact list is fetched only after an explicit user action. The manifest is
  loaded as a summary.
- Static UI assets are served with compression and immutable caching.

## Frontend loading model

Large project pages are route-level lazy modules. Evidence, Journal,
Literature, Data Center, Launchpad, Matrices, Settings, and Evidence Freeze no
longer belong to the common initial JavaScript path. ECharts uses only the line,
grid, legend, tooltip, and canvas modules required by the metric view.

Run Detail uses the Run summary already present in the list as an immediate
preview. The complete Run is loaded first; journal, artifacts, manifest, data
bindings, and event views follow only when the Overview is eligible. Searches
in Runs and Journal are debounced while the previous results remain visible.

## Feedback contract

- MCP tool errors use the `aexp-mcp-tool-error-v1` envelope and preserve partial
  stdout. If a Run ID was already emitted, the error payload returns it with a
  recovery action instead of losing the created Run identity.
- Event views distinguish initial loading, catch-up, live, cached snapshot, and
  unavailable states. An empty array is shown as an actual empty result only
  after the first snapshot has settled.
- Global and project headers show service connectivity. Authentication editing
  stays collapsed unless requested or a 401 response requires attention.
- Manual refreshes, status checks, and full artifact loading expose busy and
  retry states.

## Information hierarchy and accessibility

- Project cards use Work Journal as the explicit primary action and expose
  Literature, Runs, Assets, and Evidence as secondary destinations.
- Run filters have persistent labels, a result count, and a clear action.
- Missing GPU telemetry is shown as unavailable rather than a fabricated 0%.
  Snapshot freshness is visible.
- Modals use dialog semantics, initial focus, Escape handling, focus
  restoration, and sticky headers and detail tabs.
- Closing a Run opened from a project Run list returns to that project list.

## Scoped cleanup

Unused `ProjectView`, `getProjects`, `ProjectSummary`, and project preview code
were removed. The legacy `/api/v1/projects` endpoint remains available for
external compatibility; removing it requires a separate compatibility audit.

CSS consolidation is deliberately deferred. The new rules stay in the current
theme layer so this pass does not combine a product behavior change with a
high-risk full stylesheet rewrite.

## Deferred follow-up

The next performance phase should classify Run changes so observation-only
updates do not invalidate artifacts, manifests, freezes, and bindings. It can
then reduce the redundant SSE, catch-up, active-summary, and list polling paths
without weakening recovery from a disconnected stream.
