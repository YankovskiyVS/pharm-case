"""Persistent-vector caching is independent of model downloads and authorization."""

from pathlib import Path
import sqlite3

import numpy as np
import pytest

from embedding_cache import EmbeddingCache


def test_restart_reuses_vectors_and_content_or_model_change_recomputes(tmp_path: Path) -> None:
    calls: list[list[str]] = []

    def encode(texts: list[str]) -> list[np.ndarray]:
        calls.append(texts)
        return [np.asarray([len(t), 1], dtype=np.float32) for t in texts]

    chunks = [{"id": "one", "text": "private exact phrase"}, {"id": "two", "text": "other"}]
    store = EmbeddingCache(tmp_path)
    first, hits = store.vectors("model-artifact-v1", chunks, encode)
    assert hits == 0 and len(calls) == 1
    restarted = EmbeddingCache(tmp_path)
    again, hits = restarted.vectors("model-artifact-v1", chunks, encode)
    assert first == again and hits == 2 and len(calls) == 1
    chunks[0]["text"] = "corrected text"
    _, hits = restarted.vectors("model-artifact-v1", chunks, encode)
    assert hits == 1 and calls[-1] == ["corrected text"]
    _, hits = restarted.vectors("model-artifact-v2", chunks, encode)
    assert hits == 0 and len(calls[-1]) == 2
    assert b"private exact phrase" not in store.path.read_bytes()


def test_cache_never_expands_authorized_candidates_and_supports_invalidation(
    tmp_path: Path,
) -> None:
    store = EmbeddingCache(tmp_path)

    def encode(texts: list[str]) -> list[np.ndarray]:
        return [np.asarray([len(t), 1], dtype=np.float32) for t in texts]

    store.vectors("v1", [{"id": "allowed", "text": "one"}, {"id": "other", "text": "two"}], encode)
    result, hits = store.vectors("v1", [{"id": "allowed", "text": "one"}], encode)
    assert len(result) == 1 and hits == 1
    assert store.invalidate(["allowed"]) == 1
    assert store.stats()["entries"] == 1
    store.clear()
    assert store.stats() == {"entries": 0, "vector_bytes": 0}


def test_corrupt_vector_recomputed_and_cache_size_bounded(tmp_path: Path) -> None:
    store = EmbeddingCache(tmp_path, max_entries=500)

    def encode(texts: list[str]) -> list[np.ndarray]:
        return [np.asarray([1, 0], dtype=np.float32) for _ in texts]

    chunks = [{"id": str(i), "text": "sample"} for i in range(501)]
    store.vectors("v1", chunks, encode)
    assert store.stats()["entries"] == 500
    key = store.key("v1", "500", "sample")
    with sqlite3.connect(store.path) as db:
        db.execute("UPDATE vectors SET dim=999 WHERE cache_key=?", (key,))
    _, hits = store.vectors("v1", [chunks[-1]], encode)
    assert hits == 0


def test_invalid_vector_and_configuration_rejected(tmp_path: Path) -> None:
    with pytest.raises(ValueError, match="budget"):
        EmbeddingCache(tmp_path, max_entries=0)
    store = EmbeddingCache(tmp_path)
    for vector in [np.asarray([]), np.asarray([np.nan]), np.zeros(9000)]:
        with pytest.raises(ValueError):
            store.vectors("v1", [{"id": "x", "text": "text"}], lambda _: [vector])
    with pytest.raises(ValueError, match="count"):
        store.vectors("v1", [{"id": "x", "text": "text"}], lambda _: [])
