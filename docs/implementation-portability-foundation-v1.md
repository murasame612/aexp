# Implementation — aexp Portability Foundation v1

Date: 2026-08-09

## 1. Implementation method

The implementation adds a narrow `internal/portability` service and a thin
Cobra command. The service owns the report contract, path classification,
findings, rendering, and secret-exclusion boundary. The CLI owns only flags,
opening the database, selecting the attachment root, and exit behavior.

This separation keeps the audit reusable by a future API, UI, exporter, and
restore validator without duplicating migration rules in `cmd/aexp/main.go`.

## 2. Read-only database access

`store.OpenSQLiteReadOnly` opens an existing database with SQLite `mode=ro` and
`query_only`. It must not create a missing database, run migrations, reconcile
rows, or update WAL state through application writes.

The normal `store.NewSQLite` behavior remains unchanged for the service and CLI
paths that own schema migration.

## 3. Audit pipeline

The audit performs these stages in order:

1. inspect the database and attachment root;
2. list durable Projects and Project Targets;
3. list resources and storage targets as rebindable identities;
4. list Runs and their artifact records;
5. list datasets, logical roots, and path placements;
6. list Run Marks and attachment records;
7. classify every non-empty path;
8. stat controller-local durable files only;
9. derive typed findings and summary counts;
10. render JSON or deterministic human-readable output.

Remote paths are never passed to local `os.Stat`. Their state is `not_checked`
unless an existing persisted placement observation already describes them.

## 4. Path scopes

The v1 classifier uses four scopes:

- `controller_local`: database, attachments, Project local metadata, sync
  sources, and local git-diff captures;
- `remote_resource`: Run cwd/resolved cwd/run directory, target cwd, remote UI
  event paths, and remote artifact paths;
- `storage_target`: storage roots and dataset storage paths;
- `logical`: logical roots, managed URIs, and logical artifact/dataset sources.

Absolute paths are not automatically errors. They are machine-bound references
that require mapping or rebinding. A missing controller-local durable file is an
error because it cannot be recovered from the database alone.

## 5. Secret boundary

The report may include resource ID, name, type, endpoint, root, and current SSH
status. It must never include:

- `auth_ref`;
- `proxy_command` or SOCKS credentials;
- Run command, args, or environment JSON;
- Project Target environment JSON or prepare commands;
- storage `config_json`;
- API tokens or generated service configuration.

Tests insert sentinel secrets into these fields and assert that neither JSON nor
human-readable output contains them.

## 6. Compatibility method

Legacy absolute-path rows remain readable and are reported rather than rewritten.
New logical-root and path-placement rows are counted as existing portability
coverage. v1 does not backfill logical URIs because automatic inference could
attach the wrong durable identity to historical data.

## 7. Recovery extension

The following recovery operations now consume the same audit types and codes:

```text
aexp portability export
aexp portability import --dry-run --map-path OLD=NEW
aexp portability validate --map-path OLD=NEW
```

Their design and acceptance boundary are documented in
`prd-portability-recovery-v1.md`, `implementation-portability-recovery-v1.md`,
and `acceptance-portability-recovery-v1.md`. Resource rebind and production
cutover remain explicit operations after validation.
