from __future__ import annotations

import hashlib
import json
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

from integrations.zotero_lightrag.gateway import Gateway
from integrations.zotero_lightrag.index_revision import canonicalize_rows, select_rows
from integrations.zotero_lightrag.publish_corpus import build_revision, chunks, publish
from integrations.zotero_lightrag.validate_revision import validate


class CorpusContractTests(unittest.TestCase):
    def fixture(self) -> dict:
        text = "page evidence"
        digest = hashlib.sha256(text.encode()).hexdigest()
        source = f"zotero/PAPER/PDF/PAPER-PDF-page-0001-chunk-000-{digest[:12]}.txt"
        return {
            "schema": "researchos-zotero-corpus/v1",
            "publisher_version": "test",
            "parser": "test",
            "chunking": {"characters": 1400, "overlap_characters": 220},
            "source": {"kind": "zotero-local-api", "library_version": 10, "collection_key": "C", "collection_name": "fixture"},
            "counts": {"papers": 1, "attachments": 1, "annotations": 0, "fulltext_attachments": 1, "documents": 1},
            "attachment_files": [{"attachment_key": "PDF", "name": "paper.pdf", "size": 5, "sha256": "a" * 64}],
            "warnings": [],
            "documents": [{"id": "doc-1", "file_source": source, "text": text, "sha256": digest, "kind": "pdf_chunk"}],
            "provenance": {source: {"zotero_item_key": "PAPER", "attachment_key": "PDF", "page": 1, "chunk_sha256": digest, "zotero_uri": "zotero://select/library/items/PAPER", "kind": "pdf_chunk"}},
        }

    def test_publish_is_immutable_and_idempotent(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            first = publish(self.fixture(), root)
            second = publish(self.fixture(), root)
            self.assertEqual(first, second)
            result = validate(first)
            self.assertEqual(result["status"], "verified")
            self.assertEqual(result["documents"], 1)

    def test_validator_detects_tampering(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            revision = publish(self.fixture(), root)
            with (revision / "documents.jsonl").open("a") as handle:
                handle.write("{}\n")
            with self.assertRaisesRegex(ValueError, "size mismatch"):
                validate(revision)

    def test_chunks_progress_with_overlap(self) -> None:
        result = list(chunks("abcdefghij", size=5, overlap=2))
        self.assertEqual(result, ["abcde", "defgh", "ghij"])

    def test_collection_tree_is_recursive_deduplicated_and_unbounded_by_default(self) -> None:
        collections = {
            "ROOT": {"key": "ROOT", "data": {"key": "ROOT", "name": "ArchDam"}},
            "METHODS": {"key": "METHODS", "data": {"key": "METHODS", "name": "Methods"}},
            "MISSING": {"key": "MISSING", "data": {"key": "MISSING", "name": "Missing data"}},
        }

        def paper(key: str, memberships: list[str]) -> dict:
            return {
                "key": key,
                "data": {
                    "key": key,
                    "itemType": "journalArticle",
                    "title": key,
                    "collections": memberships,
                    "creators": [],
                    "tags": [],
                },
            }

        papers = {
            "ROOT": [paper("P2", ["ROOT", "METHODS"]), paper("P1", ["ROOT"])],
            "METHODS": [paper("P2", ["ROOT", "METHODS"]), paper("P3", ["METHODS"])],
            "MISSING": [paper("P4", ["MISSING"])],
        }

        def pages(path: str) -> list[dict]:
            if path == "/collections/ROOT/collections":
                return [collections["METHODS"]]
            if path == "/collections/METHODS/collections":
                return [collections["MISSING"]]
            if path == "/collections/MISSING/collections":
                return []
            for key, values in papers.items():
                if path == f"/collections/{key}/items/top":
                    return values
            if path.startswith("/items/") and path.endswith("/children"):
                return []
            raise AssertionError(f"unexpected Zotero path: {path}")

        with (
            patch("integrations.zotero_lightrag.publish_corpus.current_library_version", return_value=10),
            patch("integrations.zotero_lightrag.publish_corpus.get_json", return_value=(collections["ROOT"], {})),
            patch("integrations.zotero_lightrag.publish_corpus.all_pages", side_effect=pages),
            patch("integrations.zotero_lightrag.publish_corpus.annotation_groups", return_value={}),
        ):
            revision = build_revision("ROOT", None, 120)

        self.assertEqual(revision["counts"]["collections"], 3)
        self.assertEqual(revision["counts"]["papers"], 4)
        self.assertEqual(revision["counts"]["metadata_documents"], 4)
        self.assertEqual(len(revision["documents"]), 4)
        self.assertEqual(revision["source"]["recursive"], True)
        self.assertEqual(
            [collection["key"] for collection in revision["source"]["collections"]],
            ["METHODS", "MISSING", "ROOT"],
        )

    def test_max_papers_remains_an_explicit_deterministic_test_cap(self) -> None:
        collection = {"key": "ROOT", "data": {"key": "ROOT", "name": "ArchDam"}}
        papers = [
            {"key": key, "data": {"key": key, "itemType": "journalArticle", "title": key, "collections": ["ROOT"]}}
            for key in ("P3", "P1", "P2")
        ]

        def pages(path: str) -> list[dict]:
            if path == "/collections/ROOT/collections":
                return []
            if path == "/collections/ROOT/items/top":
                return papers
            if path.startswith("/items/") and path.endswith("/children"):
                return []
            raise AssertionError(f"unexpected Zotero path: {path}")

        with (
            patch("integrations.zotero_lightrag.publish_corpus.current_library_version", return_value=10),
            patch("integrations.zotero_lightrag.publish_corpus.get_json", return_value=(collection, {})),
            patch("integrations.zotero_lightrag.publish_corpus.all_pages", side_effect=pages),
            patch("integrations.zotero_lightrag.publish_corpus.annotation_groups", return_value={}),
        ):
            revision = build_revision("ROOT", 2, 120)

        self.assertEqual(revision["counts"]["papers"], 2)
        self.assertEqual(revision["counts"]["metadata_documents"], 2)
        self.assertEqual(
            sorted(source["zotero_item_key"] for source in revision["provenance"].values()),
            ["P1", "P2"],
        )

    def test_gateway_returns_revision_bound_evidence(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            revision = publish(self.fixture(), root / "corpora")
            (root / "active.json").write_text(
                json.dumps(
                    {
                        "status": "shadow",
                        "freshness": "fresh",
                        "corpus_revision": revision.name,
                    }
                )
            )
            source = next(iter(json.loads((revision / "provenance.json").read_text())))
            gateway = Gateway(root, "http://upstream", "internal", "external")
            upstream = {
                "response": "supported answer",
                "references": [{"reference_id": "1", "file_path": source, "content": ["page evidence"]}],
            }
            with patch("integrations.zotero_lightrag.gateway.post_json", return_value=(200, upstream)):
                status, response = gateway.query({"query": "what is supported?"})
            self.assertEqual(status, 200)
            self.assertEqual(response["answerability"], "partial")
            self.assertEqual(response["evidence_domain"], "literature")
            self.assertEqual(response["claim_scope"], "background_only")
            self.assertEqual(response["freshness"], "fresh")
            self.assertEqual(response["evidence"][0]["page"], 1)
            self.assertEqual(response["evidence"][0]["file_source"], source)
            self.assertEqual(response["corpus_revision"], revision.name)

    def test_gateway_can_query_an_indexed_non_active_revision(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            active_revision = publish(self.fixture(), root / "corpora")
            other_fixture = self.fixture()
            other_fixture["source"]["collection_key"] = "OTHER"
            other_fixture["source"]["collection_name"] = "other"
            other_revision = publish(other_fixture, root / "corpora")
            (root / "active.json").write_text(json.dumps({
                "status": "indexed", "freshness": "fresh", "corpus_revision": active_revision.name,
            }))
            workspace = root / "paperqa" / f"{other_revision.name}-hybrid"
            workspace.mkdir(parents=True)
            (workspace / "index-result.json").write_text(json.dumps({
                "status": "indexed", "backend": "paperqa2", "embedding_mode": "hybrid",
                "corpus_revision": other_revision.name, "documents": 1, "chunks": 1,
            }))
            source = next(iter(json.loads((other_revision / "provenance.json").read_text())))
            gateway = Gateway(root, "http://upstream", "internal", "external")
            upstream = {"response": "other answer", "references": [{"file_path": source, "content": ["page evidence"]}]}
            with patch("integrations.zotero_lightrag.gateway.post_json", return_value=(200, upstream)) as request:
                status, response = gateway.query({"query": "what is supported?", "corpus_revision": other_revision.name})
            self.assertEqual(status, 200)
            self.assertEqual(response["corpus_revision"], other_revision.name)
            self.assertEqual(response["zotero_collection_key"], "OTHER")
            self.assertEqual(request.call_args.args[1]["corpus_revision"], other_revision.name)

    def test_gateway_resolves_lightrag_basename_reference(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            revision = publish(self.fixture(), root / "corpora")
            (root / "active.json").write_text(
                json.dumps({"status": "shadow", "freshness": "fresh", "corpus_revision": revision.name})
            )
            source = next(iter(json.loads((revision / "provenance.json").read_text())))
            gateway = Gateway(root, "http://upstream", "internal", "external")
            upstream = {
                "response": "supported answer",
                "references": [{"reference_id": "1", "file_path": Path(source).name, "content": ["page evidence"]}],
            }
            with patch("integrations.zotero_lightrag.gateway.post_json", return_value=(200, upstream)):
                status, response = gateway.query({"query": "what is supported?"})
            self.assertEqual(status, 200)
            self.assertEqual(response["answerability"], "partial")
            self.assertEqual(response["evidence"][0]["file_source"], source)
            self.assertFalse(any(item["code"] == "REFERENCE_PROVENANCE_MISSING" for item in response["warnings"]))

    def test_gateway_uses_paperqa_as_primary_without_silent_fallback(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            revision = publish(self.fixture(), root / "corpora")
            (root / "active.json").write_text(
                json.dumps({"status": "shadow", "freshness": "fresh", "corpus_revision": revision.name})
            )
            source = next(iter(json.loads((revision / "provenance.json").read_text())))
            gateway = Gateway(
                root,
                "http://lightrag",
                "light-key",
                "external",
                paperqa_upstream="http://paperqa",
                paperqa_key="paper-key",
                primary_backend="paperqa2",
            )
            upstream = {
                "response": "PaperQA answer",
                "answerability": "supported",
                "retrieval_mode": "paperqa2-direct",
                "references": [{"reference_id": "pqac-1", "file_path": source, "content": ["page evidence"], "score": 9}],
            }
            with patch("integrations.zotero_lightrag.gateway.post_json", return_value=(200, upstream)) as request:
                status, response = gateway.query({"query": "what is supported?"})
            self.assertEqual(status, 200)
            self.assertEqual(request.call_args.args[0], "http://paperqa/query")
            self.assertEqual(response["retrieval_backend"], "paperqa2")
            self.assertEqual(response["evidence"][0]["score"], 9)
            self.assertFalse(response["fallback_used"])
            self.assertFalse(any(item["code"] == "PARTIAL_INDEX_COVERAGE" for item in response["warnings"]))

    def test_paperqa_only_revision_does_not_require_lightrag(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            revision = publish(self.fixture(), root / "corpora")
            (root / "active.json").write_text(
                json.dumps(
                    {
                        "status": "indexed",
                        "freshness": "fresh",
                        "corpus_revision": revision.name,
                        "lightrag_ready": False,
                        "lightrag_status": "disabled",
                    }
                )
            )
            source = next(iter(json.loads((revision / "provenance.json").read_text())))
            gateway = Gateway(root, "http://lightrag", "light", "external", primary_backend="paperqa2")
            upstream = {
                "response": "PaperQA-only answer",
                "answerability": "supported",
                "references": [{"reference_id": "1", "file_path": source, "content": ["evidence"]}],
            }
            with patch("integrations.zotero_lightrag.gateway.post_json", return_value=(200, upstream)) as request:
                status, response = gateway.query({"query": "what is supported?"})
            self.assertEqual(status, 200)
            self.assertEqual(response["retrieval_backend"], "paperqa2")
            self.assertEqual(request.call_count, 1)

            with patch("integrations.zotero_lightrag.gateway.post_json") as request:
                status, response = gateway.query({"query": "what is supported?", "backend": "lightrag"})
            self.assertEqual(status, 409)
            self.assertEqual(response["code"], "LIGHTRAG_NOT_ENABLED")
            request.assert_not_called()

    def test_paperqa_failure_never_falls_back_to_lightrag(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            revision = publish(self.fixture(), root / "corpora")
            (root / "active.json").write_text(
                json.dumps({"status": "shadow", "freshness": "fresh", "corpus_revision": revision.name})
            )
            gateway = Gateway(root, "http://lightrag", "light", "external", primary_backend="paperqa2")
            with patch("integrations.zotero_lightrag.gateway.post_json", return_value=(503, {"detail": "not indexed"})) as request:
                status, response = gateway.query(
                    {"query": "what is supported?", "allow_lightrag_fallback": True}
                )
            self.assertEqual(status, 503)
            self.assertEqual(response["code"], "PAPERQA_UPSTREAM")
            self.assertEqual(request.call_count, 1)

    def test_fixture_file_source_basename_is_globally_addressable(self) -> None:
        source = self.fixture()["documents"][0]["file_source"]
        self.assertEqual(Path(source).name.split("-")[:2], ["PAPER", "PDF"])

    def test_index_canonicalization_keeps_a_stable_source(self) -> None:
        rows = [
            {"sha256": "b", "file_source": "zotero/B.txt", "text": " repeated "},
            {"sha256": "a", "file_source": "zotero/A.txt", "text": "repeated"},
            {"sha256": "c", "file_source": "zotero/C.txt", "text": "other"},
        ]
        canonical = canonicalize_rows(rows)
        self.assertEqual({row["file_source"] for row in canonical}, {"zotero/A.txt", "zotero/C.txt"})
        self.assertEqual({row["text"] for row in canonical}, {"repeated", "other"})

    def test_shadow_selection_balances_pdf_chunks_per_paper(self) -> None:
        rows = []
        provenance = {}
        for paper in ("P1", "P2"):
            for page in range(1, 7):
                source = f"zotero/{paper}/A/{paper}-A-page-{page:04d}.txt"
                rows.append({"kind": "pdf_chunk", "file_source": source, "sha256": f"{paper}-{page}", "text": source})
                provenance[source] = {"zotero_item_key": paper, "page": page, "page_chunk_index": 0}
        annotation = {"kind": "annotation", "file_source": "zotero/P1/A/P1-A-annotation.txt", "sha256": "note", "text": "note"}
        rows.append(annotation)
        provenance[annotation["file_source"]] = {"zotero_item_key": "P1"}
        selected = select_rows(rows, provenance, "shadow", 3)
        self.assertEqual(len(selected), 7)
        self.assertEqual(sum(row["kind"] == "annotation" for row in selected), 1)


if __name__ == "__main__":
    unittest.main()
