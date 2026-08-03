# aexp Research Evidence Graph Acceptance Standard

Status: Binding acceptance criteria
Version: 2.0
Date: 2026-08-02
Evidence contract: `research-thread-v2`

This document is the completion contract for `docs/prd-research-evidence-graph.md`. A criterion passes only with an automated test or a recorded, reproducible verification command. “The page opens” or “the request returned 200” is not sufficient when the criterion concerns integrity or semantics.

## 0. Product comprehension contract

### READ-01 Ninety-second research read

Using the default ResearchOS Evidence route, a reader can identify from one bounded thread, without opening the giant Overview graph: the hypothesis, experiment design and comparison, immutable Result and Run provenance, projected interpretation, durable Conclusion/claim or Issue, and any `next_step` child hypothesis.

Acceptance requires UI fixtures for a positive thread, a stable negative thread, a mixed thread, and an issue-driven branch.

### READ-02 One model, multiple projections

Run facts, Project Journal, and accepted Evidence have distinct responsibilities. Research Threads, Cause/Effect focus, list search, and Overview render the same accepted semantic graph. No UI projection persists a second semantic truth or visual coordinates into `graph_hash`.

### READ-03 Default simplicity

The Project Evidence route defaults to Research Threads. Overview is explicitly secondary/advanced. Topic capacity, Result disposition, outcome counts, cross-thread branch targets, and unassigned reasons are visible without opening Overview.

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

A smoke Run cannot be accepted as `supports`, `weakens`, or `does_not_prove` evidence for a formal claim. The gate follows every `source_run_id` on a newly added or touched Result even when no visual Run node exists. A conclusion-bearing Result requires succeeded `formal` or explicitly compatible `ablation` Runs and a released EvidenceSnapshot. Failed, canceled, pilot, smoke, cross-Project, and provenance-incomplete Run sources produce structured blockers. Failed/invalid terminal Runs may only be curated through a justified `reveals_issue` outcome or remain in Journal. Values are never inferred from titles, commands, or current project configuration.

A dataset reference is eligible only when its immutable ID, `dataset@version`, and manifest SHA-256 match a registry row whose state is `verified`; a caller-supplied hash or metadata-only `registered` row is rejected by submit, proposal-plan, and freeze-plan.

### SEM-05A Canonical authored nodes and outcomes

Every newly added `claim` declares `claimKind=hypothesis|result`. Every touched Result declares `resultDisposition=conclusion|issue|mixed|pending`, provides immutable provenance, and has outgoing edges consistent with that disposition. Removing or replacing an outcome edge revalidates the final merged graph. Historical untyped Claims remain readable but cannot be copied or upserted to bypass this rule.

Stable negative evidence routes to a negative Conclusion with `weakens`/`does_not_prove`. A setting, data, implementation, or interpretation limitation routes to Issue with a rationale. A pending Result supplies a reason. Every new or touched Result has an incoming Experiment Design `next_step` edge; missing it yields `RESULT_DESIGN_LINK_MISSING`, and failure to appear in the shared hypothesis-led projection yields `RESULT_THREAD_UNASSIGNED`. Outgoing Result → Hypothesis/Experiment/Result semantic edges are blocked.

Proposal-plan returns the merged candidate `projected_research`. A canonical Hypothesis → Design → Result proposal is acceptable only when there are no blockers, the touched nodes are absent from `unassigned`, and the intended Thread reports the expected Result count. `threadRootId`-style metadata alone never establishes ownership.

### SEM-06 Evidence compatibility

When a claim declares dataset manifest or evaluation protocol context, incompatible support/weakening evidence is rejected. A matching context passes.

### AUDIT-01 Accepted-map audit

Store, API, CLI, and typed MCP expose the same `evidence-map-audit-v1` read model. `blockers` and `warnings` are always arrays and deterministically ordered. `--fail-on-blockers` exits non-zero after emitting the JSON report. Unknown Maps return 404/a typed not-found error. Audit performs no write.

### AUDIT-02 Result-aware provenance

Audit follows every accepted v2 Result's `source_run_ids` and `source_snapshot_ids`, validates object existence, Project ownership, terminal Run state, disposition/outcome agreement, and—when the Result reaches a Conclusion—formal readiness plus the exact snapshot's released state. Missing or invalid v2 references block eligibility. Legacy visual Run readiness is a compatibility warning; history remains readable and is never rewritten automatically.

## 4. Journal-backed Evidence proposal workflow

### PROP-01 Required Agent outcome

A typed Agent workflow can append a Project Journal entry without opening a proposal. When a durable judgment changes, it can submit a project-scoped proposal optionally linked to Journal entries and zero, one, or many Runs. Ordinary Journal writing never requires `no_graph_impact`.

### PROP-02 Idempotency and cardinality

Submitting the same canonical proposal content returns the existing proposal hash/status. Proposal idempotency is scoped by target map and semantic content rather than by a single Run.

### PROP-03 Review boundary

Submitting a proposal does not modify accepted graph nodes, edges, revision, or hash.

### PROP-04 Plan and accept

Planning acceptance has no side effects and returns proposed changes and all blockers. Accepting a clean proposal applies the patch transactionally, records reviewer/time, advances the graph revision when semantic content changes, and marks the proposal `accepted`.

A new or touched `Conclusion/Issue --next_step-->` branch is eligible only when its target is a canonical `claimKind=hypothesis`. A historical bypass remains readable and produces `LEGACY_THREAD_BRANCH_BYPASS` during accepted-map audit; it does not block an unrelated proposal. Editing either bypass endpoint without repairing the edge produces `THREAD_BRANCH_HYPOTHESIS_REQUIRED`.

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
- submit a Journal-backed, project-scoped Evidence proposal;
- plan and accept/reject a proposal;
- list pending proposals;
- run a side-effect-free Result-aware audit with a stable schema version and machine-readable eligibility;
- export a canonical read-only graph snapshot.

JSON mode emits structured fields and does not mix progress prose into stdout.

### MCP-01 Agent workflow

Typed MCP tools expose Project Journal writing, proposal submission/planning/status, and Result-aware Evidence audit. No MCP tool directly mutates accepted graph without the review operation.

The default tool surface also exposes the server-authored Research Thread projection, capacity/thread-family guidance, safe proposal rebase, bounded reorganization planning, and `aexp_branch_from_outcome`. The typed branch tool creates an idempotent pending proposal from an accepted Conclusion/Issue to a child Hypothesis, optionally adds its Experiment Design, immediately returns the side-effect-free plan, and exposes neither raw patch fields nor acceptance controls. `aexp_cli` rejects direct accepted-graph writes (`evidence add-node`, `add-edge`, and whole-graph save) while preserving read, audit, plan, and proposal commands. Rejected attempts leave graph revision and hash unchanged.

### MCP-02 Low-noise responses

List tools default to compact summaries with bounded result counts. Full graph/revision payloads require an explicit get call.

### CTX-01 Compact default research entry

Store, CLI, and typed MCP expose `project-research-context-v1` as a side-effect-free Project orientation read. It includes project identity, bounded active Map/Thread summaries, recent Journal headings and open next actions, total/active Run counts plus bounded compact recent Runs, pending proposal summaries, warnings, and typed `next_reads`. Unknown Projects return a typed not-found error.

### CTX-02 Bounded payload and honest omissions

Default context limits are deterministic and independently configurable. An automated representative fixture serializes to at most 8 KiB. The payload does not contain Journal Markdown bodies, full Run commands, log text, metric histories, artifacts, manifests, graph bodies, or revision snapshots. Every omitted detail that is useful for likely continuation has an exact-ID or filtered `next_reads` hint.

### CTX-03 Run discoverability is preserved

The default MCP surface includes compact, bounded `aexp_list_runs` with project/status/kind/resource filters and pagination, plus exact `aexp_get_run_snapshot` and `aexp_tail_run_logs`. A Project with many Runs remains discoverable without loading the full ledger into the Agent context.

### MCP-03 Capability profiles and compatibility

With no profile environment variable, `tools/list` exposes no more than 12 default research tools and includes Project Context, Journal create/get, resource list, Run submit/list/snapshot/logs, and proposal create/plan. `advanced` adds evidence curation and operational drill-down; `admin` adds maintenance controls; `all` exposes the complete registry. Tools hidden from `tools/list` remain callable by exact legacy name during the documented compatibility window. No profile permits direct accepted-graph mutation without reviewed acceptance.

## 6. ResearchOS UI

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

The review surface shows source Journal entries, Result-level Run/snapshot provenance, base/current revisions, proposed nodes/edges, blockers, and accept/reject actions. A stale proposal cannot be accepted without planning or a safe object-level rebase.

### UI-04 States and readability

Eligible, pending, accepted, rejected, expired, conflicted, and blocked states have distinct text and visual treatment. Meaning is not conveyed by color alone. Long node text and large graphs remain contained and scrollable.

### UI-04A Thread contract visibility

The default reader names Topic, Research Thread (假设链), and Stage Column consistently. Its compact status row independently shows readability (`legacy_readable` or current), v2 contract compliance, and publication readiness; presentation density never masquerades as a semantic or release failure. Thread headers show the derived structural phase and only surface complexity when the advisory 12/16-node thresholds are crossed. Result cards display a disposition badge, and a Conclusion/Issue `next_step` to a child Hypothesis renders a named jump target. Capacity guidance is subordinate presentation advice. Every unassigned card displays its server-provided reason and identifies the section as a temporary inbox.

The UI consumes server-provided Thread, interpretation, structural-health, and audit projections. When graph revision and projection revision differ, it shows a short syncing state and refetches; it must not reconstruct accepted Research Thread ownership locally or let proposal preview replace accepted health.

### UI-04B Cause/effect focus

Hovering, focusing, or activating a card always establishes it as the origin. Connected cards in the same thread remain emphasized and unrelated cards are muted. If the card has no direct adjacent-stage relation, only the origin remains emphasized and the UI explicitly says there is no direct cause/effect relation; it must not present every card as selected. Keyboard/touch activation has the same semantics. If a connected peer is hidden by search, the UI reports the hidden relation count. Cross-thread relations remain explicit jump chips instead of long overlay lines.

### UI-05 Legacy chain

A legacy Evidence Chain with old types renders without a blank screen, remains navigable, and can be archived read-only.

## 7. Regression, build, and deployment

### TEST-01 Backend

All existing Go tests pass. New tests cover migration, hashing, CAS conflicts, atomicity, semantic validation, proposal lifecycle, API status codes, CLI JSON, and MCP mapping.

### TEST-02 UI-v2

All existing UI tests pass. New tests cover deterministic layout, proposal states, conflict handling, legacy rendering, the four canonical Result dispositions (positive, stable-negative, mixed, issue-driven), and connected/disconnected/filtered cause-effect focus including keyboard/touch activation.

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
