"""Offline adapter of the pinned MIT VoiceFM contrastive Whisper encoder.

Architecture/forward follow upstream src/models/whisper_encoder.py at the pinned
revision. Its from_pretrained constructor is replaced by local config construction
and STRICT COMPLETE checkpoint loading. No pretrained backbone download/fallback.
See upstream/LICENSE. Fitted probes follow unified_gsd_probes_v3.py's scaler+LR.
"""

from __future__ import annotations

import importlib
from pathlib import Path
from typing import Any

import numpy as np
from numpy.typing import NDArray

from artifacts import ArtifactError, Manifest, read_json, validate_probes


def dependencies() -> bool:
    """Lightweight default image deliberately has no heavyweight model runtime."""
    return all(
        importlib.util.find_spec(n) is not None
        for n in ("torch", "torchaudio", "transformers", "safetensors", "soundfile")
    )


class Adapter:
    """Strict CPU inference; no randomly initialized tensor can survive load_state_dict."""

    def __init__(self, directory: Path, manifest: Manifest) -> None:
        if not dependencies():
            raise ArtifactError("runtime_not_installed")
        self.torch = importlib.import_module("torch")
        self.torch.set_num_threads(2)
        self.torchaudio = importlib.import_module("torchaudio")
        transformers = importlib.import_module("transformers")
        whisper = importlib.import_module("transformers.models.whisper.modeling_whisper")
        safe = importlib.import_module("safetensors.torch")
        # safetensors cannot execute pickle. No torch.load in the inference path.
        state = safe.load_file(str(directory / "weights.safetensors"), device="cpu")
        keys = set(state)
        if not keys or any(
            not k.startswith(("encoder.", "projection.", "task_embedding.")) for k in keys
        ):
            raise ArtifactError("unsupported_checkpoint")
        task_weight = state.get("task_embedding.weight")
        if task_weight is None or len(task_weight.shape) != 2 or task_weight.shape[1] != 1280:
            raise ArtifactError("unsupported_checkpoint")
        config = transformers.WhisperConfig.from_json_file(str(directory / "config.json"))
        self.encoder = whisper.WhisperEncoder(config)
        nn = self.torch.nn
        self.projection = nn.Sequential(
            nn.Linear(1280, 1280), nn.GELU(), nn.LayerNorm(1280), nn.Linear(1280, 256)
        )
        self.tasks = nn.Embedding(task_weight.shape[0], 1280)
        for name, module in [
            ("encoder", self.encoder),
            ("projection", self.projection),
            ("task_embedding", self.tasks),
        ]:
            selected = {
                k.removeprefix(name + "."): v for k, v in state.items() if k.startswith(name + ".")
            }
            # strict=True checks every expected parameter/buffer and rejects extras.
            module.load_state_dict(selected, strict=True)
            module.float().eval().requires_grad_(False)
        del state
        self.feature_extractor = transformers.WhisperFeatureExtractor.from_pretrained(
            str(directory), local_files_only=True
        )
        self.task_map = read_json(directory / "task_map.json")
        if any(v >= self.tasks.num_embeddings for v in self.task_map.values()):
            raise ArtifactError("task_map_invalid")
        self.probes = validate_probes(read_json(directory / "probes.json"))
        self.manifest = manifest

    def embedding(self, path: Path, task: str) -> NDArray[np.float64]:
        """Match upstream mono, sinc-resampling, -40dB trim, peak norm and 30s trim."""
        # The worker decodes compressed formats to float32 WAV with original rate/channels.
        # torchaudio resampling is identical to upstream, not an ffmpeg approximation.
        soundfile = importlib.import_module("soundfile")
        values, rate = soundfile.read(str(path), dtype="float32", always_2d=True)
        if not np.isfinite(values).all() or values.size == 0:
            raise ArtifactError("invalid_audio")
        waveform = self.torch.from_numpy(values.T.copy()).mean(dim=0)
        if rate != 16000:
            waveform = self.torchaudio.transforms.Resample(rate, 16000)(waveform)
        above = self.torch.nonzero(waveform.abs() > 0.01).squeeze()
        if above.ndim > 0 and above.numel() > 1:
            waveform = waveform[int(above[0]) : int(above[-1]) + 1]
        if waveform.numel() < 400 or float(waveform.abs().max()) <= 0.001:
            raise ArtifactError("insufficient_audio")
        waveform = waveform / waveform.abs().max()
        waveform = waveform[:480000]
        # Upstream probe batch length affects masking. A single recording corresponds
        # to batch_size=1; all1500 tokens are pooled, exactly as upstream forward.
        features = self.feature_extractor(
            waveform.numpy(), sampling_rate=16000, return_tensors="pt"
        ).input_features
        task_id = self.task_map.get(task)
        if task_id is None:
            raise ArtifactError("unsupported_task")
        with self.torch.inference_mode():
            hidden = self.encoder(features.float(), return_dict=True).last_hidden_state
            pooled = hidden.mean(dim=1) + self.tasks(self.torch.tensor([task_id]))
            output = self.torch.nn.functional.normalize(self.projection(pooled), p=2, dim=-1)
        result = np.asarray(output[0].cpu().numpy(), dtype=np.float64)
        if result.shape != (256,) or not np.isfinite(result).all():
            raise ArtifactError("invalid_model_output")
        return result

    def infer(self, path: Path, task: str) -> dict[str, Any]:
        """Apply real fitted StandardScaler and binary logistic probes, never AUROC values."""
        embedding = self.embedding(path, task)
        return score(embedding, self.probes)


def score(embedding: NDArray[np.float64], probes: dict[str, Any]) -> dict[str, Any]:
    """Numerically stable equivalent of fitted sklearn scaler+predict_proba[:,1]."""
    validate_probes(probes)
    if embedding.shape != (256,) or not np.isfinite(embedding).all():
        raise ArtifactError("invalid_model_output")
    scaled = (embedding - np.asarray(probes["scaler_mean"])) / np.asarray(probes["scaler_scale"])
    scores = []
    for probe in probes["probes"]:
        value = float(np.dot(scaled, probe["coef"]) + probe["intercept"])
        probability = float(np.exp(-np.logaddexp(0, -value)))
        scores.append({"label": probe["label"], "score": probability})
    return {
        "scores": scores,
        "embedding_dimension": 256,
        "aggregation": "single_recording",
        "calibrated_for_project": False,
    }
