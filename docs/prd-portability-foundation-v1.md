# PRD — aexp Portability Foundation v1

Status: target mode approved for implementation
Date: 2026-08-09

## 1. Problem

`aexp` currently preserves experiment provenance and control-plane history well,
but a restored database is not yet a restored research workspace. Durable rows
still contain machine-specific paths, local attachments live outside SQLite,
and SSH resources must be rebound on a new controller.

The product must stop treating “the database opened” as equivalent to “the
workspace is recoverable”. Portability must become an explicit, inspectable
state with honest missing-file and rebind reporting.

## 2. Target operating model

The target is a portable control plane with replaceable physical placements:

```text
durable identity and provenance
        │
        ├── logical references: project, run, dataset, artifact, attachment
        │
        └── replaceable bindings
              ├── controller-local paths
              ├── remote resource paths
              ├── storage target roots
              └── SSH/auth bindings
```

After restoring on another controller, Projects, Journal, Evidence, Run history,
and immutable manifests must remain browsable before any remote resource is
reachable. Missing local files and unbound resources must be explicit states,
not silent broken links.

## 3. v1 product surface

Portability Foundation v1 introduces:

```bash
aexp portability audit
aexp portability audit --json
aexp portability audit --strict
```

The audit is offline and read-only:

- opens an existing SQLite database without migrations or writes;
- inventories Projects, Project Targets, Runs, artifacts, datasets, logical
  roots, path placements, storage targets, resources, and Run Mark attachments;
- classifies controller-local, remote-resource, storage, and logical paths;
- checks only controller-local file existence;
- identifies resources that require rebinding on another controller;
- reports logical-reference coverage and machine-bound legacy paths;
- never probes SSH, storage, or remote filesystems;
- never prints auth references, proxy commands, environment JSON, commands, API
  tokens, or storage configuration JSON.

## 4. Report contract

The JSON report uses schema `portability-audit-v1` and contains:

- audit mode and target mode;
- database and attachment-root locations;
- stable summary counts;
- path references with scope and observability state;
- resource rebind requirements without credentials;
- typed findings with severity, code, entity, field, and recommended action;
- a readiness result for creating a future portability bundle.

Severity semantics:

- `error`: a controller-local durable file is missing or the database cannot be
  audited safely;
- `warning`: migration requires mapping/rebinding or a durable record lacks a
  portable logical reference;
- `info`: machine-bound state is present but expected and explicitly classified.

`--strict` exits unsuccessfully only when `error` findings exist. Warnings are
actionable migration work, not evidence that current provenance is invalid.

## 5. Non-goals

v1 does not:

- export or import a bundle;
- rewrite database paths;
- copy attachments or artifacts;
- contact remote resources;
- validate SSH credentials;
- resume, resubmit, or mutate Runs;
- claim that remote artifacts exist based only on database records;
- include secrets in output.

## 6. Follow-on capabilities

Later phases may consume the v1 report contract to implement:

1. consistent SQLite snapshot plus manifest generation;
2. attachment and manifest bundling;
3. import dry-run and schema compatibility checks;
4. path-prefix mapping;
5. resource and credential rebinding;
6. isolated restore validation;
7. explicit cutover.

Those phases must reuse the audit findings rather than establish a second,
incompatible definition of portability.

## 7. Success criteria

Portability Foundation v1 is complete when a user can run one read-only command
against an existing database and answer:

- Which state is portable as-is?
- Which paths are tied to this controller or a remote resource?
- Which local durable files are missing?
- Which resources require rebinding?
- Which records lack logical references needed by a future bundle?
- Can a future bundle be created without first repairing blocking local loss?
