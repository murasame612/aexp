#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$ROOT/../.." && pwd)"
REMOTE="${ZOTERO_LIGHTRAG_REMOTE:-mu}"
REMOTE_ROOT="${ZOTERO_LIGHTRAG_REMOTE_ROOT:-/home/murasame/services/zotero-lightrag-v1}"
REMOTE_ADDR="${ZOTERO_LIGHTRAG_REMOTE_ADDR:-100.90.101.9}"
TOKEN_FILE="${ZOTERO_LIGHTRAG_TOKEN_FILE:-$REPO_ROOT/.tmp/zotero-lightrag.token}"

ensure_token() {
  if [[ ! -s "$TOKEN_FILE" ]]; then
    mkdir -p "$(dirname "$TOKEN_FILE")"
    umask 077
    python3 -c 'import secrets; print(secrets.token_urlsafe(32))' >"$TOKEN_FILE"
  fi
}

sync_app() {
  ssh "$REMOTE" "mkdir -p '$REMOTE_ROOT/app' '$REMOTE_ROOT/state/corpora' '$REMOTE_ROOT/state/workspaces' '$REMOTE_ROOT/models' ~/.config/systemd/user"
  rsync -a \
    "$ROOT/gateway.py" "$ROOT/index_paperqa_revision.py" \
    "$ROOT/model_service.py" "$ROOT/paperqa_backend.py" "$ROOT/paperqa_service.py" "$ROOT/validate_revision.py" \
    "$ROOT/requirements-paperqa.txt" \
    "$REMOTE:$REMOTE_ROOT/app/"
  rsync -a \
    "$ROOT/systemd/zotero-lightrag-model.service" \
    "$ROOT/systemd/zotero-paperqa.service" \
    "$ROOT/systemd/zotero-lightrag-gateway.service" \
    "$REMOTE:~/.config/systemd/user/"
}

write_env() {
  ensure_token
  if [[ ! -f "$REPO_ROOT/.env" ]]; then
    echo "missing $REPO_ROOT/.env" >&2
    exit 1
  fi
  set -a
  # shellcheck disable=SC1091
  source "$REPO_ROOT/.env"
  set +a
  local relay="${OPENAI_BASE_URL:-${URL:-}}"
  local relay_key="${OPENAI_API_KEY:-${Key:-}}"
  relay="${relay%/}"
  [[ "$relay" == */v1 ]] || relay="$relay/v1"
  if [[ -z "$relay" || -z "$relay_key" ]]; then
    echo "relay URL/key missing" >&2
    exit 1
  fi
  local paperqa_key
  paperqa_key="$(python3 -c 'import secrets; print(secrets.token_urlsafe(32))')"
  {
    printf 'BGE_EMBEDDING_REVISION=5617a9f61b028005a4858fdac845db406aefb181\n'
    printf 'BGE_RERANK_REVISION=953dc6f6f85a1b2dbfca4c34a2796e7dde08d41e\n'
    printf 'BGE_DEVICE=cuda\nHF_HOME=%s/models\nHF_ENDPOINT=https://hf-mirror.com\nHF_HUB_DISABLE_XET=1\n' "$REMOTE_ROOT"
    printf 'ZOTERO_LIGHTRAG_TOKEN=%s\n' "$(<"$TOKEN_FILE")"
    printf 'ZOTERO_LIGHTRAG_STATE_ROOT=%s/state\n' "$REMOTE_ROOT"
    printf 'PAPERQA_API_KEY=%s\nPAPERQA_UPSTREAM=http://127.0.0.1:9631\n' "$paperqa_key"
    printf 'PAPERQA_STATE_ROOT=%s/state\nZOTERO_RAG_PRIMARY_BACKEND=paperqa2\n' "$REMOTE_ROOT"
    printf 'PAPERQA_LLM_MODEL=gpt-5.6-luna\nPAPERQA_LLM_BASE_URL=%s\nPAPERQA_LLM_API_KEY=%s\n' "$relay" "$relay_key"
    printf 'PAPERQA_EMBEDDING_MODE=hybrid\nPAPERQA_EMBEDDING_MODEL=researchos-bge-m3\nPAPERQA_EMBEDDING_DIM=1024\n'
    printf 'PAPERQA_EMBEDDING_BASE_URL=http://127.0.0.1:8001/v1\nPAPERQA_EMBEDDING_API_KEY=local-only\nPAPERQA_SPARSE_DIM=1024\n'
  } | ssh "$REMOTE" "umask 077; install -m 600 /dev/stdin '$REMOTE_ROOT/service.env'"
}

install_remote() {
  sync_app
  write_env
  ssh "$REMOTE" bash -s -- "$REMOTE_ROOT" <<'REMOTE_SCRIPT'
set -euo pipefail
root="$1"
mkdir -p "$root/.tools" "$root/state"
if [[ ! -x "$root/.tools/uv" ]]; then
  curl -LsSf https://astral.sh/uv/install.sh | env UV_INSTALL_DIR="$root/.tools" sh
fi
if [[ ! -x "$root/.venv-model/bin/python" ]]; then
  if [[ -x "$root/.venv/bin/python" ]] && "$root/.venv/bin/python" -c 'import torch, transformers, sentence_transformers, fastapi, uvicorn' >/dev/null 2>&1; then
    # One-time migration for the existing mu deployment. The old environment
    # remains readable, but only the model service is allowed to use it.
    ln -s .venv "$root/.venv-model"
  else
    "$root/.tools/uv" venv --python 3.12 "$root/.venv-model"
  fi
fi
if [[ ! -x "$root/.venv-paperqa/bin/python" ]]; then
  "$root/.tools/uv" venv --python 3.12 "$root/.venv-paperqa"
fi
"$root/.tools/uv" pip install --python "$root/.venv-model/bin/python" \
  'torch==2.13.0' 'sentence-transformers==5.6.1' 'transformers==5.14.1' \
  'fastapi==0.141.1' 'uvicorn==0.52.1' 'huggingface-hub==1.26.0'
"$root/.tools/uv" pip install --python "$root/.venv-paperqa/bin/python" -r "$root/app/requirements-paperqa.txt"
REMOTE_SCRIPT
  ssh "$REMOTE" bash -s <<'REMOTE_SCRIPT'
set -euo pipefail
systemctl --user daemon-reload
systemctl --user disable --now zotero-lightrag-official.service >/dev/null 2>&1 || true
systemctl --user enable zotero-lightrag-model.service zotero-paperqa.service zotero-lightrag-gateway.service >/dev/null
systemctl --user restart zotero-lightrag-model.service zotero-paperqa.service
for _ in $(seq 1 60); do
  if curl -fsS --max-time 2 -H "X-API-Key: $(sed -n 's/^PAPERQA_API_KEY=//p' /home/murasame/services/zotero-lightrag-v1/service.env)" http://127.0.0.1:9631/health >/dev/null; then
    systemctl --user restart zotero-lightrag-gateway.service
    exit 0
  fi
  sleep 1
done
echo "PaperQA2 did not become ready after environment rotation" >&2
exit 1
REMOTE_SCRIPT
}

download_models() {
  ssh "$REMOTE" "set -a; source '$REMOTE_ROOT/service.env'; set +a; '$REMOTE_ROOT/.venv-model/bin/python' -" <<'PY'
import os
from huggingface_hub import snapshot_download
for repo, revision in [
    ("BAAI/bge-m3", os.environ["BGE_EMBEDDING_REVISION"]),
    ("BAAI/bge-reranker-v2-m3", os.environ["BGE_RERANK_REVISION"]),
]:
    print(
        repo,
        snapshot_download(
            repo_id=repo,
            revision=revision,
            allow_patterns=["*.json", "*.model", "*.bin", "*.safetensors", "*.pt", "1_Pooling/*"],
        ),
        flush=True,
    )
PY
}

publish_revision() {
  local revision="${1:?revision directory required}"
  python3 "$ROOT/validate_revision.py" "$revision"
  local name
  name="$(basename "$revision")"
  ssh "$REMOTE" "mkdir -p '$REMOTE_ROOT/state/corpora/$name'"
  rsync -a "$revision/" "$REMOTE:$REMOTE_ROOT/state/corpora/$name/"
  ssh "$REMOTE" "'$REMOTE_ROOT/.venv-paperqa/bin/python' '$REMOTE_ROOT/app/validate_revision.py' '$REMOTE_ROOT/state/corpora/$name'"
}

start_services() {
  ssh "$REMOTE" "systemctl --user restart zotero-lightrag-model.service"
  echo "model service is loading pinned weights; inspect with: $0 status"
}

index_paperqa_revision() {
  local revision="${1:?corpus revision name required}"
  local embedding_mode="${2:-hybrid}"
  ssh "$REMOTE" bash -s -- "$REMOTE_ROOT" "$revision" "$embedding_mode" <<'REMOTE_SCRIPT'
set -euo pipefail
root="$1"
revision="$2"
embedding_mode="$3"
corpus="$root/state/corpora/$revision"
workspace="$root/state/paperqa/$revision-$embedding_mode"
staging="$root/state/paperqa/.incoming-$revision-$embedding_mode-$$"
[[ -f "$corpus/manifest.json" ]] || { echo "unknown corpus revision: $revision" >&2; exit 2; }
rm -rf "$staging"
mkdir -p "$staging" "$(dirname "$workspace")"
set -a
source "$root/service.env"
set +a
"$root/.venv-paperqa/bin/python" "$root/app/index_paperqa_revision.py" \
  "$corpus" --output "$staging" --embedding-mode "$embedding_mode"
if [[ -d "$workspace" ]]; then
  rm -rf "$workspace"
fi
mv "$staging" "$workspace"
python3 - "$root/state/paperqa-active.json" "$revision" "$embedding_mode" "paperqa/$revision-$embedding_mode" "$workspace/index-result.json" <<'PY'
import json, os, pathlib, sys, tempfile
target = pathlib.Path(sys.argv[1])
result = json.loads(pathlib.Path(sys.argv[5]).read_text())
payload = {
    "status": "indexed",
    "backend": "paperqa2",
    "corpus_revision": sys.argv[2],
    "embedding_mode": sys.argv[3],
    "workspace": sys.argv[4],
    "paperqa_version": result["paperqa_version"],
    "documents": result["documents"],
    "chunks": result["chunks"],
}

target.parent.mkdir(parents=True, exist_ok=True)
fd, temporary = tempfile.mkstemp(prefix=".paperqa-active-", dir=target.parent)
try:
    with os.fdopen(fd, "w") as handle:
        json.dump(payload, handle, ensure_ascii=False, sort_keys=True, indent=2)
        handle.write("\n")
        handle.flush()
        os.fsync(handle.fileno())
    os.replace(temporary, target)
finally:
    if os.path.exists(temporary):
        os.unlink(temporary)
PY
systemctl --user restart zotero-paperqa.service
REMOTE_SCRIPT
}

activate_paperqa_revision() {
  local revision="${1:?corpus revision name required}"
  ssh "$REMOTE" bash -s -- "$REMOTE_ROOT" "$revision" <<'REMOTE_SCRIPT'
set -euo pipefail
root="$1"
revision="$2"
[[ -f "$root/state/corpora/$revision/manifest.json" ]] || { echo "unknown corpus revision: $revision" >&2; exit 2; }
[[ -f "$root/state/paperqa-active.json" ]] || { echo "PaperQA revision has not been indexed" >&2; exit 2; }
paperqa_revision="$(python3 -c 'import json; print(json.load(open("'$root'/state/paperqa-active.json"))["corpus_revision"])')"
[[ "$paperqa_revision" == "$revision" ]] || { echo "PaperQA index belongs to $paperqa_revision, not $revision" >&2; exit 2; }
python3 - "$root/state/active.json" "$revision" <<'PY'
import json, os, pathlib, sys, tempfile
target = pathlib.Path(sys.argv[1])
payload = {
    "status": "indexed",
    "freshness": "fresh",
    "corpus_revision": sys.argv[2],
    "profile": "paperqa2",
    "warnings": [],
}
fd, temporary = tempfile.mkstemp(prefix=".active-", dir=target.parent)
try:
    with os.fdopen(fd, "w") as handle:
        json.dump(payload, handle, ensure_ascii=False, sort_keys=True, indent=2)
        handle.write("\n")
        handle.flush()
        os.fsync(handle.fileno())
    os.replace(temporary, target)
finally:
    if os.path.exists(temporary):
        os.unlink(temporary)
PY
python3 - "$root/service.env" <<'PY'
import os, pathlib, sys, tempfile
target = pathlib.Path(sys.argv[1])
lines = target.read_text().splitlines()
updated = []
found = False
for line in lines:
    if line.startswith("ZOTERO_RAG_PRIMARY_BACKEND="):
        updated.append("ZOTERO_RAG_PRIMARY_BACKEND=paperqa2")
        found = True
    else:
        updated.append(line)
if not found:
    updated.append("ZOTERO_RAG_PRIMARY_BACKEND=paperqa2")
fd, temporary = tempfile.mkstemp(prefix=".service-env-", dir=target.parent)
try:
    with os.fdopen(fd, "w") as handle:
        handle.write("\n".join(updated) + "\n")
        handle.flush()
        os.fsync(handle.fileno())
    os.chmod(temporary, 0o600)
    os.replace(temporary, target)
finally:
    if os.path.exists(temporary):
        os.unlink(temporary)
PY
systemctl --user disable --now zotero-lightrag-official.service >/dev/null 2>&1 || true
systemctl --user restart zotero-paperqa.service zotero-lightrag-gateway.service
REMOTE_SCRIPT
}

status() {
  ensure_token
  ssh "$REMOTE" "systemctl --user --no-pager --full status zotero-lightrag-model.service zotero-paperqa.service zotero-lightrag-gateway.service 2>/dev/null | sed -n '1,160p'" || true
  curl -fsS --max-time 5 -H "Authorization: Bearer $(<"$TOKEN_FILE")" "http://$REMOTE_ADDR:8766/health" || true
  printf '\n'
}

case "${1:-}" in
  install) install_remote ;;
  download-models) download_models ;;
  publish) publish_revision "${2:-}" ;;
  start) start_services ;;
  status) status ;;
  paperqa-index) index_paperqa_revision "${2:-}" "${3:-hybrid}" ;;
  paperqa-activate) activate_paperqa_revision "${2:-}" ;;
  *) echo "usage: $0 {install|download-models|publish REVISION|start|paperqa-index CORPUS_REVISION [hybrid|sparse]|paperqa-activate CORPUS_REVISION|status}" >&2; exit 2 ;;
esac
