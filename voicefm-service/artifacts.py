"""Validate an operator-supplied, licensed, immutable offline VoiceFM bundle."""

from __future__ import annotations

import hashlib
import json
import math
from pathlib import Path
from typing import Any, Literal

from pydantic import BaseModel, ConfigDict, Field

UPSTREAM_REVISION = "86d22629bde5d4e15194197da38e9c5323f48da9"
FILES = {
    "weights.safetensors",
    "config.json",
    "preprocessor_config.json",
    "task_map.json",
    "probes.json",
}
LABELS = {
    "gsd_control",
    "cat_voice",
    "cat_neuro",
    "cat_mood",
    "cat_respiratory",
    "gsd_parkinsons",
    "gsd_alz_dementia_mci",
    "gsd_mtd",
    "gsd_copd_asthma",
    "gsd_depression",
    "gsd_airway_stenosis",
    "gsd_benign_lesion",
    "gsd_anxiety",
    "gsd_laryngeal_dystonia",
}
LIMITATIONS = [
    "Research use only. Scores are not a diagnosis and must not determine treatment or urgent care.",
    "VoiceFM clinical checkpoints and fitted probes are separate from the MIT source release.",
    "Russian language, these recording tasks and this patient population have not been validated.",
    "Published probes aggregate participant recordings; a single recording is not a validated clinical assessment.",
]


class ArtifactError(RuntimeError):
    """A safe reason code, never a private path, audio sample or upstream traceback."""


class Manifest(BaseModel):
    """Hash-pinned provenance supplied by an authorized model operator."""

    model_config = ConfigDict(extra="forbid")
    schema_version: Literal[1]
    architecture: Literal["voicefm-whisper-large-v2-contrastive-256"]
    upstream_revision: Literal[UPSTREAM_REVISION]  # type: ignore[valid-type]
    source_url: str = Field(pattern=r"^https://", max_length=1000)
    weights_license: str = Field(min_length=3, max_length=1000)
    probe_source: str = Field(min_length=3, max_length=1000)
    probe_license: str = Field(min_length=3, max_length=1000)
    research_only: Literal[True]
    files: dict[str, str]


def read_json(path: Path) -> Any:
    """Read small metadata without pickle, external code or unbounded input."""
    if path.stat().st_size > 2 * 1024 * 1024:
        raise ArtifactError("unsupported_checkpoint")
    return json.loads(
        path.read_text(), parse_constant=lambda _: (_ for _ in ()).throw(ValueError())
    )


def digest(path: Path) -> str:
    """Hash actual bytes, independent of alias, filename or claimed version."""
    value = hashlib.sha256()
    with path.open("rb") as source:
        while part := source.read(1024 * 1024):
            value.update(part)
    return value.hexdigest()


def vector(value: Any) -> list[float]:
    """Validate a finite 256-dimensional fitted probe vector."""
    if not isinstance(value, list) or len(value) != 256:
        raise ArtifactError("probe_invalid")
    if any(type(x) not in (int, float) or not math.isfinite(x) for x in value):
        raise ArtifactError("probe_invalid")
    return [float(x) for x in value]


def validate_probes(value: Any) -> dict[str, Any]:
    """Only fitted scaler/logistic parameters are supported, never result AUROCs."""
    if not isinstance(value, dict) or set(value) != {
        "schema_version",
        "aggregation",
        "scaler_mean",
        "scaler_scale",
        "probes",
    }:
        raise ArtifactError("probe_invalid")
    if value["schema_version"] != 1 or value["aggregation"] != "participant_mean":
        raise ArtifactError("probe_invalid")
    vector(value["scaler_mean"])
    if any(x <= 0 for x in vector(value["scaler_scale"])):
        raise ArtifactError("probe_invalid")
    probes = value["probes"]
    if not isinstance(probes, list) or not 1 <= len(probes) <= len(LABELS):
        raise ArtifactError("probe_invalid")
    seen = set()
    for probe in probes:
        if not isinstance(probe, dict) or set(probe) != {"label", "coef", "intercept"}:
            raise ArtifactError("probe_invalid")
        if probe["label"] not in LABELS or probe["label"] in seen:
            raise ArtifactError("probe_invalid")
        seen.add(probe["label"])
        vector(probe["coef"])
        if type(probe["intercept"]) not in (int, float) or not math.isfinite(probe["intercept"]):
            raise ArtifactError("probe_invalid")
    return value


def validate(directory: Path) -> Manifest:
    """Reject incomplete, mutable-by-symlink or incompatible bundles before model loading."""
    if not (directory / "manifest.json").is_file():
        raise ArtifactError("not_installed")
    try:
        if (directory / "manifest.json").is_symlink():
            raise ArtifactError("unsupported_checkpoint")
        manifest = Manifest.model_validate(read_json(directory / "manifest.json"))
        if manifest.upstream_revision != UPSTREAM_REVISION or set(manifest.files) != FILES:
            raise ArtifactError("unsupported_checkpoint")
        for name, expected in manifest.files.items():
            path = directory / name
            if not path.is_file():
                raise ArtifactError("probe_missing" if name == "probes.json" else "not_installed")
            if (
                path.is_symlink()
                or len(expected) != 64
                or any(c not in "0123456789abcdef" for c in expected)
            ):
                raise ArtifactError("unsupported_checkpoint")
            if path.stat().st_size > 4 * 1024**3 or digest(path) != expected:
                raise ArtifactError("artifact_hash_mismatch")
        config = read_json(directory / "config.json")
        required = {
            "d_model": 1280,
            "encoder_layers": 32,
            "encoder_attention_heads": 20,
            "encoder_ffn_dim": 5120,
            "num_mel_bins": 80,
            "max_source_positions": 1500,
        }
        if not isinstance(config, dict) or any(config.get(k) != v for k, v in required.items()):
            raise ArtifactError("unsupported_checkpoint")
        fe = read_json(directory / "preprocessor_config.json")
        if not isinstance(fe, dict) or any(
            fe.get(k) != v
            for k, v in {
                "feature_size": 80,
                "sampling_rate": 16000,
                "hop_length": 160,
                "chunk_length": 30,
                "n_fft": 400,
            }.items()
        ):
            raise ArtifactError("unsupported_checkpoint")
        tasks = read_json(directory / "task_map.json")
        if not isinstance(tasks, dict) or tasks.get("unknown") != 0 or len(tasks) > 1000:
            raise ArtifactError("task_map_invalid")
        if any(
            not isinstance(k, str) or len(k) > 128 or type(v) is not int or v < 0
            for k, v in tasks.items()
        ):
            raise ArtifactError("task_map_invalid")
        validate_probes(read_json(directory / "probes.json"))
        return manifest
    except ArtifactError:
        raise
    except (OSError, ValueError, TypeError, KeyError) as error:
        raise ArtifactError("unsupported_checkpoint") from error


def provenance(directory: Path, manifest: Manifest | None = None) -> dict[str, Any]:
    """Return only immutable model identity; do not expose operator-private metadata."""
    return {
        "name": "VoiceFM-Whisper",
        "architecture": "voicefm-whisper-large-v2-contrastive-256",
        "upstream_revision": UPSTREAM_REVISION,
        "revision": digest(directory / "manifest.json") if manifest else "unavailable",
        "artifact_hashes": dict(manifest.files) if manifest else {},
        "research_only": True,
    }
