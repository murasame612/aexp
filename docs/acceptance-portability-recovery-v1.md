# Acceptance — aexp Portability Recovery v1

Date: 2026-08-09

## AC-01 — source safety

- Export uses an existing source database opened read-only.
- A consistent snapshot contains rows committed before the snapshot and does
  not contain rows added afterwards.
- Snapshot output is owner-readable/writable only.
- Missing source files block export rather than producing a partial bundle.

## AC-02 — complete declared bundle

- Manifest schema is `aexp-portability-bundle-v1`.
- The manifest declares exactly one database and every registered attachment.
- Every payload entry has a size and SHA-256 checksum.
- Project trees, datasets, and remote artifacts are explicitly excluded.

## AC-03 — archive safety

- Absolute paths and traversal members are rejected.
- Non-regular archive members are rejected.
- Duplicate archive members are rejected.
- Files absent from the manifest are rejected.
- Duplicate attachment identities are rejected.
- Size or checksum mismatches are rejected.

## AC-04 — isolated import

- Import requires `--to` unless `--dry-run` is used.
- Existing destinations are rejected and never merged.
- The destination is published only after all validation succeeds.
- `~/.aexp` is never replaced automatically.

## AC-05 — path mapping

- Mappings require absolute `OLD=NEW` values.
- Duplicate mapping sources and identity mappings are rejected.
- More-specific prefixes take precedence.
- Only exact paths and path-segment descendants are rewritten.
- Opaque JSON, commands, and narrative text remain unchanged.

## AC-06 — resource rebind boundary

- Resource IDs, names, endpoints, and roots remain available.
- SSH auth references, SOCKS/proxy configuration, and cached SSH status are
  cleared in the copied database.
- No SSH or storage connection is attempted during export, validation, or
  import.

## AC-07 — attachment recovery

- Registered attachments are copied and checksum-verified.
- Imported attachment rows point at the isolated restored files.
- Missing attachments block export.

## AC-08 — restored database validation

- The restored database passes SQLite `integrity_check`.
- Schema compatibility is evaluated on the copy, never on the source.
- The portability audit can read the restored database.
- Projects, historical Runs, and Run Mark attachments remain queryable.

## AC-09 — no automatic execution

- No service is started.
- No resource doctor is run.
- No Run is resumed, cancelled, or otherwise changed.
- Smoke checks are not represented as experimental results.

## AC-10 — regression suite

```bash
go test ./cmd/aexp ./internal/portability ./internal/store
go test ./...
```
