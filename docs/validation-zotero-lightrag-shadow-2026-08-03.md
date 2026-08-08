# Zotero → LightRAG Shadow Validation — 2026-08-03

Status: shadow validation passed; full acceptance remains pending

This report records observed deployment facts separately from the binding
acceptance checklist. A passing shadow smoke test is not a scientific result
and is not sufficient to mark the backend `accepted`.

## Frozen source

- Zotero source: macOS Local API, collection `SHUMTSPS` (`damxer投稿`)
- Zotero `library_version`: `16153`
- Corpus revision: `corpus_5f1dd19e8bdc693b2ba9`
- Manifest SHA-256: `5f1dd19e8bdc693b2ba9d686f3c01369b963170ac553899e165ae7d658ea4084`
- Papers: 25
- PDF attachments: 31
- Zotero annotations: 110
- Source documents with page/annotation provenance: 2384
- Warnings: five original attachment files were macOS dataless; their Zotero
  full text is present, but the revision correctly does not claim raw-file SHA-256 for them.

Mac and mu independently validated the same manifest and artifact hashes.

## Pinned runtime

- Official LightRAG `v1.5.5`, commit `22ea2d0cbfa2b7002aa118bd0bf1780a69d489bc`
- LLM `gpt-5.6-luna`, temperature 0, max gleaning 1
- BGE-M3 revision `5617a9f61b028005a4858fdac845db406aefb181`
- bge-reranker-v2-m3 revision `953dc6f6f85a1b2dbfca4c34a2796e7dde08d41e`
- Project-local CPython 3.12 environment on `mu`; no global Python or Linux Zotero installation

## Shadow coverage

The immutable corpus is complete, while the first graph index intentionally
uses the documented `shadow` profile: every Zotero annotation plus a
paper-balanced sample of PDF chunks. The gateway must return
`PARTIAL_INDEX_COVERAGE`; this profile cannot become `accepted`.

- Selected source documents: 209 (all 110 annotations plus four evenly sampled
  PDF chunks per paper)
- Deterministic derived-index documents: 208
- Exact duplicate texts omitted from the derived index: 1
- Final LightRAG status: 208 processed, 0 failed

## Observed checks

- BGE-M3 returned normalized 1024-dimensional embeddings on CUDA.
- The reranker ranked the relevant literature sentence above an unrelated image-classification sentence.
- Model and LightRAG services remained private; the existing prototype on port 8765 was not replaced.
- Interrupted/failed incoming workspaces did not become active.
- The active workspace symlink and `active.json` both identify
  `corpus_5f1dd19e8bdc693b2ba9-shadow`; model, official LightRAG and provenance
  gateway services survived an idempotent activation.
- The gateway listens only on mu's private overlay address. Missing and invalid
  bearer tokens returned HTTP 401; the valid token returned HTTP 200.
- A LightRAG compatibility defect was found during activation: the status
  response includes `all` as a summary field. It was incorrectly counted as
  active work after all 208 documents were processed. The adapter now excludes
  summary fields from lifecycle counts; the completed staging workspace was
  preserved and promoted without re-indexing.

## Real provenance query

The authenticated gateway was asked what the paper *A novel method for
settlement imputation and monitoring of earth-rockfill dams subjected to
large-scale missing data* proposes and how it is evaluated.

- Answerability: `supported`
- Response length: 2536 characters
- Evidence records: 20
- Corpus revision and freshness: current revision, `fresh`
- The first evidence record resolved to Zotero item `822TAAV9`, attachment
  `5HQQXTMN`, page 1 and chunk SHA-256
  `91d12a204f0607568fd5505b6ed7f5cb9f7900935909c47709f3973cbab1f052`.
- The target paper was also retrieved at page 11. Other returned records retained
  their Zotero item, attachment, page, logical source path and chunk hash.
- The response explicitly retained `PARTIAL_INDEX_COVERAGE` and five
  `ATTACHMENT_DATALESS` warnings.

This proves the shadow path from a frozen Mac Zotero revision through the GPU
models and official LightRAG to revision-bound evidence. It is an engineering
validation only, not a retrieval-quality result or scientific experiment.

## Pending before `accepted`

- Freeze the 40-question evaluation set and run BM25/dense/LightRAG quality gates before any `accepted` decision.
- Run full-profile indexing, delete-zero-ghost, rollback, performance and cost accounting acceptance checks.
