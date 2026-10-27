"""Model readiness, paths and offline restart can be verified without inference."""

from pathlib import Path

import numpy as np
import pytest
from pytest import MonkeyPatch

import model_runtime
from model_runtime import ModelRuntime, ModelsUnavailable, Settings


def settings(tmp_path: Path) -> Settings:
    return Settings(tmp_path / "models", tmp_path / "cache", "base", "test-embed")


def test_unprepared_models_fail_without_network_downloads(
    tmp_path: Path, monkeypatch: MonkeyPatch
) -> None:
    registry = ModelRuntime(settings(tmp_path))
    monkeypatch.setattr(
        model_runtime, "download_model", lambda *a, **k: pytest.fail("network attempted")
    )
    assert not registry.prewarm(download=False)
    assert registry.status()["status"] == "not_ready"
    assert registry.status()["models"]["asr"]["error_code"] == "asr_model_unavailable"
    assert registry.status()["models"]["embedding"]["error_code"] == "embedding_model_unavailable"


def test_prewarm_and_offline_restart_use_identical_paths(
    tmp_path: Path, monkeypatch: MonkeyPatch
) -> None:
    config = settings(tmp_path)
    paths: list[str] = []
    local_flags: list[bool] = []

    def download(_name: str, **kwargs: object) -> str:
        path = Path(str(kwargs["output_dir"]))
        path.mkdir(parents=True, exist_ok=True)
        (path / "model.bin").write_bytes(b"synthetic public model fixture")
        return str(path)

    def asr(path: str, **kwargs: object) -> object:
        paths.append(path)
        assert kwargs["local_files_only"] is True
        return object()

    class Embed:
        def __init__(self, _name: str, **kwargs: object) -> None:
            local_flags.append(bool(kwargs["local_files_only"]))
            target = Path(str(kwargs["cache_dir"]))
            target.mkdir(parents=True, exist_ok=True)
            (target / "model.onnx").write_bytes(b"test public weights")

        def embed(self, _texts: list[str], **_kwargs: object) -> list[np.ndarray]:
            return [np.asarray([1, 0], dtype=np.float32)]

    monkeypatch.setattr(model_runtime, "download_model", download)
    monkeypatch.setattr(model_runtime, "WhisperModel", asr)
    monkeypatch.setattr(model_runtime, "TextEmbedding", Embed)
    registry = ModelRuntime(config)
    progress: list[str] = []
    assert registry.prewarm(download=True, progress=lambda s: progress.append(s["status"]))
    assert registry.status()["ready"]
    assert "warming" in progress
    fingerprint = registry.embedding_key
    asr_fingerprint = registry.asr_revision
    restarted = ModelRuntime(config)
    monkeypatch.setattr(
        model_runtime, "download_model", lambda *a, **k: pytest.fail("unexpected download")
    )
    assert restarted.status()["models"]["asr"]["cached"]
    assert restarted.prewarm(download=False)
    assert restarted.status()["ready"]
    assert paths == [str(config.asr_path), str(config.asr_path)]
    assert local_flags == [False, True]
    assert restarted.embedding_key == fingerprint
    assert restarted.asr_revision == asr_fingerprint
    assert asr_fingerprint.startswith("sha256:")
    (config.asr_path / "model.bin").write_bytes(b"changed model under the same alias")
    changed = ModelRuntime(config)
    changed.load_asr(download=False)
    assert changed.asr_revision != asr_fingerprint


def test_model_revision_changes_path_and_artifact_changes_fingerprint(tmp_path: Path) -> None:
    config = settings(tmp_path)
    alternate = Settings(
        config.model_dir, config.cache_dir, config.asr_model, config.embed_model, "new"
    )
    assert alternate.asr_path != config.asr_path
    registry = ModelRuntime(config)
    with pytest.raises(ModelsUnavailable):
        registry._artifact_fingerprint()
    config.embedding_path.mkdir(parents=True)
    model = config.embedding_path / "model.onnx"
    model.write_bytes(b"old")
    old = registry._artifact_fingerprint()
    model.write_bytes(b"new")
    assert registry._artifact_fingerprint() != old
