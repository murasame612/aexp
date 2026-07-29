# aexp Research Evidence Graph Acceptance Standard

Status: Binding acceptance criteria  
Version: 1.0  
Date: 2026-07-23

This document is the completion contract for `docs/prd-research-evidence-graph.md`. A criterion passes only with an automated test or a recorded, reproducible verification command. “The page opens” or “the request returned 200” is not sufficient when the criterion concerns integrity or semantics.

## 1. Database and migration

### DB-01 Additive migration

Given a copy of the current production database, after opening it with the new binary:

- every pre-existing core table has the same row count;
- all existing Evidence Chains, nodes, edges, Runs, and Project Run Cards remain readable;
- new proposal fields have safe defaults;
- new revision tables are present;
- `PRAGMA integrity_check` returns `ok`.

### DB-02 Schema compatibility

Running migration twice is idempotent. A database with the historical `project_id` schema variant opens successfully and does not lose or rewrite unrelated data.

### DB-03 Append-only revisions

An accepted semantic mutation creates exactly one new revision. No operation updates or deletes an existing revision row. A layout-only move does not create a semantic revision.

## 2. Canonical graph and concurrency

### GRAPH-01 Deterministic graph hash

The same nodes and edges in different input orders produce the same `graph_hash`. Changing only node `x`, `y`, width, height, or pin state also preserves the hash. Changing a semantic title, reference, state, node, or edge changes the hash.

### GRAPH-02 Compare-and-swap

Two writers read revision `N`. The first valid semantic write succeeds as `N+1`. The second write using expected revision `N` receives HTTP/API conflict with current revision/hash and makes no database change.

### GRAPH-03 Atomicity

If any node, edge, provenance check, or revision insert fails, the graph, revision number, proposal state, and revision history remain unchanged.

## 3. Semantic validation

### SEM-01 Structural blockers

The service rejects, before writing:

- duplicate node IDs;
- duplicate edge IDs;
- missing endpoints;
- semantic self-loops;
- duplicate `(source, target, type)` semantic edges;
- a second node with the same `run_id` in one graph.

Each rejection contains a stable blocker code and a human-readable message.

### SEM-02 Typed directions

All V1 semantic edge types accept only the directions defined in the PRD. Invalid directions are rejected. Legacy edge types remain readable.

### SEM-03 DAG

Adding a semantic edge that creates a cycle is rejected. `custom` or explicitly visual-only edges do not participate in cycle detection.

### SEM-04 Archived graph

An archived graph remains readable but rejects semantic writes and proposal acceptance.

### SEM-05 Evidence eligibility

A smoke Run cannot be accepted as `supports` or `weakens` evidence for a formal claim. A formal Run missing explicit dataset, protocol, seed, or provenance context produces structured blockers rather than inferred values. A dataset reference is eligible only when its immutable ID, `dataset@version`, and manifest SHA-256 match a registry row whose state is `verified`; a caller-supplied hash or metadata-only `registered` row is rejected by submit, proposal-plan, and freeze-plan.

### SEM-06 Evidence compatibility

When a claim declares dataset manifest or evaluation protocol context, incompatible support/weakening evidence is rejected. A matching context passes.

## 4. Project Run Card proposal workflow

### PROP-01 Required Agent outcome

A typed Agent workflow can record either:

- a pending graph patch; or
- `no_graph_impact=true` and a non-empty reason.

Submitting neither is rejected for proposal-aware completion.

### PROP-02 Idempotency and cardinality

Submitting the same proposal content for a Run returns the existing proposal hash/status. A Run never has more than one pending graph patch.

### PROP-03 Review boundary

Submitting a proposal does not modify accepted graph nodes, edges, revision, or hash.

### PROP-04 Plan and accept

Planning acceptance has no side effects and returns proposed changes and all blockers. Accepting a clean proposal applies the patch transactionally, records reviewer/time, advances the graph revision when semantic content changes, and marks the proposal `accepted`.

### PROP-05 Stale, rejected, and expired proposals

- stale base revision returns a conflict and remains pending;
- reject records review metadata without changing the graph;
- pending proposals older than 14 days can be marked expired;
- accepted, rejected, and expired proposals cannot be accepted again.

## 5. API, CLI, and MCP

### API-01 Revision-aware graph writes

Graph reads include `revision` and `graph_hash`. New graph writes carry `expected_revision`. Stale writes return HTTP 409 with a structured conflict body; validation blockers return a stable 4xx response.

### API-02 Proposal endpoints

API coverage exists for submit/update, plan, accept, reject, expire, list pending, revision list, and revision snapshot. Unknown graph/proposal/revision returns 404.

### CLI-01 User workflow

CLI can:

- inspect graph revision/hash;
- submit a Run Card graph proposal or no-impact reason;
- plan and accept/reject a proposal;
- list pending proposals;
- export a canonical read-only graph snapshot.

JSON mode emits structured fields and does not mix progress prose into stdout.

### MCP-01 Agent workflow

Typed MCP tools expose proposal submission, proposal planning/status, and no-impact reporting. No MCP tool directly mutates accepted graph without the review operation.

### MCP-02 Low-noise responses

List tools default to compact summaries with bounded result counts. Full graph/revision payloads require an explicit get call.

## 6. UI-v2

### UI-01 Project hierarchy

Project UI retains the Project as the execution/resource boundary and exposes a prominent Research Graph view. The Overview shows compact counts for active claims, issues, plans, and pending proposals.

### UI-02 Automatic layout

On an unpinned graph:

- topological rank increases horizontally;
- stable type lanes and time/ID ordering make repeated layout deterministic;
- “自动排版” affects unpinned nodes only;
- dragging pins a node;
- “重置固定位置” clears pins and arranges all nodes.

Reloading preserves pins. Layout operations do not change semantic graph hash.

### UI-03 Proposal review

The review surface shows the Run, Project Run Card, base/current revisions, proposed nodes/edges, blockers, and accept/reject actions. A stale proposal cannot be accepted from the UI without replanning.

### UI-04 States and readability

Eligible, pending, accepted, rejected, expired, conflicted, and blocked states have distinct text and visual treatment. Meaning is not conveyed by color alone. Long node text and large graphs remain contained and scrollable.

### UI-05 Legacy chain

A legacy Evidence Chain with old types renders without a blank screen, remains navigable, and can be archived read-only.

## 7. Regression, build, and deployment

### TEST-01 Backend

All existing Go tests pass. New tests cover migration, hashing, CAS conflicts, atomicity, semantic validation, proposal lifecycle, API status codes, CLI JSON, and MCP mapping.

### TEST-02 UI-v2

All existing UI tests pass. New tests cover deterministic layout, pin/reset behavior, proposal states, conflict handling, and legacy graph rendering.

### BUILD-01 Production artifacts

The production Go binary and UI-v2 production bundle build successfully from the repository's declared local environments. No global package installation is used.

### DEPLOY-01 Safe database rehearsal

Before touching the live service:

1. create a SQLite backup;
2. migrate the backup with the new binary;
3. compare required table counts;
4. run `PRAGMA integrity_check`;
5. record the result.

### DEPLOY-02 Binary replacement

Only after every required test and rehearsal passes:

1. preserve a rollback copy of the currently installed binary;
2. replace it with the newly compiled binary;
3. restart `com.ziwu.aexp`;
4. verify process health, API health, UI-v2 assets, graph read, and a side-effect-free proposal plan;
5. confirm zombie-process count does not immediately grow during the verification.

If any verification fails, restore the previous binary and report the exact failed stage. Existing experimental data and remote artifacts must not be deleted.

## 8. Definition of done

The Goal is complete only when:

- every P0 criterion above passes;
- Go and UI-v2 test/build evidence is recorded;
- migration rehearsal passes against a real database backup;
- the installed binary is the verified new build;
- the service has restarted and passed post-deployment checks;
- remaining deferred P1 work, if any, is explicitly listed and does not contradict the PRD's source-of-truth or review boundaries.
