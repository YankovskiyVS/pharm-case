"""Metadata fixtures are deliberately not clinical weights and never run a model."""

import json
from pathlib import Path
from typing import Any

import numpy as np
import pytest

from adapter import score
from artifacts import ArtifactError, FILES, UPSTREAM_REVISION, digest, validate, validate_probes
from manage_models import install


def probes() -> dict[str, Any]:
    return {
        "schema_version": 1,
        "aggregation": "participant_mean",
        "scaler_mean": [0.0] * 256,
        "scaler_scale": [1.0] * 256,
        "probes": [{"label": "cat_mood", "coef": [1.0] * 256, "intercept": 0.0}],
    }


def bundle(root: Path) -> Path:
    root.mkdir()
    (root / "weights.safetensors").write_bytes(b"not-a-real-model-metadata-fixture-only")
    (root / "config.json").write_text(
        json.dumps(
            {
                "d_model": 1280,
                "encoder_layers": 32,
                "encoder_attention_heads": 20,
                "encoder_ffn_dim": 5120,
                "num_mel_bins": 80,
                "max_source_positions": 1500,
            }
        )
    )
    (root / "preprocessor_config.json").write_text(
        json.dumps(
            {
                "feature_size": 80,
                "sampling_rate": 16000,
                "hop_length": 160,
                "chunk_length": 30,
                "n_fft": 400,
            }
        )
    )
    (root / "task_map.json").write_text(json.dumps({"unknown": 0}))
    (root / "probes.json").write_text(json.dumps(probes()))
    (root / "manifest.json").write_text(
        json.dumps(
            {
                "schema_version": 1,
                "architecture": "voicefm-whisper-large-v2-contrastive-256",
                "upstream_revision": UPSTREAM_REVISION,
                "source_url": "https://example.org/test-only",
                "weights_license": "test-only",
                "probe_source": "test-only",
                "probe_license": "test-only",
                "research_only": True,
                "files": {name: digest(root / name) for name in FILES},
            }
        )
    )
    return root


def test_metadata_integrity_and_install_do_not_claim_model_readiness(tmp_path: Path) -> None:
    source = bundle(tmp_path / "source")
    assert validate(source).research_only
    destination = tmp_path / "installed"
    install(source, destination)
    assert validate(destination).files == validate(source).files
    with pytest.raises(ArtifactError, match="destination_not_empty"):
        install(source, destination)
    (destination / "weights.safetensors").write_bytes(b"tampered")
    with pytest.raises(ArtifactError, match="artifact_hash_mismatch"):
        validate(destination)


@pytest.mark.parametrize(
    "mutation", ["missing", "traversal", "symlink", "wrong_architecture", "extra"]
)
def test_unsupported_metadata_fails_closed(tmp_path: Path, mutation: str) -> None:
    source = bundle(tmp_path / "source")
    path = source / "manifest.json"
    manifest = json.loads(path.read_text())
    if mutation == "missing":
        (source / "probes.json").unlink()
    elif mutation == "traversal":
        manifest["files"]["../outside"] = "0" * 64
    elif mutation == "symlink":
        target = tmp_path / "external"
        (source / "weights.safetensors").rename(target)
        (source / "weights.safetensors").symlink_to(target)
    elif mutation == "wrong_architecture":
        manifest["architecture"] = "whisper-base"
    else:
        manifest["external_code"] = "malicious.py"
    path.write_text(json.dumps(manifest))
    with pytest.raises(ArtifactError):
        validate(source)


def test_probe_validation_rejects_results_nan_wrong_dimension_and_scale() -> None:
    for invalid in [
        {"cat_mood": 0.95},
        {**probes(), "scaler_scale": [0.0] * 256},
        {**probes(), "scaler_mean": [float("nan")] * 256},
        {**probes(), "scaler_mean": [0.0]},
    ]:
        with pytest.raises(ArtifactError, match="probe_invalid"):
            validate_probes(invalid)
    result = score(np.zeros(256), probes())
    assert result["scores"] == [{"label": "cat_mood", "score": 0.5}]
    value = probes()
    value["probes"][0]["intercept"] = -10000.0
    assert score(np.zeros(256), value)["scores"][0]["score"] == 0.0
    with pytest.raises(ArtifactError, match="invalid_model_output"):
        score(np.asarray([1.0]), probes())
