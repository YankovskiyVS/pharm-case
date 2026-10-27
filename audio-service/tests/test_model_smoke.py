"""Opt-in real ASR/retrieval smoke test using synthetic Russian speech."""

import os
import subprocess
from pathlib import Path

import pytest

from service import Chunk, Search, analyze, search
from model_runtime import runtime


@pytest.mark.skipif(os.getenv("RUN_MODEL_SMOKE") != "1", reason="opt-in model downloads")
def test_real_russian_transcription_and_cited_retrieval(tmp_path: Path) -> None:
    assert runtime().prewarm(download=False), "run python -m manage_models prewarm first"
    wav = tmp_path / "synthetic.wav"
    subprocess.run(
        [
            "espeak-ng",
            "-v",
            "ru",
            "-s",
            "130",
            "-w",
            str(wav),
            "Сегодня я хорошо спал. Утром я проснулся и открыл окно. "
            "Ночью я почти не просыпался. Сейчас я рассказываю о своём сне. "
            "Вечером я планирую спокойно отдохнуть и почитать книгу.",
        ],
        check=True,
        capture_output=True,
        timeout=20,
    )
    measured = analyze(wav.read_bytes(), "free")
    assert measured["transcription_status"] == "ready"
    assert measured["segments"]
    chunks = [
        Chunk(id=f"synthetic-{i}", recording_id="synthetic-recording", **segment)
        for i, segment in enumerate(measured["segments"])
    ]
    # Reuse recognized words to test wiring; separate labelled evaluation measures ASR accuracy.
    answer = search(Search(query=chunks[0].text, chunks=chunks))
    assert answer["status"] == "ready"
    assert all(source["recording_id"] == "synthetic-recording" for source in answer["sources"])
