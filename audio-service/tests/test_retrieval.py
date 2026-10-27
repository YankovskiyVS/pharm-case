"""Retrieval policy tests run without ML dependencies or network downloads."""

import unittest
from retrieval import lexical, retrieve


class RetrievalTests(unittest.TestCase):
    def test_bm25_finds_exact_term_independently(self) -> None:
        scores = lexical("сон", ["Сегодня хороший сон", "Сегодня прогулка"])
        self.assertGreater(scores[0], scores[1])

    def test_hybrid_keeps_lexical_candidate_and_citation(self) -> None:
        chunks = [
            {"id": "a", "text": "сон улучшился", "recording_id": "r1", "start": 2, "end": 5},
            {"id": "b", "text": "гулял", "recording_id": "r2", "start": 1, "end": 3},
        ]
        found = retrieve("сон", chunks, [[0, 1], [1, 0]], [1, 0])
        self.assertTrue(
            any(c["id"] == "a" and c["recording_id"] == "r1" and c["start"] == 2 for c in found)
        )

    def test_unrelated_candidates_yield_insufficient_evidence(self) -> None:
        found = retrieve("лекарства", [{"id": "a", "text": "погода"}], [[0, 1]], [1, 0])
        self.assertEqual(found, [])

    def test_empty_corpus(self) -> None:
        self.assertEqual(retrieve("сон", [], [], [1, 0]), [])

    def test_semantic_candidate_without_exact_words(self) -> None:
        found = retrieve("отдых", [{"id": "allowed", "text": "спал хорошо"}], [[1, 0]], [1, 0])
        self.assertEqual([c["id"] for c in found], ["allowed"])

    def test_casefolded_cyrillic(self) -> None:
        self.assertEqual(lexical("СОН", ["Сон"]), lexical("сон", ["сон"]))


if __name__ == "__main__":
    unittest.main()
