# Zotero → PaperQA2 v1

This directory contains the narrow integration described by
[`docs/prd-aexp-literature-retrieval-v1.md`](../../docs/prd-aexp-literature-retrieval-v1.md).

The boundary is intentional:

- Zotero on the Mac is the only writable literature source.
- `publish_corpus.py` freezes one immutable corpus revision without reading
  `zotero.sqlite`.
- PaperQA2 `2026.3.18` is the primary literature QA backend on `mu`.
- LightRAG is frozen and is not installed or queried by the default path.
- `gateway.py` gives PaperQA2 one revision-bound provenance contract.
- Nothing in this integration can write an accepted ResearchOS Evidence Map.

## Publish a corpus revision

Zotero must be open on the Mac so its Local API is available.

```bash
python3 integrations/zotero_lightrag/publish_corpus.py \
  --collection 3VJI47K2 \
  --output-root .tmp/zotero-corpus
```

The command writes into a temporary directory, checks that the Zotero library
version did not change during collection, hashes every available PDF and output
file, and then atomically publishes `corpus_<hash-prefix>/`. Repeating the same
input is idempotent; published revisions are never overwritten.

The selected collection is a project boundary, not a sampling hint. Publishing
recursively includes the root collection and every descendant collection,
deduplicates papers by Zotero item key, preserves their collection membership
in provenance, and keeps the complete extracted full text. The defaults are
unbounded. Use `--max-papers` or `--max-chunks-per-paper` only for an explicitly
labelled development fixture, never for an accepted corpus.

Every selected Zotero paper contributes a `metadata` document containing its
title and available abstract/bibliographic fields. PDF text and annotations are
additional evidence. This keeps papers with unavailable local attachments
discoverable without pretending that metadata is full-text evidence; callers
can distinguish the source through provenance `kind`.

## Validate a revision

```bash
python3 integrations/zotero_lightrag/validate_revision.py \
  .tmp/zotero-corpus/corpus_<hash-prefix>
```

## Deploy the services

`deploy_mu.sh` installs into `/home/murasame/services/zotero-lightrag-v1` and
uses only a project-local virtual environment. It never installs Zotero on
Linux and never modifies the system Python environment.

```bash
integrations/zotero_lightrag/deploy_mu.sh install
integrations/zotero_lightrag/deploy_mu.sh publish \
  .tmp/zotero-corpus/corpus_<hash-prefix>
integrations/zotero_lightrag/deploy_mu.sh paperqa-index corpus_<hash-prefix> hybrid
integrations/zotero_lightrag/deploy_mu.sh paperqa-activate corpus_<hash-prefix>
integrations/zotero_lightrag/deploy_mu.sh status
```

`paperqa-activate` makes the verified PaperQA2 index active. Publishing and
querying never waits for graph extraction.

The provenance gateway is exposed only on mu's private address at port `8766` and requires a
bearer token. A local embedding service is a hard dependency for indexing. The
deployment refuses to silently replace pinned BGE-M3 with another model.

`paperqa-index` consumes every frozen chunk. It uses the already pinned local
BGE-M3 service plus a sparse embedding, and never calls an LLM while indexing.
The resulting `docs.pkl` is a derived, hash-checked index and may be rebuilt at
any time.

After activation, Agents use the provenance gateway rather than either raw
backend:

```bash
python3 integrations/zotero_lightrag/query.py \
  "Which methods handle missing dam displacement monitoring data?"
```

PaperQA2 is the only enabled backend. The gateway never silently falls back to
LightRAG; an old explicit `backend=lightrag` request returns
`LIGHTRAG_NOT_ENABLED`.

## Status meanings

- `installed`: a backend is installed, but no verified index is active.
- `blocked_model_download`: BGE-M3/reranker weights have not been explicitly
  approved and installed.
- `indexed`: PaperQA2 covers the complete immutable corpus revision.
- `accepted`: all criteria in the acceptance document passed.
