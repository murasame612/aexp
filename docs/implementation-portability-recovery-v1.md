# Implementation — aexp Portability Recovery v1

Date: 2026-08-09

## Architecture

The recovery implementation extends `internal/portability` with one shared
bundle/restore workflow. CLI `import --dry-run` and `validate` use the same
restore function, so checksum, extraction, mapping, resource-clearing, and
audit rules cannot drift between commands.

The CLI implementation lives in `cmd/aexp/portability_cmd.go` instead of adding
more migration logic to the already large main command file.

## Consistent export

`store.SnapshotSQLite` opens the source using SQLite `mode=ro` and creates a
standalone copy with `VACUUM INTO`. It does not run source migrations, request a
WAL checkpoint, or create a missing source database.

The copied database is then opened independently. Resource auth references,
SOCKS/proxy configuration, and cached reachability state are cleared from this
copy. The source database remains untouched.

Registered attachments are copied individually using their entity identities,
not by recursively archiving an arbitrary home-directory subtree. Every payload
file is hashed before the manifest and tar.gz archive are written.

## Restore and validation

Restore performs these stages:

1. extract only regular files through path-traversal-safe member resolution;
2. decode the exact supported manifest schema;
3. reject duplicate, unsafe, or undeclared members;
4. verify size and SHA-256 for every declared file;
5. open and migrate only the copied database;
6. clear resource bindings again as an idempotent safety step;
7. point attachment rows at restored files;
8. apply explicit path-prefix mappings to known path columns;
9. run SQLite `integrity_check`;
10. run the portability audit over the restored copy;
11. publish the directory atomically only after validation succeeds.

Dry-run validation uses a temporary directory that is removed afterwards.
Normal import stages beside the requested destination and renames the complete
workspace into place. Existing destinations are never merged or overwritten.

## Path mapping coverage

Known path columns cover resources, Projects, Project Targets, Runs, artifacts,
storage targets, path placements, dataset materializations, Run Mark
attachments, and execution cwd records. More-specific mapping prefixes are
applied before broader prefixes.

Opaque JSON, commands, environment values, and free-form evidence text are not
rewritten. Automatic replacement there could corrupt provenance or executable
content.

## Restored layout

```text
<destination>/
  manifest.json
  database/
    aexp.db
  attachments/
    <attachment_id>/
      <filename>
```

This is an isolated recovery workspace. A later cutover command may copy or
adopt it as the active `~/.aexp`, but v1 deliberately does not do so.

## Scoped cleanup

- Portability commands were moved out of `cmd/aexp/main.go` into a dedicated
  command file.
- The duplicate unprefixed `sha256File` implementation was removed; release
  checksum verification now reuses the existing `fileSHA256` helper.
- Import and validate share one restore path instead of duplicating validation
  logic.
