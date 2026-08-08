#!/usr/bin/env python3
"""Private OpenAI-embedding and Cohere-rerank service for pinned BGE models."""

from __future__ import annotations

import os
import threading
from contextlib import asynccontextmanager
from typing import Any

import torch
from fastapi import FastAPI, HTTPException
from pydantic import BaseModel
from sentence_transformers import SentenceTransformer
from transformers import AutoModelForSequenceClassification, AutoTokenizer


EMBEDDING_MODEL = os.getenv("BGE_EMBEDDING_MODEL", "BAAI/bge-m3")
EMBEDDING_REVISION = os.getenv("BGE_EMBEDDING_REVISION", "5617a9f61b028005a4858fdac845db406aefb181")
RERANK_MODEL = os.getenv("BGE_RERANK_MODEL", "BAAI/bge-reranker-v2-m3")
RERANK_REVISION = os.getenv("BGE_RERANK_REVISION", "953dc6f6f85a1b2dbfca4c34a2796e7dde08d41e")
DEVICE = os.getenv("BGE_DEVICE", "cuda" if torch.cuda.is_available() else "cpu")
MAX_RERANK_LENGTH = int(os.getenv("BGE_RERANK_MAX_LENGTH", "2048"))
LOCK = threading.Lock()


class Runtime:
    embedding: SentenceTransformer | None = None
    rerank_tokenizer: Any = None
    reranker: Any = None

    def load(self) -> None:
        dtype = torch.float16 if DEVICE.startswith("cuda") else torch.float32
        self.embedding = SentenceTransformer(
            EMBEDDING_MODEL,
            revision=EMBEDDING_REVISION,
            device=DEVICE,
            model_kwargs={"torch_dtype": dtype},
        )
        self.rerank_tokenizer = AutoTokenizer.from_pretrained(RERANK_MODEL, revision=RERANK_REVISION)
        self.reranker = AutoModelForSequenceClassification.from_pretrained(
            RERANK_MODEL,
            revision=RERANK_REVISION,
            torch_dtype=dtype,
        ).to(DEVICE)
        self.reranker.eval()


runtime = Runtime()


@asynccontextmanager
async def lifespan(_: FastAPI):
    runtime.load()
    yield


app = FastAPI(title="ResearchOS pinned BGE service", version="1.0", lifespan=lifespan)


class EmbeddingRequest(BaseModel):
    input: str | list[str]
    model: str | None = None
    encoding_format: str | None = None


class RerankRequest(BaseModel):
    model: str | None = None
    query: str
    documents: list[str | dict[str, Any]]
    top_n: int | None = None
    return_documents: bool = False


@app.get("/health")
def health() -> dict[str, Any]:
    return {
        "status": "ready" if runtime.embedding is not None and runtime.reranker is not None else "loading",
        "device": DEVICE,
        "embedding": {"model": EMBEDDING_MODEL, "revision": EMBEDDING_REVISION, "dimension": 1024},
        "reranker": {"model": RERANK_MODEL, "revision": RERANK_REVISION},
    }


@app.post("/v1/embeddings")
def embeddings(request: EmbeddingRequest) -> dict[str, Any]:
    if runtime.embedding is None:
        raise HTTPException(status_code=503, detail="embedding model is loading")
    texts = [request.input] if isinstance(request.input, str) else request.input
    if not texts or any(not isinstance(text, str) or not text.strip() for text in texts):
        raise HTTPException(status_code=400, detail="input must contain non-empty strings")
    with LOCK:
        vectors = runtime.embedding.encode(
            texts,
            batch_size=min(16, len(texts)),
            normalize_embeddings=True,
            convert_to_numpy=True,
            show_progress_bar=False,
        )
    return {
        "object": "list",
        "model": EMBEDDING_MODEL,
        "data": [{"object": "embedding", "index": index, "embedding": vector.tolist()} for index, vector in enumerate(vectors)],
        "usage": {"prompt_tokens": 0, "total_tokens": 0},
    }


def document_text(document: str | dict[str, Any]) -> str:
    if isinstance(document, str):
        return document
    return str(document.get("text", ""))


@app.post("/rerank")
def rerank(request: RerankRequest) -> dict[str, Any]:
    if runtime.reranker is None or runtime.rerank_tokenizer is None:
        raise HTTPException(status_code=503, detail="reranker is loading")
    documents = [document_text(document) for document in request.documents]
    pairs = [[request.query, document] for document in documents]
    with LOCK, torch.inference_mode():
        inputs = runtime.rerank_tokenizer(
            pairs,
            padding=True,
            truncation=True,
            max_length=MAX_RERANK_LENGTH,
            return_tensors="pt",
        ).to(DEVICE)
        scores = runtime.reranker(**inputs).logits.view(-1).float().cpu().tolist()
    ranking = sorted(enumerate(scores), key=lambda value: value[1], reverse=True)
    if request.top_n is not None:
        ranking = ranking[: max(0, request.top_n)]
    results = []
    for index, score in ranking:
        item: dict[str, Any] = {"index": index, "relevance_score": score}
        if request.return_documents:
            item["document"] = {"text": documents[index]}
        results.append(item)
    return {"id": "researchos-bge-rerank", "results": results, "meta": {"model": RERANK_MODEL}}
