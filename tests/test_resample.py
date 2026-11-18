"""Deterministic resampling and explicit channel layouts."""

from __future__ import annotations

import numpy as np
import pytest

from mixforge.errors import RenderError
from mixforge.resample import (
    apply_layout,
    ms_to_frames,
    output_frame_count,
    resample,
    resample_range,
    source_window,
    validate_layout,
)

from conftest import tone


def _rms(x: np.ndarray) -> float:
    return float(np.sqrt(np.mean(np.asarray(x, dtype=np.float64) ** 2)))


@pytest.mark.parametrize("src,dst,freq", [
    (44100, 48000, 1000.0), (48000, 44100, 1000.0),
    (22050, 48000, 800.0), (96000, 48000, 2000.0),
])
def test_resample_accuracy(src: int, dst: int, freq: float) -> None:
    x = tone(src, src, freq)[:, None]
    y = resample(x, src, dst)
    expected = output_frame_count(src, src, dst)
    assert y.shape == (expected, 1)
    times = np.arange(expected) / dst
    annual = 0.25 * np.sin(2 * np.pi * freq * times)
    edge = 400
    assert np.max(np.abs(y[edge:-edge, 0] - annual[edge:-edge])) < 1e-4
    assert _rms(y[edge:-edge]) == pytest.approx(_rms(x), rel=1e-3)


def test_resample_is_deterministic() -> None:
    x = tone(4410, 44100, 440.0)[:, None]
    assert np.array_equal(resample(x, 44100, 48000), resample(x, 44100, 48000))


def test_same_rate_is_identity() -> None:
    x = tone(100, 48000, 440.0)[:, None]
    assert np.array_equal(resample(x, 48000, 48000), x)


def test_empty_audio_yields_zero_frames() -> None:
    assert resample(np.zeros((0, 2)), 44100, 48000).shape == (0, 2)


def test_single_frame_is_defined() -> None:
    y = resample(np.ones((1, 1)), 44100, 48000)
    assert y.shape == (1, 1)
    assert np.all(np.isfinite(y))


@pytest.mark.parametrize("block", [1, 2, 3, 257, 1000, 4096])
def test_block_partition_is_bit_identical(block: int) -> None:
    rng = np.random.default_rng(7)
    x = rng.standard_normal((44100, 2)) * 0.1
    full = resample(x, 44100, 48000)
    total = output_frame_count(x.shape[0], 44100, 48000)
    ratio = 44100 / 48000
    out = np.zeros_like(full)
    for start in range(0, total, block):
        end = min(total, start + block)
        lo, hi = source_window(start, end, ratio, x.shape[0])
        out[start:end] = resample_range(x[lo:hi], lo, 44100, 48000, start, end)
    assert np.array_equal(full, out)


def test_source_window_stays_inside_file() -> None:
    assert source_window(0, 10, 44100 / 48000, 100)[0] == 0
    assert source_window(90, 100, 44100 / 48000, 100)[1] == 100


@pytest.mark.parametrize("channels,matrix", [
    (2, ((1.0, 0.0), (0.0, 1.0))),
    (1, ((1.0,), (1.0,))),
    (2, ((0.7, 0.3), (0.2, 0.8))),
])
def test_layout_matches_source(channels, matrix) -> None:
    data = np.arange(4 * channels, dtype=np.float64).reshape(4, channels)
    out = apply_layout(data, matrix, track_id="t")
    assert out.shape == (4, 2)
    for row in range(2):
        expected = sum(data[:, c] * matrix[row][c] for c in range(channels))
        assert np.allclose(out[:, row], expected)


def test_layout_width_mismatch_rejected() -> None:
    with pytest.raises(RenderError, match="channel matrix has 2") as excinfo:
        apply_layout(np.zeros((4, 1)), ((0.5, 0.5), (0.5, 0.5)), track_id="vo")
    assert excinfo.value.track_id == "vo"
    assert excinfo.value.field == "channels"


def test_validate_layout_accepts_explicit_three_channel_matrix() -> None:
    validate_layout(3, ((1.0, 0.0, 0.0), (0.0, 1.0, 0.0)), track_id="t")
    with pytest.raises(RenderError):
        validate_layout(2, ((1.0, 0.0, 0.0), (0.0, 1.0, 0.0)), track_id="t")


def test_ms_to_frames_rounds_half_up() -> None:
    assert ms_to_frames(0, 48000) == 0
    assert ms_to_frames(1000, 48000) == 48000
    assert ms_to_frames(0.5, 48000) == 24
