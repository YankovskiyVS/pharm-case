"""Synthetic signals verify measurements; these tests make no clinical claims."""

from __future__ import annotations

import math

import numpy as np
import pytest

from analysis import RATE, finite, measure


def test_known_pitch_and_missing_transcript_metrics() -> None:
    t = np.arange(6 * RATE) / RATE
    signal = (0.2 * np.sin(2 * np.pi * 180 * t)).astype(np.float32)
    result = measure(signal, "vowel")
    assert result["usable"]
    assert result["metrics"]["f0_median_hz"] == pytest.approx(180, abs=2)
    assert result["metrics"]["words_per_minute"] is None
    assert result["metrics"]["jitter_local"] is not None
    assert result["metrics"]["jitter_local"] < 0.01


def test_silence_has_no_pitch_or_diagnosis() -> None:
    result = measure(np.zeros(6 * RATE, dtype=np.float32), "vowel")
    assert not result["usable"]
    assert "low_level" in result["warnings"]
    assert "insufficient_voice" in result["warnings"]
    assert result["metrics"]["f0_median_hz"] is None


def test_clipping_suppresses_acoustic_interpretation() -> None:
    signal = np.ones(6 * RATE, dtype=np.float32)
    result = measure(signal, "vowel")
    assert not result["usable"]
    assert "clipping" in result["warnings"]
    assert result["metrics"]["jitter_local"] is None


@pytest.mark.parametrize("length", [RATE, 121 * RATE])
def test_duration_is_bounded(length: int) -> None:
    with pytest.raises(ValueError):
        measure(np.zeros(length, dtype=np.float32), "vowel")


def test_nonfinite_values_remain_missing() -> None:
    assert finite(math.nan) is None
    assert finite(math.inf) is None
    signal = np.zeros(6 * RATE, dtype=np.float32)
    signal[0] = np.nan
    with pytest.raises(ValueError):
        measure(signal, "vowel")
