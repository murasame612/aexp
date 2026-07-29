# aexp compatibility and deprecation ledger

Baseline: `vNext-system-simplification` (2026-07-24)

Removal rule: no compatibility entry is removed until its replacement has
shipped for two releases, production usage has been checked, and the real
SQLite migration rehearsal still passes. Old records remain readable after a
write entry is disabled.

| Compatibility entry | Status in vNext | Canonical replacement | Usage observation | Earliest removal |
| --- | --- | --- | --- | --- |
| `dataset ...` CLI | hidden, callable | `asset publish/get/list`; Run prepares inputs | shell/MCP audit plus release checklist | vNext+2 |
| `storage`, `fs`, `transfer`, `sync` CLI | hidden administrator compatibility | Asset intent; Settings/System Activity diagnostics | service log and explicit admin use | vNext+2 |
| low-level storage/path/transfer MCP tools | callable but omitted from `tools/list` | `aexp_asset_*`, Run submit, system readiness | MCP client tool-call audit where available | vNext+2 |
| `run freeze` / `freeze ...` | hidden/deprecated, old data readable | output publishing → `snapshot create` → `release evaluate` | freeze table growth and CLI audit | vNext+2 |
| direct Evidence `create/add-node/add-edge/list` CLI | hidden expert compatibility | Project primary Map + proposal plan/review | Evidence revision audit | vNext+2 |
| `aexp_create_evidence_chain` and generic `aexp_cli` MCP | callable but omitted from default tools | Project primary Map and typed tools | MCP call audit | vNext+2 |
| Project Run Card tools | callable but omitted from default tools | Evidence proposal / Run interpretation projection | `project_run_cards.updated_at` audit | vNext+2 |
| RunMark CLI/MCP/API and historical records | CLI hidden; MCP callable but omitted from default tools; UI read-only under Run Raw | Project Journal with optional zero-to-many Run references | `run_marks.created_at` audit and legacy API access log | vNext+2 |
| manual project categories | legacy records readable and removable; create/assign API returns `410 MANUAL_PROJECT_WRITE_DEPRECATED`; excluded from canonical Project scope | `ProjectDefinition.id` plus explicit Card reassign | row-count and write timestamp audit | vNext+2 |
| legacy `/projects` aggregation API | read compatibility | `/project-definitions` and Project-scoped endpoints | HTTP access log | vNext+2 |
| legacy UI `/` | compatibility link under UI-v2 | `/ui-v2/` | HTTP access log | vNext+2 |
| `Store.UpdateRun` full-row method | Store compatibility only; production executor has no unconditional call sites | lifecycle CAS, status-observation, failure-metadata and finalization narrow writers | repository call-site audit (`rg '\\.UpdateRun\\('`) | vNext+2 |

The presence of a compatibility implementation does not authorize new product
features on it. New research writes must target Project, Asset, Run, Snapshot /
Release, or a reviewable Evidence proposal. Administrator diagnostics may read
Transfer, Placement, Storage, and Binding state but must not create a second
research fact source.
