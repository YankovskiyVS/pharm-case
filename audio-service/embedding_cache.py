"""Disposable persisted document vectors; clinical ownership remains with Go."""

from __future__ import annotations

import hashlib
import os
import sqlite3
import time
from collections.abc import Callable, Iterator, Sequence
from contextlib import contextmanager
from functools import lru_cache
from pathlib import Path
from typing import Any

import numpy as np
from numpy.typing import NDArray


class EmbeddingCache:
    """Cache by artifact identity, immutable chunk ID and exact content checksum.

    Raw transcript text and user queries are not stored. Every search still supplies
    an authorized candidate set; cached rows never expand that set.
    """

    def __init__(self, directory: Path, max_entries: int = 20000, ttl_days: int = 30) -> None:
        if max_entries < 500 or not 1 <= ttl_days <= 365:
            raise ValueError("invalid_embedding_cache_budget")
        directory.mkdir(parents=True, exist_ok=True, mode=0o700)
        self.path = directory / "embeddings.sqlite3"
        self.max_entries = max_entries
        self.ttl_seconds = ttl_days * 86400
        with self._connection() as db:
            db.execute("PRAGMA journal_mode=WAL")
            db.execute("""CREATE TABLE IF NOT EXISTS vectors(
                cache_key TEXT PRIMARY KEY, source_id TEXT NOT NULL,
                model_key TEXT NOT NULL, dim INTEGER NOT NULL,
                value BLOB NOT NULL, accessed_at INTEGER NOT NULL)""")
            db.execute("CREATE INDEX IF NOT EXISTS vectors_access ON vectors(accessed_at)")
            db.execute("CREATE INDEX IF NOT EXISTS vectors_source ON vectors(source_id)")
        self.path.chmod(0o600)

    @contextmanager
    def _connection(self) -> Iterator[sqlite3.Connection]:
        connection = sqlite3.connect(self.path, timeout=5)
        try:
            yield connection
            connection.commit()
        except Exception:
            connection.rollback()
            raise
        finally:
            connection.close()

    @staticmethod
    def key(model: str, source: str, text: str) -> str:
        return hashlib.sha256((model + "\0" + source + "\0" + text).encode()).hexdigest()

    @staticmethod
    def _valid(vector: NDArray[np.float32]) -> NDArray[np.float32]:
        value = np.asarray(vector, dtype=np.float32).reshape(-1)
        if len(value) < 1 or len(value) > 8192 or not np.isfinite(value).all():
            raise ValueError("invalid_embedding")
        return value

    def vectors(
        self,
        model: str,
        chunks: Sequence[dict[str, Any]],
        embed: Callable[[list[str]], list[NDArray[np.float32]]],
    ) -> tuple[list[list[float]], int]:
        """Reuse existing vectors and encode only changed/new authorized chunks."""
        now = int(time.time())
        keys = [self.key(model, str(c["id"]), str(c["text"])) for c in chunks]
        cached: dict[str, NDArray[np.float32]] = {}
        with self._connection() as db:
            for key in keys:
                row = db.execute(
                    "SELECT dim,value FROM vectors WHERE cache_key=?", (key,)
                ).fetchone()
                if row is not None:
                    value = np.frombuffer(row[1], dtype=np.float32)
                    if len(value) == row[0] and np.isfinite(value).all():
                        cached[key] = value
        missing = [(key, c) for key, c in zip(keys, chunks, strict=True) if key not in cached]
        if missing:
            encoded = embed([str(chunk["text"]) for _, chunk in missing])
            if len(encoded) != len(missing):
                raise ValueError("embedding_count_mismatch")
            with self._connection() as db:
                for (key, chunk), vector in zip(missing, encoded, strict=True):
                    value = self._valid(vector)
                    db.execute(
                        "INSERT OR REPLACE INTO vectors VALUES(?,?,?,?,?,?)",
                        (key, str(chunk["id"]), model, len(value), value.tobytes(), now),
                    )
                    cached[key] = value
        with self._connection() as db:
            db.executemany(
                "UPDATE vectors SET accessed_at=? WHERE cache_key=?", [(now, k) for k in keys]
            )
            db.execute("DELETE FROM vectors WHERE accessed_at<?", (now - self.ttl_seconds,))
            db.execute(
                """DELETE FROM vectors WHERE cache_key IN (
                SELECT cache_key FROM vectors ORDER BY accessed_at DESC,cache_key
                LIMIT -1 OFFSET ?)""",
                (self.max_entries,),
            )
        return [cached[key].astype(float).tolist() for key in keys], len(chunks) - len(missing)

    def invalidate(self, sources: Sequence[str]) -> int:
        """A Go-owned delete may invalidate derived vectors without exposing text."""
        with self._connection() as db:
            before = db.total_changes
            db.executemany("DELETE FROM vectors WHERE source_id=?", [(s,) for s in sources])
            return db.total_changes - before

    def clear(self) -> None:
        with self._connection() as db:
            db.execute("DELETE FROM vectors")

    def stats(self) -> dict[str, int]:
        with self._connection() as db:
            row = db.execute(
                "SELECT count(*),coalesce(sum(length(value)),0) FROM vectors"
            ).fetchone()
        return {"entries": int(row[0]), "vector_bytes": int(row[1])}


@lru_cache(maxsize=8)
def _cache(directory: Path, max_entries: int, ttl_days: int) -> EmbeddingCache:
    return EmbeddingCache(directory, max_entries, ttl_days)


def cache(directory: Path) -> EmbeddingCache:
    """Open a private, rebuildable cache with bounded technical retention."""
    return _cache(
        directory,
        max_entries=int(os.getenv("AUDIO_CACHE_MAX_ENTRIES", "20000")),
        ttl_days=int(os.getenv("AUDIO_CACHE_TTL_DAYS", "30")),
    )
