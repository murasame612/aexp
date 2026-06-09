# aexp Python ML Example

This is a minimal project profile template for an ML repo. Keep experiment logic in scripts and configs; keep `.aexp.yaml` as the reusable command recipe.

```bash
aexp project init --resource mu --cwd /home/ziwu/project/python-ml --dry-run
aexp project doctor
aexp project sync --dry-run
aexp project run setup
aexp project run train --dry-run
aexp project run train
```

Notes:

- `setup` is tooling only, not experiment evidence.
- `train` is a formal run and can be used as experiment evidence after inspection.
- `sync.profile: code` avoids common local caches and training outputs.
- `.aexpignore` is merged into sync excludes unless `--no-default-excludes` is used.
- The script writes structured metrics to `$AEXP_UI_EVENTS` through `aexp_events`, so the Web UI can render charts without tailing noisy raw logs.
