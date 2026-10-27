"""Private replaceable VoiceFM research adapter and explicitly synthetic PoC fixture."""

from __future__ import annotations

import asyncio
import contextlib
import hashlib
import hmac
import json
import os
import signal
import sys
import tempfile
from collections.abc import AsyncIterator
from contextlib import asynccontextmanager
from pathlib import Path
from typing import Any

from fastapi import Depends, FastAPI, Header, HTTPException, Request
from fastapi.responses import JSONResponse

from adapter import dependencies
from artifacts import ArtifactError, LIMITATIONS, Manifest, provenance, validate

MOCK_DESCRIPTION = (
    "Synthetic demonstration fixture, not a prediction about this patient. "
    "The displayed scores are fixed example data and do not depend on the recording. "
    "They must not be used for diagnosis, risk assessment or treatment."
)
MOCK_SCORES = [
    {"label": "cat_voice", "score": 0.25},
    {"label": "cat_neuro", "score": 0.35},
    {"label": "cat_mood", "score": 0.45},
    {"label": "cat_respiratory", "score": 0.15},
]


def setting(name: str, default: int, maximum: int) -> int:
    """Apply bounded operational budgets even when an environment value is malformed."""
    try:
        return max(1, min(int(os.getenv(name, str(default))), maximum))
    except ValueError:
        return default


class Runtime:
    """No database, patient history or cross-request audio state lives in this service."""

    def __init__(self) -> None:
        self.mode = os.getenv("VOICEFM_MODE", "mock")
        if self.mode not in {"mock", "real", "disabled"}:
            self.mode = "disabled"
        self.directory = Path(os.getenv("VOICEFM_MODEL_DIR", "/models"))
        self.manifest: Manifest | None = None
        self.ready = self.mode == "mock"
        self.reason = (
            "" if self.ready else "disabled" if self.mode == "disabled" else "not_installed"
        )
        self.active = 0
        self.lane = asyncio.Semaphore(1)

    def metadata(self) -> dict[str, Any]:
        """Same provenance envelope in all modes, with mock origin impossible to overlook."""
        is_mock = self.mode == "mock"
        model = (
            {"name": "voicefm-demo-fixture-v1", "revision": "fixture-v1-fixed-scores"}
            if is_mock
            else provenance(self.directory, self.manifest)
        )
        return {
            "mode": self.mode,
            "is_mock": is_mock,
            "clinical_use": False,
            "input_used_for_prediction": False,
            "model": model,
            "research_only": True,
            "limitations": [MOCK_DESCRIPTION] if is_mock else list(LIMITATIONS),
        }

    def status(self) -> dict[str, Any]:
        """Ready mock is a working demo, never a ready clinical model."""
        return {
            **self.metadata(),
            "status": "ready" if self.ready else "model_unavailable",
            "ready": self.ready,
            "reason_code": self.reason,
            "active": self.active,
            "weights_access": (
                "not_required_for_mock" if self.mode == "mock" else "operator_supplied"
            ),
        }

    async def initialize(self) -> None:
        """Verify real artifacts, then actually load the complete CPU model before ready."""
        if self.mode != "real":
            return
        self.reason = "loading"
        try:
            self.manifest = await asyncio.to_thread(validate, self.directory)
            if not dependencies():
                raise ArtifactError("runtime_not_installed")
            await self.operation(None, "unknown")
            self.ready, self.reason = True, ""
        except ArtifactError as error:
            self.ready, self.reason = False, str(error)
        except (OSError, ValueError):
            self.ready, self.reason = False, "unsupported_checkpoint"

    async def operation(self, content: bytes | None, task: str) -> dict[str, Any]:
        """Kill the complete process group on timeout/cancellation, including audio decoder."""
        with tempfile.TemporaryDirectory(prefix="voicefm-") as folder:
            directory = Path(folder)
            output = directory / "result.json"
            command = [
                sys.executable,
                "-m",
                "worker",
                "--bundle",
                str(self.directory),
                "--output",
                str(output),
                "--task",
                task,
            ]
            if content is not None:
                source = directory / "audio.input"
                source.write_bytes(content)
                command += ["--input", str(source)]
            process = await asyncio.create_subprocess_exec(
                *command,
                stdout=asyncio.subprocess.DEVNULL,
                stderr=asyncio.subprocess.DEVNULL,
                start_new_session=True,
            )
            try:
                await asyncio.wait_for(process.wait(), setting("VOICEFM_TIMEOUT_SECONDS", 90, 180))
            except (TimeoutError, asyncio.CancelledError):
                with contextlib.suppress(ProcessLookupError):
                    os.killpg(process.pid, signal.SIGKILL)
                await process.wait()
                raise
            if not output.is_file() or output.stat().st_size > 128 * 1024:
                raise ArtifactError("inference_failed")
            result = json.loads(output.read_text())
            if not isinstance(result, dict) or not result.get("ok"):
                reason = (
                    result.get("reason_code", "inference_failed")
                    if isinstance(result, dict)
                    else "inference_failed"
                )
                raise ArtifactError(reason)
            outputs = result.get("outputs")
            if not isinstance(outputs, dict):
                raise ArtifactError("invalid_model_output")
            return outputs


runtime = Runtime()


@asynccontextmanager
async def lifespan(_app: FastAPI) -> AsyncIterator[None]:
    """Warm a real bundle in the background while exposing explicit loading status."""
    task = asyncio.create_task(runtime.initialize())
    yield
    task.cancel()
    with contextlib.suppress(asyncio.CancelledError):
        await task


app = FastAPI(lifespan=lifespan, docs_url=None, redoc_url=None, openapi_url=None)


def authenticate(authorization: str = Header(default="")) -> None:
    """Dedicated token; absent/short configuration always fails closed."""
    try:
        secret = (
            Path(os.getenv("VOICEFM_TOKEN_FILE", "/run/secrets/voicefm_token")).read_text().strip()
        )
    except OSError:
        secret = ""
    if len(secret) < 32 or not hmac.compare_digest(
        authorization.encode(), ("Bearer " + secret).encode()
    ):
        raise HTTPException(401, "unauthorized")


@app.get("/health/live")
def live() -> dict[str, str]:
    """Public liveness contains no model, clinical or credential metadata."""
    return {"status": "ok"}


@app.get("/health/ready")
def ready() -> JSONResponse:
    """A fixture-ready response explicitly identifies its synthetic mode."""
    return JSONResponse(runtime.status(), status_code=200 if runtime.ready else 503)


@app.get("/status", dependencies=[Depends(authenticate)])
def status() -> dict[str, Any]:
    """Authenticated aggregate status only."""
    return runtime.status()


async def read_audio(request: Request) -> bytes:
    """Bound upload bytes and elapsed time, including absent or dishonest length headers."""
    data = bytearray()
    async with asyncio.timeout(20):
        async for part in request.stream():
            data.extend(part)
            if len(data) > 12 * 1024 * 1024:
                raise HTTPException(413, "audio_too_large")
    if not data:
        raise HTTPException(422, "invalid_audio")
    return bytes(data)


async def validate_audio(content: bytes) -> None:
    """Exercise the audio upload flow without using its content to generate demo scores."""
    with tempfile.TemporaryDirectory(prefix="voicefm-validate-") as folder:
        source = Path(folder) / "audio.input"
        source.write_bytes(content)
        process = await asyncio.create_subprocess_exec(
            "ffmpeg",
            "-nostdin",
            "-v",
            "error",
            "-protocol_whitelist",
            "file,pipe",
            "-format_whitelist",
            "wav,ogg,matroska,webm,mov,mp4,m4a,3gp,3g2,mj2,mp3,aac,flac",
            "-i",
            str(source),
            "-t",
            "121",
            "-f",
            "s16le",
            "-ar",
            "16000",
            "-ac",
            "1",
            "pipe:1",
            stdout=asyncio.subprocess.PIPE,
            stderr=asyncio.subprocess.DEVNULL,
        )
        try:
            output, _ = await asyncio.wait_for(process.communicate(), 15)
        except (TimeoutError, asyncio.CancelledError):
            with contextlib.suppress(ProcessLookupError):
                process.kill()
            await process.wait()
            raise
        if process.returncode or not 800 <= len(output) <= 120 * 16000 * 2:
            raise ArtifactError("invalid_audio")


@app.post("/infer", dependencies=[Depends(authenticate)])
@app.post("/analyze", dependencies=[Depends(authenticate)])
async def analyze(request: Request, x_voice_task: str = Header(default="unknown")) -> JSONResponse:
    """One replaceable contract; only licensed real mode can use audio for predictions."""
    if not runtime.ready:
        return JSONResponse({**runtime.status(), "outputs": {}}, status_code=503)
    if x_voice_task not in {"unknown", "vowel", "reading", "free_speech"}:
        raise HTTPException(422, "unsupported_task")
    try:
        await asyncio.wait_for(runtime.lane.acquire(), setting("VOICEFM_QUEUE_SECONDS", 3, 10))
    except TimeoutError:
        raise HTTPException(429, "inference_busy") from None
    runtime.active = 1
    try:
        content = await read_audio(request)
        if runtime.mode == "mock":
            await validate_audio(content)
            outputs = {
                "scores": [dict(item) for item in MOCK_SCORES],
                "interpretation": MOCK_DESCRIPTION,
                "calibrated_for_project": False,
                "fixture_id": "voicefm-demo-fixture-v1",
            }
        else:
            outputs = await runtime.operation(content, x_voice_task)
        return JSONResponse(
            {
                **runtime.metadata(),
                "status": "ready",
                "outputs": outputs,
                "input_used_for_prediction": runtime.mode == "real",
                "input_sha256": hashlib.sha256(content).hexdigest(),
            }
        )
    except TimeoutError:
        return JSONResponse(
            {
                **runtime.metadata(),
                "status": "model_unavailable",
                "reason_code": "timeout",
                "outputs": {},
            },
            status_code=503,
        )
    except ArtifactError as error:
        code = str(error)
        return JSONResponse(
            {
                **runtime.metadata(),
                "status": "model_unavailable",
                "reason_code": code,
                "outputs": {},
            },
            status_code=(
                422 if code in {"invalid_audio", "insufficient_audio", "unsupported_task"} else 503
            ),
        )
    except (OSError, ValueError):
        return JSONResponse(
            {
                **runtime.metadata(),
                "status": "model_unavailable",
                "reason_code": "inference_failed",
                "outputs": {},
            },
            status_code=503,
        )
    finally:
        runtime.active = 0
        runtime.lane.release()
