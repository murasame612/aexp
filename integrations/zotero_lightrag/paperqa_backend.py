#!/usr/bin/env python3
"""PaperQA2 adapter for immutable ResearchOS Zotero corpus revisions.

The adapter deliberately uses PaperQA's manual ``Docs`` API.  The immutable
corpus already owns parsing, chunk identity, and Zotero metadata; asking
PaperQA to parse Zotero again would create a second, conflicting source of
truth.  Each PaperQA ``Text`` therefore carries the frozen ``file_source`` so
query evidence can be resolved by the provenance gateway.
"""

from __future__ import annotations

import asyncio
import hashlib
import json
import os
import pickle
import tempfile
import threading
from pathlib import Path
from typing import Any

from paperqa import (
    Doc,
    Docs,
    HybridEmbeddingModel,
    LiteLLMEmbeddingModel,
    Settings,
    SparseEmbeddingModel,
    Text,
)
from paperqa.version import __version__ as paperqa_version

try:
    from .validate_revision import validate
except ImportError:  # Direct script execution on the deployed host.
    from validate_revision import validate


INDEX_SCHEMA = "researchos-paperqa-index/v1"


def classify_answerability(session: Any, references: list[dict[str, Any]]) -> str:
    """Classify answerability without treating retrieval as entailment.

    PaperQA may return contexts even when answer generation did not establish a
    supported answer.  A retrieved citation is therefore only a candidate
    source.  ``supported`` is reserved for PaperQA's explicit successful-answer
    signal; an answer with candidate sources but no positive signal is partial.
    """

    if references and getattr(session, "has_successful_answer", None) is True:
        return "supported"
    if references and str(getattr(session, "answer", "") or "").strip():
        return "partial"
    return "unsupported"


def atomic_write(path: Path, payload: bytes) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    descriptor, temporary = tempfile.mkstemp(prefix=f".{path.name}-", dir=path.parent)
    try:
        with os.fdopen(descriptor, "wb") as handle:
            handle.write(payload)
            handle.flush()
            os.fsync(handle.fileno())
        os.replace(temporary, path)
    finally:
        if os.path.exists(temporary):
            os.unlink(temporary)


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for block in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


def embedding_model(mode: str | None = None):
    selected = mode or os.getenv("PAPERQA_EMBEDDING_MODE", "hybrid")
    sparse_dimensions = int(os.getenv("PAPERQA_SPARSE_DIM", "1024"))
    sparse = SparseEmbeddingModel(ndim=sparse_dimensions)
    if selected == "sparse":
        return sparse
    if selected != "hybrid":
        raise ValueError(f"unsupported PaperQA embedding mode: {selected}")

    alias = os.getenv("PAPERQA_EMBEDDING_MODEL", "researchos-bge-m3")
    api_base = os.getenv("PAPERQA_EMBEDDING_BASE_URL", "http://127.0.0.1:8001/v1")
    api_key = os.getenv("PAPERQA_EMBEDDING_API_KEY", "local-only")
    dense = LiteLLMEmbeddingModel(
        name=alias,
        # The private OpenAI-compatible BGE endpoint has a fixed 1024-D
        # output.  Leaving ``ndim`` unset prevents LiteLLM from sending the
        # OpenAI-only ``dimensions`` request parameter to a custom model.
        ndim=None,
        config={
            "batch_size": int(os.getenv("PAPERQA_EMBEDDING_BATCH_SIZE", "16")),
            "model_list": [
                {
                    "model_name": alias,
                    "litellm_params": {
                        "model": f"openai/{alias}",
                        "api_base": api_base,
                        "api_key": api_key,
                    },
                }
            ],
        },
    )
    return HybridEmbeddingModel(name=f"hybrid-{alias}", models=[dense, sparse])


def load_revision(revision: Path) -> tuple[dict[str, Any], list[dict[str, Any]], dict[str, dict[str, Any]]]:
    verified = validate(revision)
    rows = [json.loads(line) for line in (revision / "documents.jsonl").read_text().splitlines() if line]
    provenance = json.loads((revision / "provenance.json").read_text())
    for row in rows:
        source = row.get("file_source")
        if not source or source not in provenance:
            raise ValueError(f"document has no frozen provenance: {source!r}")
        if provenance[source].get("chunk_sha256") != row.get("sha256"):
            raise ValueError(f"document/provenance hash mismatch: {source}")
    return verified, rows, provenance


def citation_for(source: dict[str, Any]) -> str:
    title = str(source.get("title") or "Untitled Zotero item").strip()
    creators = [str(value).strip() for value in source.get("creators") or [] if str(value).strip()]
    authors = ", ".join(creators[:4])
    if len(creators) > 4:
        authors += ", et al."
    return f"{authors}. {title}." if authors else f"{title}."


async def build_docs(
    rows: list[dict[str, Any]],
    provenance: dict[str, dict[str, Any]],
    *,
    embedding_mode: str | None = None,
) -> Docs:
    grouped: dict[str, list[dict[str, Any]]] = {}
    for row in sorted(rows, key=lambda value: value["file_source"]):
        grouped.setdefault(provenance[row["file_source"]]["zotero_item_key"], []).append(row)

    docs = Docs()
    model = embedding_model(embedding_mode)
    for item_key in sorted(grouped):
        paper_rows = grouped[item_key]
        first_source = provenance[paper_rows[0]["file_source"]]
        content_hash = hashlib.sha256(
            "\n".join(sorted(str(row["sha256"]) for row in paper_rows)).encode()
        ).hexdigest()
        doc = Doc(
            docname=item_key,
            dockey=item_key,
            citation=citation_for(first_source),
            content_hash=content_hash,
        )
        texts = []
        for row in paper_rows:
            source = provenance[row["file_source"]]
            page = source.get("page")
            label = f"{item_key} page {page}" if page is not None else f"{item_key} annotation"
            texts.append(
                Text(
                    text=row["text"],
                    name=label,
                    doc=doc,
                    file_source=row["file_source"],
                    zotero_item_key=item_key,
                    attachment_key=source.get("attachment_key"),
                    page=page,
                    page_label=source.get("page_label"),
                    kind=source.get("kind"),
                    chunk_sha256=source.get("chunk_sha256"),
                )
            )
        added = await docs.aadd_texts(texts, doc, embedding_model=model)
        if not added:
            raise ValueError(f"PaperQA rejected Zotero item {item_key}")
    return docs


def build_index(revision: Path, output: Path, *, embedding_mode: str | None = None) -> dict[str, Any]:
    verified, rows, provenance = load_revision(revision)
    docs = asyncio.run(build_docs(rows, provenance, embedding_mode=embedding_mode))
    serialized = pickle.dumps(docs, protocol=pickle.HIGHEST_PROTOCOL)
    docs_path = output / "docs.pkl"
    atomic_write(docs_path, serialized)
    result = {
        "schema": INDEX_SCHEMA,
        "status": "indexed",
        "backend": "paperqa2",
        "paperqa_version": paperqa_version,
        "corpus_revision": revision.name,
        "corpus_manifest_sha256": verified["manifest_sha256"],
        "documents": len(docs.docs),
        "chunks": len(docs.texts),
        "embedding_mode": embedding_mode or os.getenv("PAPERQA_EMBEDDING_MODE", "hybrid"),
        "docs_pickle_sha256": sha256_file(docs_path),
    }
    atomic_write(
        output / "index-result.json",
        (json.dumps(result, ensure_ascii=False, sort_keys=True, indent=2) + "\n").encode(),
    )
    return result


def llm_config() -> tuple[str, dict[str, Any]]:
    alias = os.getenv("PAPERQA_LLM_MODEL", "gpt-5.6-luna")
    api_base = os.environ["PAPERQA_LLM_BASE_URL"].rstrip("/")
    api_key = os.environ["PAPERQA_LLM_API_KEY"]
    return alias, {
        "model_list": [
            {
                "model_name": alias,
                "litellm_params": {
                    "model": f"openai/{alias}",
                    "api_base": api_base,
                    "api_key": api_key,
                    "temperature": 0,
                },
            }
        ]
    }


class PaperQARuntime:
    """Revision-aware, serialized PaperQA query runtime."""

    def __init__(self, state_root: Path):
        self.state_root = state_root
        self._revision: str | None = None
        self._docs: Docs | None = None
        self._embedding_model = None
        self._lock = threading.Lock()

    def active(self) -> dict[str, Any]:
        path = self.state_root / "paperqa-active.json"
        if not path.is_file():
            return {"status": "installed", "backend": "paperqa2"}
        return json.loads(path.read_text())

    def _load(self, expected_revision: str) -> tuple[Docs, dict[str, Any]]:
        active = self.active()
        if active.get("status") == "indexed" and active.get("corpus_revision") == expected_revision:
            workspace = self.state_root / str(active["workspace"])
        else:
            candidates = sorted(
                (self.state_root / "paperqa").glob(f"{expected_revision}-*/index-result.json"),
                key=lambda path: ("-hybrid/" not in str(path), str(path)),
            )
            if not candidates:
                raise RuntimeError("PAPERQA_REVISION_NOT_INDEXED")
            workspace = candidates[0].parent
        result = json.loads((workspace / "index-result.json").read_text())
        if result.get("status") != "indexed" or result.get("corpus_revision") != expected_revision:
            raise RuntimeError("PAPERQA_REVISION_MISMATCH")
        docs_path = workspace / "docs.pkl"
        if self._revision != expected_revision or self._docs is None:
            if sha256_file(docs_path) != result["docs_pickle_sha256"]:
                raise RuntimeError("PAPERQA_INDEX_HASH_MISMATCH")
            self._docs = pickle.loads(docs_path.read_bytes())
            self._revision = expected_revision
            self._embedding_model = embedding_model(result["embedding_mode"])
        return self._docs, result

    async def _aquery(self, docs: Docs, query: str, body: dict[str, Any]):
        alias, config = llm_config()
        settings = Settings(
            llm=alias,
            summary_llm=alias,
            llm_config=config,
            summary_llm_config=config,
            embedding=self._embedding_model.name,
            # GPT-5-compatible APIs expose a fixed temperature of 1. PaperQA
            # otherwise warns and overrides 0 internally on every request.
            temperature=1,
            answer={
                "evidence_k": min(max(int(body.get("evidence_k", 10)), 3), 20),
                "answer_max_sources": min(max(int(body.get("answer_max_sources", 6)), 2), 12),
                "evidence_relevance_score_cutoff": 1,
            },
        )
        return await docs.aquery(query, settings=settings, embedding_model=self._embedding_model)

    def query(self, body: dict[str, Any]) -> dict[str, Any]:
        query = str(body.get("query", "")).strip()
        expected_revision = str(body.get("corpus_revision", "")).strip()
        with self._lock:
            docs, result = self._load(expected_revision)
            session = asyncio.run(self._aquery(docs, query, body))
        references = []
        for context in session.contexts:
            source = getattr(context.text, "file_source", None)
            if not source:
                continue
            references.append(
                {
                    "reference_id": context.id,
                    "file_path": source,
                    "content": [context.context],
                    "score": context.score,
                }
            )
        answerability = classify_answerability(session, references)
        return {
            "response": session.answer,
            "answerability": answerability,
            "references": references,
            "retrieval_mode": "paperqa2-direct",
            "paperqa_version": result["paperqa_version"],
            "cost": session.cost,
        }
