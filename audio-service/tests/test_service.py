"""Service authentication and bounded input validation without model downloads."""

from pathlib import Path

from fastapi.testclient import TestClient
from pytest import MonkeyPatch

from service import app


def test_private_service_requires_credential(tmp_path: Path, monkeypatch: MonkeyPatch) -> None:
    secret = tmp_path / "token"
    secret.write_text("test-secret-at-least-32-characters")
    monkeypatch.setenv("AUDIO_TOKEN_FILE", str(secret))
    with TestClient(app) as client:
        assert client.get("/health").status_code == 200
        assert client.post("/analyze", content=b"private").status_code == 401
        assert client.post("/search", json={"query": "сон", "chunks": []}).status_code == 401
        headers = {"Authorization": "Bearer test-secret-at-least-32-characters"}
        response = client.post("/search", headers=headers, json={"query": "сон", "chunks": []})
        assert response.status_code == 200
        assert response.json()["status"] == "insufficient_evidence"
        assert (
            client.post("/analyze?task=unknown", headers=headers, content=b"bad").status_code == 400
        )


def test_readiness_and_status_distinguish_missing_models(
    tmp_path: Path, monkeypatch: MonkeyPatch
) -> None:
    secret = tmp_path / "token"
    secret.write_text("service-secret-at-least-32-characters")
    monkeypatch.setenv("AUDIO_TOKEN_FILE", str(secret))
    with TestClient(app) as client:
        assert client.get("/health/live").status_code == 200
        assert client.get("/health/ready").status_code == 503
        assert client.get("/status").status_code == 401
        headers = {"Authorization": "Bearer " + secret.read_text()}
        response = client.get("/status", headers=headers)
        assert response.status_code == 200
        state = response.json()
        assert state["ready"] is False
        assert state["cache"]["persistent"] is True
        assert state["models"]["asr"]["state"] == "not_loaded"
        assert client.post("/cache/invalidate", headers=headers, json={"chunk_ids": []}).json() == {
            "removed": 0
        }


def test_empty_service_secret_does_not_become_a_known_bearer(
    tmp_path: Path, monkeypatch: MonkeyPatch
) -> None:
    secret = tmp_path / "empty"
    secret.write_text("")
    monkeypatch.setenv("AUDIO_TOKEN_FILE", str(secret))
    with TestClient(app) as client:
        assert client.get("/status", headers={"Authorization": "Bearer "}).status_code == 503
