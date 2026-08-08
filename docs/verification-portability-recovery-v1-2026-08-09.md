# Verification — aexp Portability Recovery v1

Date: 2026-08-09

## Automated verification

The following completed successfully:

```text
go test ./...
go vet ./...
git diff --check
```

Focused tests cover:

- read-only SQLite opening and standalone snapshot consistency;
- bundle export with database and registered attachments;
- missing controller files blocking export;
- safe archive extraction and undeclared-member rejection;
- manifest validation and file checksum verification;
- resource binding removal in the copied database;
- exact and prefix path mapping across known path columns;
- attachment path reconstruction after import;
- SQLite integrity checking;
- dry-run validation and isolated non-overwriting import;
- CLI audit, export, validate, strict exit, and mapping validation.

## Current control-plane recovery exercise

A temporary bundle was exported from the current default database, validated,
and then deleted from `/tmp`. No persistent migration artifact was retained.

Observed result:

```text
files: 41
database snapshots: 1
registered attachments: 40
files verified: 41
database integrity: ok
attachment references restored: 40
resource bindings cleared: 7
blocking findings: 0
warnings: 7 resource rebind requirements
validation status: valid_with_findings
```

The exercise did not connect to SSH or storage, copy remote artifacts or
datasets, replace `~/.aexp`, start a service, or resume a Run.

## Acceptance conclusion

Recovery v1 provides the intended export, validation, explicit mapping, and
isolated import foundation. The remaining warnings are expected because SSH
credentials and reachability are deliberately excluded and must be rebound on
the new controller.
