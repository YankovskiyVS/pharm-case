"""Unit tests isolate model/cache state and never initiate public downloads."""

from pathlib import Path

import pytest
from pytest import MonkeyPatch


@pytest.fixture(autouse=True)
def isolate_runtime(
    request: pytest.FixtureRequest, tmp_path: Path, monkeypatch: MonkeyPatch
) -> None:
    monkeypatch.setenv("AUDIO_PREWARM_ON_START", "false")
    if request.node.name != "test_real_russian_transcription_and_cited_retrieval":
        monkeypatch.setenv("AUDIO_MODEL_DIR", str(tmp_path / "models"))
        monkeypatch.setenv("AUDIO_CACHE_DIR", str(tmp_path / "cache"))
