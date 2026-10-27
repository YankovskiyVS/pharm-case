"""Execution timeouts retain their lane until native work actually terminates."""

import asyncio
import threading
from pathlib import Path

import pytest
from fastapi import HTTPException
from pytest import MonkeyPatch

import service
from model_runtime import ModelRuntime, Settings


def test_slow_analysis_does_not_starve_search_or_create_runaway_threads(
    tmp_path: Path, monkeypatch: MonkeyPatch
) -> None:
    registry = ModelRuntime(Settings(tmp_path / "models", tmp_path / "cache", "base", "test"))
    monkeypatch.setattr(service, "runtime", lambda: registry)
    monkeypatch.setenv("AUDIO_QUEUE_TIMEOUT_SECONDS", "0.01")
    release = threading.Event()

    def slow() -> dict:
        release.wait(3)
        return {"finished": True}

    async def scenario() -> None:
        monkeypatch.setattr(
            service, "lanes", {"analysis": asyncio.Semaphore(1), "search": asyncio.Semaphore(1)}
        )
        try:
            with pytest.raises(HTTPException) as timed:
                await service.execute("analysis", slow, 0.01)
            assert timed.value.status_code == 503
            assert registry.status()["active"]["analysis"] == 1
            with pytest.raises(HTTPException) as busy:
                await service.execute("analysis", lambda: {}, 1)
            assert busy.value.status_code == 429
            result = await service.execute("search", lambda: {"ready": True}, 1)
            assert result == {"ready": True}
        finally:
            release.set()
        async with asyncio.timeout(1):
            while registry.status()["active"]["analysis"]:
                await asyncio.sleep(0.01)
        assert not registry.status()["overdue"]["analysis"]

    asyncio.run(scenario())
