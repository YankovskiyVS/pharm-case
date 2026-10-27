"""Deterministic local model management, readiness and explicit public prewarming."""

from __future__ import annotations

import fcntl
import hashlib
import importlib.metadata
import json
import os
import threading
import time
from collections.abc import Callable
from dataclasses import dataclass
from functools import lru_cache
from pathlib import Path
from typing import Any, Literal

import onnxruntime as ort

from fastembed import TextEmbedding
from faster_whisper import WhisperModel
from faster_whisper.utils import download_model

# Disable runtime telemetry before model construction or any patient inference.
ort.disable_telemetry_events()

ModelKind = Literal["asr", "embedding"]


class ModelsUnavailable(RuntimeError):
    """The selected public model is not prepared in the local model volume."""


@dataclass(frozen=True)
class Settings:
    """All model/cache paths are explicit and shared by CLI and service processes."""

    model_dir: Path
    cache_dir: Path
    asr_model: str
    embed_model: str
    asr_revision: str | None = None

    @classmethod
    def environment(cls) -> Settings:
        """Read runtime values without depending on a writable home directory."""
        return cls(
            Path(os.getenv("AUDIO_MODEL_DIR", "/models")),
            Path(os.getenv("AUDIO_CACHE_DIR", "/cache")),
            os.getenv("AUDIO_MODEL", "base"),
            os.getenv(
                "AUDIO_EMBED_MODEL", "sentence-transformers/paraphrase-multilingual-MiniLM-L12-v2"
            ),
            os.getenv("AUDIO_MODEL_REVISION") or None,
        )

    @property
    def asr_path(self) -> Path:
        """An alias/revision maps to one deterministic model directory."""
        key = hashlib.sha256(f"{self.asr_model}:{self.asr_revision}".encode()).hexdigest()[:20]
        return self.model_dir / "whisper" / key

    @property
    def embedding_path(self) -> Path:
        return self.model_dir / "embeddings"


class ModelRuntime:
    """Load CPU models once and expose progress without clinical text or credentials."""

    def __init__(self, settings: Settings) -> None:
        self.settings = settings
        self.asr: WhisperModel | None = None
        self.embedder: TextEmbedding | None = None
        self.embedding_key = ""
        self.asr_revision = ""
        self._guard = threading.RLock()
        self._load_locks = {"asr": threading.Lock(), "embedding": threading.Lock()}
        self._models: dict[str, dict[str, Any]] = {
            "asr": {"state": "not_loaded", "model": settings.asr_model, "error_code": ""},
            "embedding": {"state": "not_loaded", "model": settings.embed_model, "error_code": ""},
        }
        self._active = {"analysis": 0, "search": 0}
        self._overdue = {"analysis": False, "search": False}
        self._progress: Callable[[dict[str, Any]], None] | None = None

    def _prepare_paths(self) -> None:
        self.settings.model_dir.mkdir(parents=True, exist_ok=True, mode=0o700)
        self.settings.cache_dir.mkdir(parents=True, exist_ok=True, mode=0o700)

    def _publish(self, kind: ModelKind, state: str, error: str = "") -> None:
        with self._guard:
            self._models[kind].update(state=state, error_code=error, updated_at=time.time())
            self._prepare_paths()
            path = self.settings.model_dir / f"{kind}-state.json"
            temporary = path.with_suffix(f".{os.getpid()}.{threading.get_ident()}.tmp")
            temporary.write_text(json.dumps(self._models[kind], ensure_ascii=False))
            temporary.chmod(0o600)
            temporary.replace(path)
            if self._progress:
                self._progress(self.status())

    def _artifact_fingerprint(self) -> str:
        """Changing ONNX weights invalidates cached embeddings, even under the same alias."""
        paths = sorted(self.settings.embedding_path.rglob("*.onnx"))
        if not paths:
            raise ModelsUnavailable("embedding_artifact_missing")
        digest = hashlib.sha256()
        for path in paths:
            digest.update(path.relative_to(self.settings.embedding_path).as_posix().encode())
            with path.open("rb") as source:
                while part := source.read(1024 * 1024):
                    digest.update(part)
        return digest.hexdigest()

    def load_asr(self, download: bool = False) -> WhisperModel:
        """User requests only load prepared files; downloads require explicit prewarm."""
        with self._load_locks["asr"]:
            if self.asr is not None:
                return self.asr
            self._prepare_paths()
            self._publish("asr", "downloading" if download else "loading")
            try:
                # A process lock prevents a one-shot warmup and service racing a download.
                with (self.settings.model_dir / "asr.lock").open("a") as lock:
                    fcntl.flock(lock, fcntl.LOCK_EX)
                    path = self.settings.asr_path
                    if download:
                        download_model(
                            self.settings.asr_model,
                            output_dir=str(path),
                            cache_dir=str(self.settings.model_dir / "huggingface"),
                            revision=self.settings.asr_revision,
                        )
                    if not (path / "model.bin").is_file():
                        raise ModelsUnavailable("asr_files_missing")
                    self._publish("asr", "loading")
                    model = WhisperModel(
                        str(path),
                        device="cpu",
                        compute_type="int8",
                        cpu_threads=2,
                        local_files_only=True,
                    )
                    digest = hashlib.sha256()
                    for artifact in sorted(path.iterdir()):
                        if artifact.is_file() and artifact.suffix in {".bin", ".json", ".txt"}:
                            digest.update(artifact.name.encode())
                            with artifact.open("rb") as source:
                                while part := source.read(1024 * 1024):
                                    digest.update(part)
                    self.asr_revision = "sha256:" + digest.hexdigest()
                self.asr = model
                self._publish("asr", "ready")
                return model
            except Exception as error:
                # Model loaders raise provider-specific classes. Persist safe codes only.
                self._publish("asr", "failed", "asr_model_unavailable")
                raise ModelsUnavailable("asr_model_unavailable") from error

    def load_embedding(self, download: bool = False) -> TextEmbedding:
        """Prepare/query the same fastembed cache directory from both CLI and runtime."""
        with self._load_locks["embedding"]:
            if self.embedder is not None:
                return self.embedder
            self._prepare_paths()
            self._publish("embedding", "downloading" if download else "loading")
            try:
                with (self.settings.model_dir / "embedding.lock").open("a") as lock:
                    fcntl.flock(lock, fcntl.LOCK_EX)
                    if not download and not any(self.settings.embedding_path.rglob("*.onnx")):
                        raise ModelsUnavailable("embedding_files_missing")
                    model = TextEmbedding(
                        self.settings.embed_model,
                        cache_dir=str(self.settings.embedding_path),
                        threads=2,
                        local_files_only=not download,
                    )
                    # Force initialization/warm inference before readiness is declared.
                    list(model.embed(["Проверка готовности модели."], batch_size=1))
                    fingerprint = self._artifact_fingerprint()
                self.embedding_key = ":".join(
                    [
                        self.settings.embed_model,
                        importlib.metadata.version("fastembed"),
                        fingerprint,
                    ]
                )
                self.embedder = model
                self._publish("embedding", "ready")
                return model
            except Exception as error:
                self._publish("embedding", "failed", "embedding_model_unavailable")
                raise ModelsUnavailable("embedding_model_unavailable") from error

    def prewarm(
        self, download: bool = False, progress: Callable[[dict[str, Any]], None] | None = None
    ) -> bool:
        """Attempt both models independently and report safe per-model stage progress."""
        okay = True
        self._progress = progress
        for loader in (self.load_asr, self.load_embedding):
            try:
                if progress:
                    progress(self.status())
                loader(download)
            except ModelsUnavailable:
                okay = False
            if progress:
                progress(self.status())
        self._progress = None
        return okay

    def active(self, lane: str, delta: int, overdue: bool = False) -> None:
        with self._guard:
            self._active[lane] += delta
            self._overdue[lane] = overdue

    def mark_overdue(self, lane: str) -> None:
        with self._guard:
            self._overdue[lane] = True

    def status(self) -> dict[str, Any]:
        """Readiness means in-process models are usable, not just cache files exist."""
        with self._guard:
            models = {key: dict(value) for key, value in self._models.items()}
            for kind, state in models.items():
                if state["state"] == "not_loaded":
                    try:
                        disk = json.loads(
                            (self.settings.model_dir / f"{kind}-state.json").read_text()
                        )
                        if isinstance(disk, dict) and disk.get("model") == state["model"]:
                            state["cached"] = disk.get("state") == "ready"
                    except (OSError, ValueError, TypeError):
                        state["cached"] = False
            ready = self.asr is not None and self.embedder is not None
            overdue = any(self._overdue.values())
            warming = any(m["state"] in {"loading", "downloading"} for m in models.values())
            status = (
                "degraded"
                if overdue
                else "ready" if ready else "warming" if warming else "not_ready"
            )
            return {
                "status": status,
                "ready": ready and not overdue,
                "models": models,
                "active": dict(self._active),
                "overdue": dict(self._overdue),
                "pipeline": "praat-v1-vad-silero-16k",
                "cache": {"persistent": True, "model_local_only": True},
            }


@lru_cache(maxsize=4)
def _runtime(settings: Settings) -> ModelRuntime:
    return ModelRuntime(settings)


def runtime() -> ModelRuntime:
    """Select one registry per runtime configuration; tests can isolate temporary paths."""
    return _runtime(Settings.environment())
