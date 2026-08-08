# Verification — aexp Portability Foundation v1

Date: 2026-08-09

## Automated acceptance

The implementation passed both the focused acceptance suite and the complete Go
regression suite:

```text
go test ./internal/portability ./internal/store ./cmd/aexp
go test ./...
```

Covered behaviors include:

- an existing SQLite database can be opened read-only;
- a missing database is rejected without being created;
- writes through the audit database handle fail;
- missing and present attachments are distinguished;
- controller-local missing paths are blocking findings;
- remote paths are recorded without filesystem or network probes;
- logical roots and placements are inventoried;
- SSH resources are marked for credential/reachability rebinding;
- persisted sentinel secrets and commands do not appear in JSON or human output;
- repeated audits over unchanged state are deterministic;
- `--json --strict` emits the report and returns an error for blocking findings;
- Run lifecycle state remains unchanged.

## Current control-plane read-only exercise

The command below was run against the current default database:

```text
go run ./cmd/aexp portability audit
```

Observed summary at verification time:

```text
status: needs_mapping
projects/runs: 4 / 697
resources: 7 (7 require rebind)
artifacts: 3848 (0 lack logical reference)
datasets: 3 (0 lack logical URI)
attachments: 40 (0 missing)
machine paths: 5697
logical coverage: 3 roots / 19 placements
findings: 0 blocking / 7 warnings
```

The seven warnings are resource-rebinding requirements. This exercise did not
contact remote resources, move data, rewrite paths, validate remote reachability,
or resume Runs.

## Acceptance conclusion

Portability Foundation v1 meets the documented acceptance standard for a
read-only preflight and stable report contract. It does not claim that aexp can
already export, import, remap, validate, or resume a workspace on another
controller. Those remain subsequent implementation phases built on this audit.
