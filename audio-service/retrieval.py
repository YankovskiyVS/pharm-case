"""Patient-scoped modular hybrid retrieval; caller supplies authorized chunks only."""

from __future__ import annotations

import math
import re
from collections import Counter
from collections.abc import Sequence
from typing import Any


def terms(text: str) -> list[str]:
    """Tokenize Russian and Latin words consistently."""
    return re.findall(r"[\w]+", text.casefold())


def lexical(query: str, texts: list[str]) -> list[float]:
    """BM25 candidates are scored independently of dense retrieval."""
    docs = [terms(t) for t in texts]
    average = sum(map(len, docs)) / max(len(docs), 1)
    scores = [0.0] * len(docs)
    for word in set(terms(query)):
        df = sum(word in doc for doc in docs)
        idf = math.log(1 + (len(docs) - df + 0.5) / (df + 0.5))
        for i, doc in enumerate(docs):
            tf = Counter(doc)[word]
            scores[i] += idf * tf * 2.2 / (tf + 1.2 * (0.25 + 0.75 * len(doc) / max(average, 1)))
    return scores


def retrieve(
    query: str,
    chunks: list[dict[str, Any]],
    vectors: Sequence[Sequence[float]],
    question: Sequence[float],
) -> list[dict[str, Any]]:
    """Fuse independent BM25/dense candidates, then rerank for query coverage."""
    if not chunks:
        return []
    text_scores = lexical(query, [c["text"] for c in chunks])
    question_norm = math.sqrt(sum(x * x for x in question))
    dense = [
        sum(a * b for a, b in zip(vector, question, strict=True))
        / max(math.sqrt(sum(x * x for x in vector)) * question_norm, 1e-9)
        for vector in vectors
    ]
    sparse_order = sorted(range(len(chunks)), key=lambda i: text_scores[i], reverse=True)[:30]
    dense_order = sorted(range(len(chunks)), key=lambda i: dense[i], reverse=True)[:30]
    fusion: dict[int, float] = {}
    for order in (sparse_order, dense_order):
        for rank, idx in enumerate(order):
            fusion[idx] = fusion.get(idx, 0) + 1 / (60 + rank + 1)
    words = set(terms(query))
    candidates = sorted(fusion, key=lambda i: fusion[i], reverse=True)[:20]
    # Deterministic second stage, explicitly not a clinical relevance probability.
    candidates.sort(
        key=lambda i: (
            fusion[i]
            + 0.02 * len(words & set(terms(chunks[i]["text"]))) / max(len(words), 1)
            + 0.02 * float(dense[i])
        ),
        reverse=True,
    )
    # Conservative empty-evidence behavior; threshold is technical, not validated clinically.
    return [
        {**chunks[i], "similarity": round(float(dense[i]), 4)}
        for i in candidates
        if text_scores[i] > 0 or dense[i] >= 0.85
    ][:5]
