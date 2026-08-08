from __future__ import annotations

import hashlib
import json
import pickle
import tempfile
import unittest
from pathlib import Path
from types import SimpleNamespace

from integrations.zotero_lightrag.paperqa_backend import build_index, classify_answerability
from integrations.zotero_lightrag.publish_corpus import publish


class PaperQAContractTests(unittest.TestCase):
    def test_retrieved_context_without_positive_answer_signal_is_partial(self) -> None:
        session = SimpleNamespace(answer="plausible answer", has_successful_answer=None)
        self.assertEqual(classify_answerability(session, [{"file_path": "candidate"}]), "partial")

    def test_supported_requires_explicit_successful_answer_signal(self) -> None:
        session = SimpleNamespace(answer="supported answer", has_successful_answer=True)
        self.assertEqual(classify_answerability(session, [{"file_path": "candidate"}]), "supported")

    def fixture(self) -> dict:
        first = "Water level and temperature drive arch dam displacement."
        second = "Historical cases can support auditable displacement predictions."
        rows = []
        provenance = {}
        for index, text in enumerate((first, second), start=1):
            digest = hashlib.sha256(text.encode()).hexdigest()
            source = f"zotero/PAPER/PDF/PAPER-PDF-page-{index:04d}-chunk-000-{digest[:12]}.txt"
            rows.append({"id": f"doc-{index}", "file_source": source, "text": text, "sha256": digest, "kind": "pdf_chunk"})
            provenance[source] = {
                "zotero_item_key": "PAPER",
                "attachment_key": "PDF",
                "page": index,
                "page_label": str(index),
                "chunk_sha256": digest,
                "zotero_uri": "zotero://select/library/items/PAPER",
                "kind": "pdf_chunk",
                "title": "Fixture Paper",
                "creators": ["A. Researcher"],
            }
        return {
            "schema": "researchos-zotero-corpus/v1",
            "publisher_version": "test",
            "parser": "test",
            "chunking": {"characters": 1400, "overlap_characters": 220},
            "source": {"kind": "zotero-local-api", "library_version": 10, "collection_key": "C", "collection_name": "fixture"},
            "counts": {"papers": 1, "attachments": 1, "annotations": 0, "fulltext_attachments": 1, "documents": 2},
            "attachment_files": [{"attachment_key": "PDF", "name": "paper.pdf", "size": 5, "sha256": "a" * 64}],
            "warnings": [],
            "documents": rows,
            "provenance": provenance,
        }

    def test_index_preserves_frozen_chunk_identity(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            revision = publish(self.fixture(), root / "corpora")
            output = root / "index"
            result = build_index(revision, output, embedding_mode="sparse")
            docs = pickle.loads((output / "docs.pkl").read_bytes())
            sources = {text.file_source for text in docs.texts}
            self.assertEqual(result["backend"], "paperqa2")
            self.assertEqual(result["documents"], 1)
            self.assertEqual(result["chunks"], 2)
            self.assertEqual(sources, set(json.loads((revision / "provenance.json").read_text())))

    def test_index_rejects_provenance_hash_mismatch(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            fixture = self.fixture()
            first_source = fixture["documents"][0]["file_source"]
            fixture["provenance"][first_source]["chunk_sha256"] = "f" * 64
            revision = publish(fixture, root / "corpora")
            with self.assertRaisesRegex(ValueError, "hash mismatch"):
                build_index(revision, root / "index", embedding_mode="sparse")


if __name__ == "__main__":
    unittest.main()
