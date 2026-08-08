#!/usr/bin/env bash
set -euo pipefail

revision="${1:?corpus revision name required}"
profile="${2:-shadow}"
if [[ "$profile" != "shadow" && "$profile" != "full" ]]; then
  echo "profile must be shadow or full" >&2
  exit 2
fi
root="${ZOTERO_LIGHTRAG_ROOT:-/home/murasame/services/zotero-lightrag-v1}"
corpus="$root/state/corpora/$revision"
workspace="$root/state/workspaces/$revision-$profile"
staging="$root/state/workspaces/.incoming-$revision-$profile-$$"
superseded="$root/state/workspaces/.superseded-$revision-$profile-$$"
active="$root/state/workspaces/active"
previous="$(readlink -f "$active" 2>/dev/null || true)"

if [[ ! -f "$corpus/manifest.json" ]]; then
  echo "unknown corpus revision: $revision" >&2
  exit 2
fi

wait_ready() {
  for _ in $(seq 1 120); do
    if curl -fsS --max-time 2 -H "X-API-Key: $LIGHTRAG_API_KEY" http://127.0.0.1:9621/health >/dev/null; then
      return
    fi
    sleep 1
  done
  curl -fsS --max-time 5 -H "X-API-Key: $LIGHTRAG_API_KEY" http://127.0.0.1:9621/health >/dev/null
}

write_active() {
  python3 - "$root/state/active.json" "$revision" "$corpus/manifest.json" "$workspace/index-result.json" <<'PY'
import json
import os
import pathlib
import sys
import tempfile

target = pathlib.Path(sys.argv[1])
revision = sys.argv[2]
manifest = json.loads(pathlib.Path(sys.argv[3]).read_text())
index_result = json.loads(pathlib.Path(sys.argv[4]).read_text())
warnings = list(manifest.get("warnings", []))
if index_result["profile"] == "shadow":
    warnings.append(
        {
            "code": "PARTIAL_INDEX_COVERAGE",
            "selected_source_documents": index_result["selected_source_documents"],
            "source_documents": index_result["source_documents"],
        }
    )
payload = {
    "status": "shadow",
    "freshness": "fresh",
    "corpus_revision": revision,
    "manifest_sha256": "sha256:" + manifest["manifest_sha256"],
    "library_version": manifest["source"]["library_version"],
    "warnings": warnings,
    "index_profile": index_result["profile"],
    "source_documents": index_result["source_documents"],
    "selected_source_documents": index_result["selected_source_documents"],
    "index_documents": index_result["index_documents"],
    "duplicates_omitted": index_result["duplicates_omitted"],
    "pdf_chunks_per_paper": index_result.get("pdf_chunks_per_paper"),
}
target.parent.mkdir(parents=True, exist_ok=True)
fd, temp = tempfile.mkstemp(prefix=".active-", dir=target.parent)
try:
    with os.fdopen(fd, "w") as handle:
        json.dump(payload, handle, ensure_ascii=False, sort_keys=True, indent=2)
        handle.write("\n")
        handle.flush()
        os.fsync(handle.fileno())
    os.replace(temp, target)
finally:
    if os.path.exists(temp):
        os.unlink(temp)
PY
}

valid_existing=false
if [[ -f "$workspace/index-result.json" ]]; then
  if python3 - "$workspace/index-result.json" "$revision" "$profile" <<'PY'
import json, sys
result = json.load(open(sys.argv[1]))
raise SystemExit(0 if result.get("status") == "indexed" and result.get("corpus_revision") == sys.argv[2] and result.get("profile") == sys.argv[3] else 1)
PY
  then
    valid_existing=true
  fi
fi

if [[ "$valid_existing" == true ]]; then
  systemctl --user stop zotero-lightrag-official.service
  ln -sfn "$workspace" "$active.next"
  mv -Tf "$active.next" "$active"
  systemctl --user start zotero-lightrag-official.service
  wait_ready
  write_active
  systemctl --user restart zotero-lightrag-gateway.service
  printf 'activated existing %s (%s)\n' "$revision" "$profile"
  exit 0
fi

rm -rf "$staging" "$superseded"
mkdir -p "$staging"
ln -sfn "$staging" "$active.next"
mv -Tf "$active.next" "$active"

rollback() {
  systemctl --user stop zotero-lightrag-official.service || true
  if [[ -n "$previous" ]]; then
    ln -sfn "$previous" "$active.next"
    mv -Tf "$active.next" "$active"
  fi
  if [[ -d "$superseded" ]]; then
    rm -rf "$workspace"
    mv "$superseded" "$workspace"
  fi
  rm -rf "$staging"
  systemctl --user start zotero-lightrag-official.service || true
}
trap rollback ERR INT TERM

systemctl --user restart zotero-lightrag-official.service
wait_ready

"$root/.venv/bin/python" "$root/app/index_revision.py" "$corpus" --profile "$profile" --result "$staging/index-result.json"

systemctl --user stop zotero-lightrag-official.service
if [[ -d "$workspace" ]]; then
  mv "$workspace" "$superseded"
fi
mv "$staging" "$workspace"
ln -sfn "$workspace" "$active.next"
mv -Tf "$active.next" "$active"
systemctl --user start zotero-lightrag-official.service
wait_ready
write_active

systemctl --user restart zotero-lightrag-gateway.service
rm -rf "$superseded"
trap - ERR INT TERM
printf 'activated %s (%s)\n' "$revision" "$profile"
