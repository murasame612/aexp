# aexp Research Evidence Graph PRD

Status: Accepted for implementation
Version: 2.0
Date: 2026-08-02
Evidence contract: `research-thread-v2`

## 1. Summary

aexp already records runs, metrics, artifacts, Project Journal entries, and curated Evidence Maps. The missing piece is no longer another graph feature. It is one small, enforceable contract shared by the backend, MCP, Skill, and ResearchOS so a reader can answer, in under 90 seconds:

1. What hypothesis or research question was tested?
2. How was it tested and compared?
3. What immutable Run facts were observed?
4. What interpretation follows from those facts?
5. Which durable conclusion/claim changed, or which new issue and next hypothesis emerged?

```text
immutable Run facts
→ Project Journal working reasoning
→ reviewed Evidence proposal
→ bounded research thread
→ frozen evidence / paper claim
```

This work narrows the existing Evidence Graph into a curated research-publication layer. SQLite remains the only writable source of truth. Project Journal is the cheap append-only working layer. Evidence proposals are the reviewed promotion boundary. ResearchOS reads accepted semantics through a deterministic five-stage thread projection; the giant free-form graph is an advanced inspection/editing view, never the default research-reading experience.

The graph represents durable research judgments, not every step of research reasoning. A Project remains the execution, data, Journal, and Evidence ownership boundary. A Run remains the immutable execution record.

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

A Run never directly becomes a claim. Immutable execution facts are first interpreted in the Project Journal. Only a Journal-backed finding that changes a durable research judgment is promoted through a reviewed Evidence proposal. A proposal may reference zero, one, or many Runs; each Result still carries its own immutable `source_run_ids` and/or `source_snapshot_ids`.

### 3.3 Agent proposes; accepted graph is reviewed state

An Agent may append working reasoning to the Project Journal and may promote a bounded finding through an Evidence proposal. Ordinary notes do not require a graph-impact declaration. The Agent may not silently mutate the accepted graph or accept its own proposal.

### 3.4 Semantics precede layout

Node identity, typed relations, provenance, and compatibility rules are canonical. Coordinates are a UI projection. Automatic layout must make the graph readable without manual dragging; user pinning remains available.

### 3.5 Keep the graph selective

The graph contains decision-changing evidence: dataset or protocol changes, matched baselines, formal multi-seed results, important negative controls, withdrawn conclusions, current claims, issues, and next steps. It is not a mirror of every Run.

### 3.6 Preserve history

Existing chains and legacy node/edge types remain readable. A stale chain may be archived read-only. Accepted semantic changes create append-only revisions.

### 3.7 Time to write, structure to read

The accepted Evidence DAG remains the semantic source of truth, while one shared server projection derives bounded research threads for both UI and Agents. A thread is not a stored node or a second graph model. It is a deterministic read projection with five presentation stages: hypothesis, experiment design, experiment result, interpretation, and outcome. Interpretation is derived from the Result's real Conclusion/Issue edges and is never persisted as a node. The outcome column contains durable Conclusions and Issues. Protocol details belong to the experiment design; Run and dataset identity remain provenance instead of collection nodes. Neutral and long-range context stays in Focus/Overview; cross-thread causality becomes compact bridge metadata; legacy semantic nodes that cannot be assigned safely remain visible in a triage section with a stable reason.

New proposals must not bypass the interpretation boundary: a Result may emit outcome edges only to a Conclusion (`supports`, `weakens`, `does_not_prove`) and/or an Issue (`reveals_issue`). It must not connect directly to a Hypothesis, experiment design, or another Result. Accepted legacy bypasses remain readable and auditable until reviewed reorganization removes them; neutral `related_to/custom` context does not form the research spine.

A Topic answers one stable research decision question. A Research Thread is one hypothesis-led causal chain inside it. A Stage Column is one of the five read-only presentation buckets; it is not a node, container, or second write model.

Topic size is not governed by a hard four-Thread or 32-node semantic limit. `research-health-v2` evaluates provenance declaration, Result disposition/outcome completeness, unassigned ratio, possible duplicate Results, follow-up links, and per-Thread complexity. Twelve semantic nodes in one Research Thread is a review prompt and sixteen is a strong complexity prompt, never an automatic blocker. `capacity` is retained only for rendering load and deterministic split candidates; a Topic may legitimately contain several coherent Research Threads when they answer the same decision question. Eight unassigned items remains a cleanup prompt because `unassigned` is a temporary inbox, not a permanent collection.

Status is deliberately separated: `legacy_readable` means historical accepted content remains readable without destructive migration; `v2_compliant` means current Research Thread semantics satisfy the v2 contract; `publication_ready` means eligible formal Results also pass provenance and release gates. Structural density does not imply contract failure, and graph compliance does not imply scientific correctness or manuscript readiness.

Every new or touched Result declares `resultDisposition=conclusion|issue|mixed|pending`. The declaration must agree with its real outcome edges: Conclusion edges use `supports`, `weakens`, or `does_not_prove`; Issue edges use `reveals_issue`; mixed has both; pending has neither. Issue, mixed, and pending results explain why no single stable conclusion is available. Historical Results without this field remain readable as legacy/unclassified and are not rewritten in bulk.

Branching is explicit rather than Run-driven. A negative or ambiguous result reaches a child hypothesis through `result → reveals_issue → issue → next_step → hypothesis`; a supported mechanism reaches an extension through `result → supports → conclusion → next_step → hypothesis`. Both a Conclusion and an Issue may motivate a new hypothesis. Only their final `next_step` establishes parent/child thread identity. A hypothesis that changes the task, protocol, prediction target, or independently reportable decision belongs in a new Topic instead.

The execution spine is explicit: `hypothesis → next_step → experiment design → next_step → result`. A design-to-result `related_to/custom` edge is only background context and must not be relied upon for thread ownership or causal rendering.

Agent authoring follows the same thread grammar. It reads the shared projection before routing or cleaning a Map, submits semantic cleanup in bounded reviewable proposals, and never writes visual coordinates. Incomplete research is allowed and represented by advisory plan warnings; only graph integrity, provenance, routing, and true object-level revision conflicts are blockers. Unrelated revision advances are handled by safe proposal rebase.

### 3.8 Three layers, one research story

The system deliberately has three different writing costs:

| Layer | Purpose | Write cost | Review |
| --- | --- | --- | --- |
| Run | Immutable execution facts, metrics, logs, artifacts, environment, and provenance | Automatic | None |
| Project Journal | Working memory: observations, failed-run diagnoses, decisions, next actions, and optional Run references | Very low, append-only | None |
| Evidence Map | Curated hypotheses, experiment designs, results, conclusions, issues, and cross-thread branches that change a research decision | Deliberate | Proposal plan plus human acceptance |

An exploratory measurement, implementation failure, smoke check, or unpromoted idea stays in the Journal. Evidence is promoted only when it changes a bounded research judgment. Tightening Evidence gates must never make ordinary working notes expensive.

### 3.9 Gate classes

The backend is the only rule source and exposes three explicit classes:

- **blocker**: referential integrity, typed outcome rules, project/routing ownership, immutable Result provenance, formal evidence readiness, plan/hash integrity, or a real object-level revision conflict;
- **warning**: an incomplete but honest Research Thread, missing follow-up, disconnected authored content, unassigned backlog, or presentation-density pressure;
- **Journal-only**: preliminary reads, failed-run diagnostics, batch bookkeeping, hypotheses not yet promoted, and casual cross-Topic context.

An unverified Hypothesis is the normal start of research and is neither a blocker nor a warning. Historical accepted graphs remain readable. `research-thread-v2` hard rules apply to newly added or touched semantic objects, not through a destructive bulk rewrite of history.

## 4. Scope

### 4.1 `research-thread-v2` canonical write model

| Type | Meaning |
| --- | --- |
| `claim` + `claimKind=hypothesis` | A falsifiable research judgment that starts a thread |
| `experiment` | The design: comparison, dataset/split, preprocessing, seeds, metric, decision rule, and budget |
| `claim` + `claimKind=result` | A bounded interpretation-ready fact backed by immutable Run and/or EvidenceSnapshot provenance |
| `conclusion` | A durable positive or negative research conclusion/claim |
| `issue` | A material limitation, invalidating condition, or unresolved ambiguity |

`Interpretation` is a read projection derived from a Result's disposition and real outgoing outcome edges. It is never a stored node. DatasetVersion, Run, seed, config, Git, Freeze, and snapshot identities are provenance on the design/result rather than collection nodes in the primary thread.

Legacy `dataset`, `protocol`, `run`, `plan`, `hypothesis`, `note`, and protocol-group nodes remain readable. They may be reorganized through reviewed proposals, but new default Agent authoring uses only the canonical types above. Every newly added `claim` must declare `claimKind`; an untyped historical Claim remains compatible but cannot be used to bypass v2 authoring gates.

### 4.2 `research-thread-v2` semantic spine

| Edge | Allowed direction | Meaning |
| --- | --- | --- |
| `next_step` | Hypothesis → Experiment | The design tests the hypothesis |
| `next_step` | Experiment → Result | The immutable result was produced under that design |
| `supports` / `weakens` / `does_not_prove` | Result → Conclusion | The result changes confidence in a bounded claim; a stable negative result is still a Conclusion |
| `reveals_issue` | Result → Issue | The result cannot yet form a stable conclusion because of a stated limitation, ambiguity, or invalidating condition |
| `next_step` | Conclusion/Issue → child Hypothesis | The accepted outcome opens an explicit child research thread |
| `supersedes` | old durable node → new durable node | A reviewed newer judgment replaces an older one without erasing history |

`mixed` Results have both Conclusion and Issue exits. `pending` Results have neither exit and must explain what evidence is still missing. A Result never jumps directly to a Hypothesis, another Result, or another Experiment. Failed/invalid Runs stay in Journal unless they reveal a research-relevant Issue; they never support or weaken a Conclusion. Multiple seeds normally produce one aggregate Result with multiple `source_run_ids`. Ablations may share one Experiment design while producing separate Results.

`custom`, `related_to`, and legacy structural relations remain readable background context but do not define the research spine, thread ownership, formal readiness, or child-thread identity.

The branch rule is enforced at the proposal boundary rather than by rejecting the entire historical graph. A newly added `Conclusion/Issue --next_step--> ...` edge must target a canonical `claimKind=hypothesis`. Editing either endpoint of a historical bypass also requires that bypass to be repaired in the same patch. Unrelated proposals remain eligible, while accepted historical bypasses are reported by audit as `LEGACY_THREAD_BRANCH_BYPASS` warnings.

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

### 4.4 Journal-backed Evidence proposal

Project Journal is the default interpretation record. Its Markdown body and Run references are append-only; only the next-action state may move through an audited status transition. Evidence proposal is a project-scoped, Run-optional promotion object with:

- `journal_entry_ids` as optional source reasoning;
- optional proposal-level `source_run_ids` and `source_snapshot_ids` for convenience;
- a semantic graph patch;
- `proposal_hash`, `base_graph_revision`, status, and review metadata.

Proposal-level sources never replace Result-level provenance. A one-Result proposal may copy its sources into that Result deterministically; multi-Result proposals require an explicit mapping. Project Run Cards and RunMarks remain readable compatibility records but are not new writing paths.

Agent patches may add or reorganize bounded nodes and edges, including a `supersedes` edge. Removal or rewriting of accepted evidence remains a user-reviewed operation.

Rules:

- proposal identity and idempotency are project/map scoped, not one-per-Run;
- the same proposal hash is idempotent;
- a different pending proposal replaces only that Run's unaccepted proposal;
- proposal acceptance requires the current graph revision to match `base_graph_revision`;
- stale acceptance returns a structured conflict and never partially applies;
- pending proposals expire after 14 days;
- a Result that concludes, supports, weakens, or fails to prove a claim recursively validates every `source_run_id`; a visual Run node is not required for this gate;
- conclusion-bearing Result provenance accepts only succeeded `formal` or explicitly compatible `ablation` runs with released evidence snapshots;
- failed, canceled, smoke, pilot, or provenance-incomplete Runs cannot support, weaken, or fail to prove a formal claim;
- a failed or invalid terminal Run may only enter curated Evidence through a justified `reveals_issue` path;
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
- referenced Journal entries, Runs, and snapshots exist and belong to the Project;
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

Legacy Overview clients may optionally attach a reviewed `layout_intent` with deterministic
left-to-right ranks. The outer array defines columns and the inner arrays define
top-to-bottom card order. The UI previews that intent and converts it to
coordinates only after acceptance. Unknown or duplicate node IDs are blocked;
legacy protocol containers are omitted from the primary thread projection while
their semantic experiment-design members remain visible individually. Layout
intent participates in Proposal identity, but accepted coordinates remain
projection metadata and do not affect `graph_hash`.

This is a compatibility path, not the default Agent workflow. The shared
research-thread view uses fixed swimlanes and never persists layout. For legacy
intent, acceptance is the explicit authorization to move every card named by the
intent, including a previously pinned card. Cards omitted from the intent keep
their positions. This avoids a second reset-pins switch while preserving manual
layout outside the reviewed scope.

Layout is computed client-side from semantic graph data and persisted as layout metadata. It does not affect `graph_hash`.

### 4.7 ResearchOS information architecture

The Project remains visible and useful:

- Overview: compact research digest, active claim/issue/next-step counts, and pending graph proposal count;
- Runs: immutable execution records; legacy Project Run Cards remain readable but hidden from the default writing flow;
- Research Threads: the default fixed-stage reader, outcome summaries, Result disposition, child-thread jumps, capacity guidance, and proposal review;
- Overview Graph: an advanced semantic inspection/manual-editing view; it is never the default way to understand a Project;
- Data & Evidence: datasets, artifacts, Freeze state, and NAS locations.

The Evidence Map is retained as one backend model. Research Threads, local Cause/Effect focus, list search, and Overview are projections of that same accepted graph, not additional writable stores.

The default Project Evidence route opens the thread reader. Each thread is read left-to-right as Hypothesis → Experiment Design → Result → Interpretation → Outcome. Its header summarizes `N conclusions · M issues · K pending`. Result cards show `conclusion`, `issue`, `mixed`, or `pending` disposition. Cross-thread `next_step` relations appear as explicit jump anchors. Topic capacity (`healthy`, `near_limit`, `split_recommended`, `cleanup_required`) and deterministic thread-family suggestions are visible to both users and Agents. Unassigned content displays its stable reason instead of silently disappearing.

Proposal review shows:

- source Journal entries and Result-level Run/snapshot provenance;
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
- submit or update a project-scoped, Journal-backed Evidence proposal;
- run a side-effect-free Result-aware Evidence audit;
- plan proposal acceptance without side effects;
- accept/reject/expire a proposal;
- list pending proposals by project/chain;
- retrieve revision history and a canonical snapshot.

The default Agent entry point is `aexp_get_project_research_context` (CLI: `aexp project context <project_id> --json`). It is a compact, read-only orientation document, not a new source of truth. It contains project identity, active Topic and Research Thread summaries, recent Journal headings and open next actions, compact recent Run summaries and counts, pending proposal summaries, structural warnings, and typed `next_reads`. It deliberately omits Journal bodies, full Run commands, logs, metrics series, artifacts, manifests, graph bodies, and revision payloads. Those remain available through explicit paginated lists and exact-ID drill-down tools.

Run access remains a first-class default capability. `aexp_list_runs` provides bounded, filterable, cursor-based discovery; `aexp_get_run_snapshot` and `aexp_tail_run_logs` provide exact-Run detail. The compact context must never embed the unbounded Run ledger merely to make it discoverable.

MCP tool visibility is divided into capability profiles:

- `research` (default): no more than 12 tools for orientation, Journal writing, resource discovery, Run submit/list/snapshot/logs, and proposal creation/planning;
- `advanced`: adds Topic/Thread inspection, audit, rebase, branch, reorganization, asset, release, and operational controls used during explicit curation sessions;
- `admin`: adds project doctor/init, ownership correction, cancellation, and other maintenance controls;
- `all`: compatibility profile exposing the complete registry during migration.

The profile is selected with `AEXP_MCP_TOOL_PROFILE`. Tool visibility is a context-budget and discoverability policy, not an authorization boundary: legacy exact-name calls remain dispatchable during the deprecation window. No profile exposes a direct “mutate accepted graph without review” operation. The generic `aexp_cli` tool must reject direct Evidence write subcommands such as `add-node`, `add-edge`, and whole-graph `save`; read, plan, audit, and proposal operations remain available only in the appropriate advanced/compatibility surface.

The backend owns validation, capacity, thread projection, interpretation projection, rebase, and hashing. MCP transports typed data, the `aexp-experiment` Skill defines workflow discipline, and ResearchOS renders the server projection. Those layers may point to this contract but must not maintain independent semantic rule tables.

## 5. State flows

### 5.1 Agent proposal

```text
Run finishes
→ Agent appends Journal interpretation (optional Run references)
→ durable research judgment changed?
   ├─ no → remain in Journal
   └─ yes → pending Evidence proposal at base revision
            → side-effect-free proposal plan
            → human accepted | rejected | expired | safe rebase/conflict
            → accepted-map audit
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
- Existing Project Run Cards and RunMarks remain readable compatibility data and are not rewritten.
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
3. Complete project-scoped Journal-backed proposal/audit workflow and structured blockers.
4. Add UI-v2 research graph layout and proposal review.
5. Migrate a database backup and run integrity/count checks.
6. Run complete Go and UI-v2 tests and production builds.
7. Back up the installed binary, replace it, restart `com.ziwu.aexp`, and verify API/UI health.

Rollout stops before binary replacement if migration rehearsal, tests, or production build fail.
