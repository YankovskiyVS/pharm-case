"""Measured acoustic features; thresholds describe recording usability, not disease."""

from __future__ import annotations

import math
from typing import Any

import numpy as np
import parselmouth
from faster_whisper.vad import VadOptions, get_speech_timestamps
from numpy.typing import NDArray
from parselmouth.praat import call

RATE = 16000
PIPELINE = "praat-v1-vad-silero-16k"


def finite(value: float) -> float | None:
    """JSON has no NaN; undefined measurements remain missing."""
    return round(value, 5) if math.isfinite(value) else None


def measure(samples: NDArray[np.float32], task: str) -> dict[str, Any]:
    """Extract acoustics from unchanged digital amplitude with speech gating."""
    duration = len(samples) / RATE
    if not 2 <= duration <= 120 or not np.isfinite(samples).all():
        raise ValueError("duration_or_samples")
    rms = float(np.sqrt(np.mean(samples.astype(np.float64) ** 2)))
    clipped = float(np.mean(np.abs(samples) >= 0.995))
    # Sustained phonation is a different task: Silero can reject a vowel as non-speech.
    if task == "vowel":
        frames = samples[: len(samples) // 320 * 320].reshape(-1, 320)
        active = np.sqrt(np.mean(frames**2, axis=1)) > max(0.003, rms * 0.2)
        edges = np.diff(np.r_[False, active, False].astype(int))
        spans = [
            {"start": int(a * 320), "end": int(b * 320)}
            for a, b in zip(np.where(edges == 1)[0], np.where(edges == -1)[0])
        ]
    else:
        spans = get_speech_timestamps(
            samples,
            VadOptions(min_speech_duration_ms=250, min_silence_duration_ms=300, speech_pad_ms=30),
            sampling_rate=RATE,
        )
    active_seconds = sum(s["end"] - s["start"] for s in spans) / RATE
    pauses = [
        (b["start"] - a["end"]) / RATE
        for a, b in zip(spans, spans[1:])
        if b["start"] - a["end"] >= RATE * 0.2
    ]
    warnings: list[str] = []
    if duration < 5:
        warnings.append("short_recording")
    if clipped > 0.01:
        warnings.append("clipping")
    if rms < 0.003:
        warnings.append("low_level")
    if active_seconds < 2:
        warnings.append("insufficient_voice")
    # This estimates contrast, not calibrated SNR. Missing nonspeech stays unknown.
    activity_mask = np.zeros(len(samples), dtype=bool)
    for span in spans:
        activity_mask[span["start"] : span["end"]] = True
    contrast = None
    if active_seconds >= 2 and np.count_nonzero(~activity_mask) >= RATE * 0.3:
        noise_power = float(np.mean(samples[~activity_mask].astype(np.float64) ** 2))
        voice_power = float(np.mean(samples[activity_mask].astype(np.float64) ** 2))
        contrast = finite(10 * math.log10(max(voice_power, 1e-12) / max(noise_power, 1e-12)))
        if contrast is not None and contrast < 6:
            warnings.append("low_activity_contrast")
    usable = not warnings
    recording_usable = usable
    metrics: dict[str, float | None] = {
        "duration_seconds": finite(duration),
        "active_seconds": finite(active_seconds),
        "pause_ratio": finite(1 - active_seconds / duration),
        "internal_pause_mean_seconds": finite(float(np.mean(pauses))) if pauses else None,
        "level_dbfs": finite(20 * math.log10(max(rms, 1e-12))),
        "clipped_ratio": finite(clipped),
        "activity_contrast_db": contrast,
        "f0_median_hz": None,
        "f0_sd_hz": None,
        "hnr_db": None,
        "jitter_local": None,
        "shimmer_local": None,
        "words_per_minute": None,
    }
    # Preserve original time intervals: concatenating speech would create fake pulses.
    if usable:
        sound = parselmouth.Sound(samples.astype(np.float64), RATE)
        pitch = sound.to_pitch_ac(pitch_floor=60, pitch_ceiling=500)
        f0 = pitch.selected_array["frequency"]
        times = pitch.xs()
        mask = np.zeros(len(f0), dtype=bool)
        for span in spans:
            mask |= (times >= span["start"] / RATE) & (times < span["end"] / RATE)
        voiced = f0[mask & (f0 > 0)]
        if len(voiced) >= 20:
            metrics["f0_median_hz"] = finite(float(np.median(voiced)))
            metrics["f0_sd_hz"] = finite(float(np.std(voiced)))
        else:
            warnings.append("insufficient_periodic_voice")
            usable = False
        harmonicity = sound.to_harmonicity_cc(minimum_pitch=60)
        h = harmonicity.values[0]
        ht = harmonicity.xs()
        hm = np.zeros(len(h), dtype=bool)
        for span in spans:
            hm |= (ht >= span["start"] / RATE) & (ht < span["end"] / RATE)
        valid_h = h[hm & (h > -100) & np.isfinite(h)]
        if len(valid_h):
            metrics["hnr_db"] = finite(float(np.mean(valid_h)))
        if task == "vowel" and usable:
            longest = max(spans, key=lambda s: s["end"] - s["start"])
            if (longest["end"] - longest["start"]) / RATE >= 2:
                part = sound.extract_part(
                    from_time=longest["start"] / RATE,
                    to_time=longest["end"] / RATE,
                )
                point = call(part, "To PointProcess (periodic, cc)", 60, 500)
                metrics["jitter_local"] = finite(
                    float(call(point, "Get jitter (local)", 0, 0, 0.002, 0.01667, 1.3))
                )
                metrics["shimmer_local"] = finite(
                    float(
                        call([part, point], "Get shimmer (local)", 0, 0, 0.002, 0.01667, 1.3, 1.6)
                    )
                )
    return {
        "pipeline": PIPELINE,
        "praat_version": parselmouth.PRAAT_VERSION,
        "usable": usable,
        "recording_usable": recording_usable,
        "warnings": warnings,
        "metrics": metrics,
        "activity_method": "energy-vowel-v1" if task == "vowel" else "silero-vad",
        "limitations": [
            "Не является медицинским заключением.",
            "Уровень dBFS зависит от микрофона и усиления; это не звуковое давление.",
            "Шум и другой говорящий могут искажать измерения. Автоматическая оценка не подтверждает личность говорящего.",
        ],
    }
