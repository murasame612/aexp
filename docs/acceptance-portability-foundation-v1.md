# Acceptance — aexp Portability Foundation v1

Date: 2026-08-09

All automated acceptance tests use temporary databases and temporary attachment
roots. They must not read or mutate the user's production `~/.aexp` state.

## AC-01 — read-only database open

- Opening an existing database through the audit path succeeds.
- Opening a missing path fails without creating a file.
- A write attempted through the read-only handle fails.
- Audit execution does not run schema migration or reconciliation writes.

## AC-02 — deterministic report contract

`aexp portability audit --json` returns valid JSON with:

- `schema_version=portability-audit-v1`;
- `mode=read_only`;
- `target_mode=portable_control_plane`;
- stable summary counts;
- sorted path references, resource bindings, and findings.

Repeated audits over unchanged state produce equivalent content except for the
generation timestamp.

## AC-03 — controller-local missing-file audit

Given one existing and one missing attachment:

- the existing attachment is reported as present;
- the missing attachment produces `ATTACHMENT_MISSING` with severity `error`;
- the summary blocking count increases;
- `--strict` exits unsuccessfully.

Project-local roots and local git-diff captures follow the same existence rule.

## AC-04 — remote paths remain offline

Run cwd, resolved cwd, remote run directory, target cwd, storage roots, dataset
storage paths, and remote artifact paths are classified as machine-bound without
network or local filesystem probes.

Their observability state is `not_checked` unless a persisted placement state is
available.

## AC-05 — resource rebinding

Every non-tombstone SSH resource appears as a resource binding with
`rebind_required=true`. The report preserves resource identity but does not claim
that credentials or connectivity are portable.

## AC-06 — logical reference coverage

- Logical roots and path placements are counted.
- Dataset versions without `logical_uri` produce
  `DATASET_LOGICAL_URI_MISSING` warnings.
- Absolute artifacts without `source_uri` or `relative_path` produce
  `ARTIFACT_LOGICAL_REFERENCE_MISSING` warnings.
- Existing logical URIs are preserved without resolving or probing them.

## AC-07 — secret exclusion

Sentinel values stored in resource auth/proxy fields, Run command/env fields,
Project Target env/prepare fields, and storage configuration must not occur in
JSON or human-readable output.

## AC-08 — no side effects

Before and after audit:

- database content hashes or selected row values remain unchanged;
- no attachment is copied, deleted, or rewritten;
- no SSH/storage connection is attempted;
- no Run lifecycle state changes.

## AC-09 — human-readable operator output

Default output states:

- overall readiness;
- database and attachment root;
- Projects, Runs, resources, attachments, machine-bound paths, logical coverage,
  warnings, and blocking issue counts;
- typed findings with concrete next actions;
- an explicit statement that no data was moved and remote availability was not
  checked.

## AC-10 — regression suite

The following must pass:

```bash
go test ./internal/portability ./internal/store ./cmd/aexp
go test ./...
```

Web behavior and experiment semantics are unchanged. No smoke check is reported
as a formal experiment result.
