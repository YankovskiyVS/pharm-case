"""Exercise real HTTP+audio validation with deterministic fixtures, no clinical model."""

import asyncio
import io
import wave
from pathlib import Path
from typing import Any

import numpy as np
import pytest
from fastapi.testclient import TestClient
from pytest import MonkeyPatch

import service
from artifacts import ArtifactError
from service import Runtime


@pytest.fixture
def client(tmp_path: Path, monkeypatch: MonkeyPatch) -> Any:
    secret = tmp_path / "token"
    secret.write_text("x" * 40)
    monkeypatch.setenv("VOICEFM_TOKEN_FILE", str(secret))
    monkeypatch.setenv("VOICEFM_MODE", "mock")
    monkeypatch.setenv("VOICEFM_MODEL_DIR", str(tmp_path / "no-model"))
    monkeypatch.setattr(service, "runtime", Runtime())
    with TestClient(service.app) as value:
        yield value


AUTH = {"Authorization": "Bearer " + "x" * 40}


def audio(frequency: float = 440) -> bytes:
    output = io.BytesIO()
    with wave.open(output, "wb") as target:
        target.setnchannels(1)
        target.setsampwidth(2)
        target.setframerate(16000)
        values = np.sin(np.arange(1600) * 2 * np.pi * frequency / 16000)
        target.writeframes((values * 20000).astype("<i2").tobytes())
    return output.getvalue()


def test_authentication_readiness_and_explicit_mock_status(client: TestClient) -> None:
    assert client.get("/health/live").json() == {"status": "ok"}
    assert client.get("/status").status_code == 401
    value = client.get("/status", headers=AUTH).json()
    assert value["ready"] and value["is_mock"] and value["mode"] == "mock"
    assert value["clinical_use"] is False
    assert client.get("/health/ready").json()["is_mock"] is True


def test_fixture_is_identical_for_different_audio_and_not_a_patient_prediction(
    client: TestClient,
) -> None:
    first = client.post(
        "/analyze",
        content=audio(),
        headers={**AUTH, "Content-Type": "audio/wav", "X-Voice-Task": "vowel"},
    )
    second = client.post("/infer", content=audio(700), headers=AUTH)
    assert first.status_code == second.status_code == 200
    a, b = first.json(), second.json()
    assert a["outputs"] == b["outputs"]
    assert a["input_sha256"] != b["input_sha256"]
    assert a["model"]["name"] == "voicefm-demo-fixture-v1"
    assert a["input_used_for_prediction"] is False
    assert "not a prediction about this patient" in a["outputs"]["interpretation"]
    assert a["outputs"]["scores"] == service.MOCK_SCORES
    assert service.runtime.active == 0


def test_invalid_audio_empty_upload_and_size_limit(client: TestClient) -> None:
    assert client.post("/analyze", content=b"invalid", headers=AUTH).status_code == 422
    assert client.post("/analyze", content=b"", headers=AUTH).status_code == 422
    assert (
        client.post("/analyze", content=b"x" * (12 * 1024 * 1024 + 1), headers=AUTH).status_code
        == 413
    )
    assert (
        client.post(
            "/analyze", content=audio(), headers={**AUTH, "X-Voice-Task": "arbitrary"}
        ).status_code
        == 422
    )


def test_real_mode_without_checkpoint_is_503_and_never_a_mock(
    client: TestClient, monkeypatch: MonkeyPatch
) -> None:
    monkeypatch.setenv("VOICEFM_MODE", "real")
    value = Runtime()
    asyncio.run(value.initialize())
    monkeypatch.setattr(service, "runtime", value)
    ready = client.get("/health/ready")
    assert ready.status_code == 503
    assert ready.json()["reason_code"] == "not_installed"
    response = client.post("/analyze", content=audio(), headers=AUTH)
    assert response.status_code == 503
    assert response.json()["is_mock"] is False
    assert response.json()["input_used_for_prediction"] is False
    assert response.json()["outputs"] == {}


def test_absent_or_short_secret_rejects_even_known_empty_bearer(
    client: TestClient, tmp_path: Path, monkeypatch: MonkeyPatch
) -> None:
    secret = tmp_path / "short"
    secret.write_text("")
    monkeypatch.setenv("VOICEFM_TOKEN_FILE", str(secret))
    assert client.get("/status", headers={"Authorization": "Bearer "}).status_code == 401
    monkeypatch.setenv("VOICEFM_TOKEN_FILE", str(tmp_path / "absent"))
    assert client.get("/status", headers=AUTH).status_code == 401


def test_disabled_mode_and_budget_validation(monkeypatch: MonkeyPatch) -> None:
    monkeypatch.setenv("VOICEFM_MODE", "invalid")
    value = Runtime()
    assert value.mode == "disabled" and not value.ready
    assert value.status()["reason_code"] == "disabled"
    monkeypatch.setenv("TIMEOUT_TEST", "9999999")
    assert service.setting("TIMEOUT_TEST", 3, 10) == 10
    monkeypatch.setenv("TIMEOUT_TEST", "invalid")
    assert service.setting("TIMEOUT_TEST", 3, 10) == 3


def test_lane_releases_on_model_failure(client: TestClient, monkeypatch: MonkeyPatch) -> None:
    async def invalid(_content: bytes) -> None:
        raise ArtifactError("invalid_audio")

    monkeypatch.setattr(service, "validate_audio", invalid)
    assert client.post("/analyze", content=audio(), headers=AUTH).status_code == 422
    assert service.runtime.lane._value == 1


def test_queue_is_bounded_and_does_not_start_more_work(
    client: TestClient, monkeypatch: MonkeyPatch
) -> None:
    monkeypatch.setenv("VOICEFM_QUEUE_SECONDS", "1")
    service.runtime.lane._value = 0
    assert client.post("/analyze", content=audio(), headers=AUTH).status_code == 429
    assert service.runtime.active == 0
    service.runtime.lane._value = 1


def test_actual_subprocess_is_killed_after_deadline(
    tmp_path: Path, monkeypatch: MonkeyPatch
) -> None:
    import sys

    monkeypatch.setenv("VOICEFM_TIMEOUT_SECONDS", "1")
    original = asyncio.create_subprocess_exec
    children: list[Any] = []

    async def sleeping_process(*_args: str, **kwargs: Any) -> Any:
        process = await original(sys.executable, "-c", "import time;time.sleep(30)", **kwargs)
        children.append(process)
        return process

    monkeypatch.setattr(service.asyncio, "create_subprocess_exec", sleeping_process)
    with pytest.raises(TimeoutError):
        asyncio.run(Runtime().operation(None, "unknown"))
    assert len(children) == 1 and children[0].returncode == -9


def test_real_runtime_without_dependencies_fails_closed(
    tmp_path: Path, monkeypatch: MonkeyPatch
) -> None:
    from test_artifacts import bundle

    monkeypatch.setenv("VOICEFM_MODE", "real")
    monkeypatch.setenv("VOICEFM_MODEL_DIR", str(bundle(tmp_path / "bundle")))
    monkeypatch.setattr(service, "dependencies", lambda: False)
    value = Runtime()
    asyncio.run(value.initialize())
    assert not value.ready and value.reason == "runtime_not_installed"
    assert value.status()["is_mock"] is False
    assert value.status()["model"]["revision"] != "unavailable"
