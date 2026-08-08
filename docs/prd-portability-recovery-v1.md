# PRD — aexp Portability Recovery v1

Date: 2026-08-09

## Target mode

An operator or Agent can move the durable aexp control plane to another
controller without modifying the source database and without claiming that
remote compute or storage has already moved.

The recovery workflow is:

```text
audit -> export -> validate with explicit mappings -> isolated import
      -> resource rebind -> operator cutover
```

The implementation is successful when the new controller can read Projects,
Journal, Evidence, historical Runs, and registered attachments from an isolated
restored workspace. Starting the production service and resuming Runs remain
separate operator decisions.

## User-facing commands

```bash
aexp portability audit --json --strict
aexp portability export --output control-plane.tar.gz
aexp portability validate control-plane.tar.gz \
  --map-path /Users/old/research=/home/new/research
aexp portability import control-plane.tar.gz \
  --to /home/new/aexp-restored \
  --map-path /Users/old/research=/home/new/research
```

## Bundle contents

The v1 bundle contains:

- a transactionally consistent standalone SQLite snapshot;
- every attachment registered by a Run Mark;
- a versioned manifest;
- source audit facts, file sizes, and SHA-256 checksums;
- explicit limitations and recovery boundaries.

The bundle does not contain:

- project working trees;
- remote Run directories;
- dataset bytes;
- remote artifact bytes;
- SSH private keys, proxy commands, or active credential bindings;
- an instruction to start a service or resume a Run.

## Safety requirements

- Export refuses blocking audit findings.
- Export never migrates or checkpoints the source database.
- The archive is written with owner-only permissions.
- Import rejects an existing destination.
- Import rejects unsafe paths, duplicate members, undeclared files, invalid
  manifests, and checksum mismatches.
- Path rewrites require absolute `OLD=NEW` mappings and use path-segment prefix
  matching rather than substring replacement.
- SSH resource identities remain, while controller-specific authentication and
  reachability state are cleared in the restored copy.
- Import never replaces `~/.aexp` automatically.

## Non-goals

- copying remote datasets and artifacts;
- inferring path mappings;
- rewriting paths hidden inside opaque JSON or shell commands;
- testing SSH or storage connectivity during offline validation;
- automatically installing dependencies;
- automatically starting the restored service;
- automatically resuming active Runs.
