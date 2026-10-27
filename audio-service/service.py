"""Private authenticated ML adapter; no clinical DB access or payload logging."""

from __future__ import annotations

import asyncio
import threading
from contextlib import asynccontextmanager
from collections.abc import AsyncIterator, Callable
import hmac
import os
import subprocess
import sqlite3
import tempfile
from pathlib import Path
from typing import Annotated, Any

import numpy as np
from fastapi import Depends, FastAPI, Header, HTTPException, Request
from pydantic import BaseModel, Field, ConfigDict, model_validator

from analysis import RATE, measure
from retrieval import retrieve
from model_runtime import ModelsUnavailable, runtime
from embedding_cache import cache


@asynccontextmanager
async def lifespan(_app: FastAPI) -> AsyncIterator[None]:
    """Local-only startup warmup runs independently from liveness/status routes."""
    os.umask(0o077)
    if os.getenv("AUDIO_PREWARM_ON_START", "false").lower() == "true":
        threading.Thread(target=runtime().prewarm, daemon=True, name="local-model-warmup").start()
    yield


app = FastAPI(docs_url=None, redoc_url=None, openapi_url=None, lifespan=lifespan)
# Separate execution lanes prevent a long transcription blocking transcript search.
lanes = {"analysis": asyncio.Semaphore(1), "search": asyncio.Semaphore(1)}


def budget(name: str, default: float, maximum: float) -> float:
    """Technical budgets are configured independently from clinical thresholds."""
    value = float(os.getenv(name, str(default)))
    if not 0 < value <= maximum:
        raise ValueError("invalid_execution_budget")
    return value


async def execute(lane: str, work: Callable[[], dict[str, Any]], seconds: float) -> dict[str, Any]:
    """Bound waiting/response time without releasing a lane while native work runs.

    Python cannot safely kill an inference thread. On a response timeout its lane
    stays occupied until it really finishes, preventing accumulating runaway work.
    Other lanes and readiness/status remain responsive.
    """
    semaphore = lanes[lane]
    try:
        await asyncio.wait_for(semaphore.acquire(), budget("AUDIO_QUEUE_TIMEOUT_SECONDS", 3, 30))
    except TimeoutError:
        raise HTTPException(429, "audio_worker_busy") from None
    models = runtime()
    models.active(lane, 1)
    task = asyncio.create_task(asyncio.to_thread(work))
    deferred = False

    def finished(completed: asyncio.Task[dict[str, Any]]) -> None:
        if not completed.cancelled():
            completed.exception()  # Retrieve errors without logging clinical content.
        models.active(lane, -1)
        semaphore.release()

    try:
        return await asyncio.wait_for(asyncio.shield(task), seconds)
    except TimeoutError:
        models.mark_overdue(lane)
        deferred = True
        task.add_done_callback(finished)
        raise HTTPException(503, "audio_execution_timeout") from None
    except asyncio.CancelledError:
        deferred = True
        task.add_done_callback(finished)
        raise
    finally:
        if not deferred:
            models.active(lane, -1)
            semaphore.release()


def authenticate(authorization: Annotated[str, Header()] = "") -> None:
    """Require a dedicated service credential for every data-bearing request."""
    secret = Path(os.environ["AUDIO_TOKEN_FILE"]).read_text().strip()
    if len(secret) < 16:
        raise HTTPException(503, "service_credential_not_configured")
    if not hmac.compare_digest(authorization, "Bearer " + secret):
        raise HTTPException(401, "unauthorized")


@app.get("/health")
@app.get("/health/live")
def health() -> dict[str, str]:
    """Liveness is distinct from model and cache readiness."""
    return {"status": "ok"}


@app.get("/health/ready")
def ready() -> dict[str, Any]:
    """Only in-process usable models plus writable derived cache mean ready."""
    state = runtime().status()
    try:
        state["cache"].update(cache(runtime().settings.cache_dir).stats())
    except (OSError, ValueError, sqlite3.Error):
        state["ready"] = False
        state["status"] = "degraded"
        state["cache"]["error_code"] = "cache_unavailable"
    if not state["ready"]:
        raise HTTPException(503, detail=state)
    return state


@app.get("/status", dependencies=[Depends(authenticate)])
def status() -> dict[str, Any]:
    """Report model stages, activity and aggregate cache statistics, never texts."""
    state = runtime().status()
    try:
        state["cache"].update(cache(runtime().settings.cache_dir).stats())
    except (OSError, ValueError, sqlite3.Error):
        state["ready"] = False
        state["status"] = "degraded"
        state["cache"]["error_code"] = "cache_unavailable"
    return state


def analyze(data: bytes, task: str) -> dict[str, Any]:
    """Decode locally, measure first, transcribe independently with explicit status."""
    with tempfile.TemporaryDirectory() as directory:
        source = Path(directory) / "input"
        source.write_bytes(data)
        decoded = subprocess.run(
            [
                "ffmpeg",
                "-nostdin",
                "-v",
                "error",
                "-protocol_whitelist",
                "file,pipe",
                "-i",
                str(source),
                "-t",
                "121",
                "-ac",
                "1",
                "-ar",
                str(RATE),
                "-f",
                "f32le",
                "pipe:1",
            ],
            capture_output=True,
            timeout=25,
            check=False,
        )
    if decoded.returncode != 0:
        raise ValueError("invalid_audio")
    samples = np.frombuffer(decoded.stdout, dtype=np.float32).copy()
    result = measure(samples, task)
    result["segments"] = []
    result["transcription_status"] = "not_applicable" if task == "vowel" else "insufficient_audio"
    if task != "vowel" and result["recording_usable"]:
        try:
            models = runtime()
            model_name = models.settings.asr_model
            asr = models.load_asr(download=False)
            segments, info = asr.transcribe(
                samples,
                language="ru",
                beam_size=3,
                vad_filter=True,
                condition_on_previous_text=False,
                word_timestamps=True,
            )
            accepted: list[dict[str, Any]] = [
                {"start": round(s.start, 3), "end": round(s.end, 3), "text": s.text.strip()[:4000]}
                for s in segments
                if s.text.strip()
                and s.end > s.start
                and s.no_speech_prob < 0.6
                and s.avg_logprob > -1.0
            ]
            result["segments"] = accepted
            result["transcription_status"] = "ready" if accepted else "no_confident_speech"
            result["asr_model"] = model_name
            result["asr_revision"] = models.asr_revision
            result["asr_engine"] = "faster-whisper-1.2.1"
            result["language"] = info.language
            if accepted:
                from retrieval import terms

                count = sum(len(terms(s["text"])) for s in accepted)
                result["metrics"]["words_per_minute"] = round(
                    count * 60 / result["metrics"]["duration_seconds"], 3
                )
        except (ModelsUnavailable, RuntimeError, OSError, ValueError):
            # Preserve acoustic results even when model files/inference are unavailable.
            result["transcription_status"] = "failed"
    return result


@app.post("/analyze", dependencies=[Depends(authenticate)])
async def analyze_request(request: Request, task: str = "free") -> dict[str, Any]:
    """Bound input memory and concurrency before decoding untrusted media."""
    if task not in {"free", "reading", "vowel"}:
        raise HTTPException(400, "invalid_task")
    data = bytearray()
    try:
        async with asyncio.timeout(budget("AUDIO_BODY_TIMEOUT_SECONDS", 20, 60)):
            async for part in request.stream():
                data.extend(part)
                if len(data) > 12 * 1024 * 1024:
                    raise HTTPException(413, "too_large")
    except TimeoutError:
        raise HTTPException(408, "audio_upload_timeout") from None
    try:
        return await execute(
            "analysis",
            lambda: analyze(bytes(data), task),
            budget("AUDIO_ANALYSIS_TIMEOUT_SECONDS", 180, 600),
        )
    except (ValueError, subprocess.TimeoutExpired):
        raise HTTPException(422, "invalid_audio") from None


class Chunk(BaseModel):
    """Citation fields are selected by Go after access checks."""

    model_config = ConfigDict(extra="forbid", allow_inf_nan=False)
    id: str = Field(min_length=1, max_length=128)
    recording_id: str = Field(min_length=1, max_length=128)
    start: float = Field(ge=0)
    end: float = Field(gt=0)
    text: str = Field(min_length=1, max_length=4000)

    @model_validator(mode="after")
    def interval(self) -> Chunk:
        """Only valid bounded timestamped citations can enter the retrieval cache."""
        if self.end <= self.start or self.end > 121:
            raise ValueError("invalid_chunk_interval")
        return self


class Search(BaseModel):
    model_config = ConfigDict(extra="forbid")
    query: str = Field(min_length=2, max_length=1000)
    chunks: list[Chunk] = Field(max_length=500)


def search(body: Search) -> dict[str, Any]:
    """Return quotations rather than unconstrained medical generation."""
    if not body.chunks:
        return {
            "status": "insufficient_evidence",
            "sources": [],
            "answer": "Подходящих расшифровок пока нет.",
            "limitations": "Поиск по расшифровкам. Клинический корпус не подключён. Не является медицинским заключением.",
        }
    models = runtime()
    embedder = models.load_embedding(download=False)
    model = models.settings.embed_model
    chunks = [c.model_dump() for c in body.chunks]
    store = cache(models.settings.cache_dir)

    def encode(texts: list[str]) -> list[Any]:
        return list(embedder.embed(texts, batch_size=32))

    vectors, hits = store.vectors(models.embedding_key, chunks, encode)
    question = np.asarray(next(iter(embedder.embed([body.query], batch_size=1))), dtype=np.float32)
    found = retrieve(body.query, chunks, vectors, question.astype(float).tolist())
    return {
        "status": "ready" if found else "insufficient_evidence",
        "answer": "\n".join(f"[{i + 1}] «{c['text']}»" for i, c in enumerate(found))
        or "Достаточных источников для ответа не найдено.",
        "sources": found,
        "pipeline": "bm25-dense-rrf-coverage-cached-v2",
        "cache": {"hits": hits, "computed": len(chunks) - hits},
        "embedding_version": models.embedding_key,
        "embedding_model": model,
        "limitations": "Поиск по расшифровкам. Клинический корпус не подключён. Не является медицинским заключением.",
    }


@app.post("/search", dependencies=[Depends(authenticate)])
async def search_request(body: Search) -> dict[str, Any]:
    """Reuse the bounded local model execution lane."""
    try:
        return await execute(
            "search", lambda: search(body), budget("AUDIO_SEARCH_TIMEOUT_SECONDS", 60, 300)
        )
    except ModelsUnavailable:
        raise HTTPException(503, "models_not_ready") from None
    except (RuntimeError, OSError, ValueError):
        raise HTTPException(503, "retrieval_unavailable") from None


class Invalidate(BaseModel):
    """Go may invalidate derived chunks after a clinical record deletion."""

    model_config = ConfigDict(extra="forbid")
    chunk_ids: list[str] = Field(max_length=1000)


@app.post("/cache/invalidate", dependencies=[Depends(authenticate)])
def invalidate(body: Invalidate) -> dict[str, int]:
    """Derived embeddings can be erased without accessing clinical SQL or text."""
    return {"removed": cache(runtime().settings.cache_dir).invalidate(body.chunk_ids)}
