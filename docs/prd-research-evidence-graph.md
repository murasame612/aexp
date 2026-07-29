# aexp Research Evidence Graph PRD

Status: Accepted for implementation  
Version: 1.0  
Date: 2026-07-23

## 1. Summary

aexp already records runs, metrics, artifacts, project cards, and an editable Evidence Chain. The missing layer is not another experiment scheduler or a second data platform. The missing layer is a maintained research evidence graph that explains why selected runs matter:

```text
immutable Run
→ Project Run Card
→ reviewed graph patch
→ Research Evidence Graph
→ frozen evidence / paper claims
```

This work upgrades the existing Evidence Chain into the project's typed Research Evidence Graph. It keeps SQLite as the only writable source of truth, reuses Project Run Card as the Agent's proposal envelope, adds revision and semantic validation, and makes automatic layout the default projection in UI-v2.

The graph represents research reasoning. A Project remains the execution and resource boundary. A Run remains the immutable execution record.

## 2. Problem

The current Run list answers what ran, where it ran, and what it produced, but not:

- which hypothesis or claim the run tested;
- which dataset and evaluation protocol made the comparison valid;
- whether a later protocol fix superseded an earlier result;
- whether the result supports, weakens, or fails to prove a claim;
- which issue was discovered and what experiment should happen next.

The current Evidence Chain can express a loose graph, but it is easy to stop maintaining:

- arbitrary node and edge types do not encode research semantics;
- absolute free-form positioning makes every update a manual layout task;
- whole-graph replacement has no revision conflict protection;
- the same run can be inserted repeatedly;
- stale Agent context can overwrite newer user edits;
- Project Run Cards do not carry a reviewable graph change;
- old and current protocols can coexist without an explicit supersession relation.

The result is that execution history is mistaken for research history.

## 3. Product principles

### 3.1 One writable truth

SQLite is canonical. YAML, JSON, Freeze manifests, and paper evidence are one-way snapshots or exports. aexp must never reconcile two independently editable graph stores.

### 3.2 Runs are evidence, not conclusions

A Run never directly becomes a claim. The Run is interpreted in a Project Run Card and then connected to the graph through a reviewed patch.

### 3.3 Agent proposes; accepted graph is reviewed state

An Agent may create a Project Run Card and a graph patch, or state why the run has no graph impact. It may not silently mutate the accepted graph.

### 3.4 Semantics precede layout

Node identity, typed relations, provenance, and compatibility rules are canonical. Coordinates are a UI projection. Automatic layout must make the graph readable without manual dragging; user pinning remains available.

### 3.5 Keep the graph selective

The graph contains decision-changing evidence: dataset or protocol changes, matched baselines, formal multi-seed results, important negative controls, withdrawn conclusions, current claims, issues, and next steps. It is not a mirror of every Run.

### 3.6 Preserve history

Existing chains and legacy node/edge types remain readable. A stale chain may be archived read-only. Accepted semantic changes create append-only revisions.

## 4. Scope

### 4.1 V1 node types

| Type | Meaning |
| --- | --- |
| `dataset` | An immutable dataset version or material data revision |
| `protocol` | Split, preprocessing, validator, metric, or evaluation protocol |
| `run` | A selected aexp Run with a stable `run_id` |
| `claim` | A bounded research conclusion or hypothesis state |
| `issue` | A discovered data, protocol, implementation, or interpretation problem |
| `plan` | A proposed next experiment or decision |

Legacy types `hypothesis`, `experiment`, `conclusion`, and `note` remain readable and editable for compatibility, but new Agent proposals use the V1 types.

### 4.2 V1 semantic edge types

| Edge | Allowed direction | Meaning |
| --- | --- | --- |
| `uses` | dataset/protocol → run | Run depends on this data or protocol |
| `supports` | run → claim | Evidence increases confidence in a bounded claim |
| `weakens` | run → claim | Evidence decreases confidence in a bounded claim |
| `reveals_issue` | run/claim → issue | Evidence exposes a material problem |
| `supersedes` | old node → new node | A newer dataset, protocol, claim, or plan replaces an older one |
| `next_step` | claim/issue → plan | The evidence motivates a concrete next action |

Legacy `does_not_prove` remains readable. `custom` and visual-only relations are excluded from semantic DAG validation and release reasoning.

### 4.3 Chain metadata and revisions

An Evidence Chain gains:

- optional `project_id`;
- `role`: `primary`, `secondary`, or `archive`;
- `status`: `active` or `archived`;
- monotonically increasing semantic `revision`;
- deterministic `graph_hash`.

Each accepted semantic mutation stores an append-only revision snapshot containing revision number, graph hash, actor/source metadata, and canonical graph JSON.

Graph hashing:

- is independent of input ordering;
- includes semantic node and edge content;
- excludes coordinates, size, and pin state;
- produces the same hash for the same research graph.

Layout-only changes do not create a semantic revision.

### 4.4 Project Run Card graph proposal

Project Run Card remains the single-run interpretation record and gains:

- `graph_patch_json`;
- `graph_status`: `none`, `pending`, `accepted`, `rejected`, or `expired`;
- `proposal_hash`;
- `base_graph_revision`;
- `reviewed_at`;
- `no_graph_impact`;
- `graph_impact_reason`.

An Agent completing a meaningful Run must submit exactly one of:

1. a Project Run Card with a pending graph patch; or
2. `no_graph_impact=true` with a concise reason.

V1 Agent patches are additive: they may propose nodes and edges, including a `supersedes` edge. Removal or rewriting of accepted evidence remains a user-reviewed UI/CLI action.

Rules:

- one pending proposal per Run;
- the same proposal hash is idempotent;
- a different pending proposal replaces only that Run's unaccepted proposal;
- proposal acceptance requires the current graph revision to match `base_graph_revision`;
- stale acceptance returns a structured conflict and never partially applies;
- pending proposals expire after 14 days;
- a smoke Run cannot support or weaken a formal paper claim;
- formal evidence requires explicit dataset/protocol/provenance compatibility;
- claim-bearing evidence requires at least one DatasetVersion whose immutable ID,
  `dataset@version`, manifest SHA-256, and `verified` registry state all match;
- submit, proposal planning, and paper freeze consume one shared provenance
  readiness policy rather than maintaining independent field checklists.

### 4.5 Semantic validation

Before any semantic write, aexp validates:

- unique node and edge IDs;
- valid endpoints and allowed type directions;
- no semantic self-loop;
- no duplicate semantic edge `(source, target, type)`;
- no cycle among semantic edges;
- at most one graph node for a given `run_id` in a chain;
- referenced Run and Project Run Card exist;
- archived graphs reject semantic writes;
- support/weakening evidence has compatible dataset manifest and evaluation protocol context;
- stale `expected_revision` is rejected with the current revision and hash.

Validation failures are structured blockers, not generic server errors.

### 4.6 Automatic layout

UI-v2 provides a deterministic lightweight layout without introducing a new graph-layout service:

- horizontal ranks follow topological depth;
- nodes within a rank are ordered by `occurred_at`, then stable ID;
- node types use stable vertical lanes;
- unpinned nodes are automatically placed;
- dragging a node pins it;
- “自动排版” rearranges unpinned nodes;
- “重置固定位置” clears pins and arranges the entire graph;
- an Agent patch never sends authoritative `x`/`y`.

Layout is computed client-side from semantic graph data and persisted as layout metadata. It does not affect `graph_hash`.

### 4.7 UI-v2 information architecture

The Project remains visible and useful:

- Overview: compact research digest, active claim/issue/next-step counts, and pending graph proposal count;
- Runs: execution records and Project Run Cards;
- Research Graph: accepted graph, proposal review, filters, auto-layout, and revision state;
- Data & Evidence: datasets, artifacts, Freeze state, and NAS locations.

The Evidence Chain screen is retained as the Research Graph view. It is not replaced by another graph subsystem.

Proposal review shows:

- source Run and Project Run Card;
- base and current graph revision;
- proposed nodes and relations;
- eligibility and compatibility blockers;
- accept, reject, and edit-before-accept actions;
- explicit “no graph impact” reason.

### 4.8 API, CLI, and MCP

The existing Evidence Chain APIs remain compatible and gain revision metadata.

Required operations:

- read chain and current revision/hash;
- save a user-edited graph with `expected_revision`;
- submit or update a Project Run Card graph proposal;
- plan proposal acceptance without side effects;
- accept/reject/expire a proposal;
- list pending proposals by project/chain;
- retrieve revision history and a canonical snapshot.

MCP tools are typed around proposals. They do not expose a direct “mutate accepted graph without review” tool.

## 5. State flows

### 5.1 Agent proposal

```text
Run finishes
→ Agent writes Project Run Card
→ graph impact?
   ├─ no → reason recorded
   └─ yes → pending patch at base revision
            → review plan
            → accepted | rejected | expired | revision conflict
```

### 5.2 Accepted graph mutation

```text
expected revision
→ validate types, references, compatibility, duplicates, and DAG
→ transactionally write graph
→ compute canonical graph hash
→ append semantic revision when hash changed
→ return revision/hash
```

No partial graph or partially reviewed proposal is visible as accepted state.

## 6. Migration and compatibility

- Migration is additive and must preserve all existing table row counts.
- Existing Evidence Chains default to `secondary/active`, revision `0`, and remain readable.
- Existing graph nodes and edges retain their IDs, positions, and legacy types.
- The first semantic update creates revision `1`.
- Existing Project Run Cards default to `graph_status=none`.
- Existing API clients that only read graphs continue to work.
- UI-v2 and new CLI/MCP writes use revision-aware operations.
- No existing chain is automatically declared the project's primary graph.
- The obsolete July 5 chain may be archived only by an explicit user action.

## 7. Non-goals

V1 does not:

- build a training scheduler DAG;
- import every Run into the graph;
- create a second NAS/file-transfer system;
- make editable YAML a graph database;
- infer missing dataset or protocol provenance from command text;
- let an Agent bypass proposal review;
- use an external graph-layout daemon;
- automatically delete or rewrite historical evidence;
- treat smoke results as scientific evidence.

## 8. Rollout

1. Add schema, deterministic hashing, revision snapshots, and validation.
2. Add revision-aware API/CLI/MCP operations.
3. Extend Project Run Card proposal workflow and structured blockers.
4. Add UI-v2 research graph layout and proposal review.
5. Migrate a database backup and run integrity/count checks.
6. Run complete Go and UI-v2 tests and production builds.
7. Back up the installed binary, replace it, restart `com.ziwu.aexp`, and verify API/UI health.

Rollout stops before binary replacement if migration rehearsal, tests, or production build fail.
