# Zotero PaperQA2 validation — 2026-08-04

> Historical pre-consolidation report. Statements below about a LightRAG
> shadow/fallback describe the test environment at that time. The deployed
> ResearchOS v1 path is now PaperQA2-only; see
> `verification-aexp-literature-retrieval-v1-2026-08-04.md`.

## Outcome

PaperQA2 `2026.3.18` is the primary query backend on `mu`. The official
LightRAG service remains available only as an explicit graph shadow or
fallback. Zotero on the Mac remains the sole writable literature source.

Active corpus: `corpus_5f1dd19e8bdc693b2ba9`

- 25 Zotero papers
- 2,384 frozen chunks
- 110 annotation chunks inside the corpus
- hybrid local retrieval: pinned BGE-M3 plus sparse embedding
- PaperQA index SHA-256: `72a9f377c4f15a59b32d18277acd7028a4cad7f9c6406b6aef798c5b124c15a6`

## Real query checks

1. Missing dam displacement data methods: `supported`, 9 evidence records.
   The answer distinguished interpolation/imputation from prediction and every
   evidence record resolved to the active Zotero revision.
2. Exact Zotero highlight about FEM + IPSO + GRU: `supported`, 8 evidence
   records, including annotation and PDF chunk sources.
3. Aspirin migraine trial dosage: `unsupported`, zero evidence, with an
   explicit insufficient-information answer.
4. Default gateway query after cutover: `retrieval_backend=paperqa2`, no
   fallback, 8/8 returned evidence records contained Zotero item, file source,
   and frozen chunk SHA-256.

These are retrieval validation cases, not scientific experiment results and
not accepted ResearchOS evidence claims.
