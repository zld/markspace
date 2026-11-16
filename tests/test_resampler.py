"""Tests for the from-scratch polyphase resampler."""

from __future__ import annotations

import numpy as np
import pytest

from audioprep.resampler import (
    HALF_TAPS,
    ResampleError,
    StreamingResampler,
    build_plan,
    output_frame_count,
)


def _resample(signal: np.ndarray, sr_in: int, sr_out: int, block: int = 1024):
    plan = build_plan(sr_in, sr_out)
    state = StreamingResampler(plan)
    pieces: list[np.ndarray] = []
    data = signal[:, None] if signal.ndim == 1 else signal
    for start in range(0, len(data), block):
        pieces.append(state.process_block(data[start : start + block]))
    pieces.append(state.flush())
    return np.concatenate(pieces)


@pytest.mark.parametrize(
    "sr_in,sr_out",
    [
        (48000, 24000),
        (24000, 48000),
        (44100, 48000),
        (48000, 48000),
        (32000, 48000),
        (44100, 24000),
        (8000, 192000),
        (192000, 8000),
    ],
)
@pytest.mark.parametrize("block", [1, 7, 1024, 4096])
def test_output_length_matches_ceil_ratio(sr_in, sr_out, block):
    frames = 10007
    rng = np.random.default_rng(0)
    out = _resample(rng.normal(size=frames), sr_in, sr_out, block)
    assert out.shape[0] == output_frame_count(frames, sr_in, sr_out)
    assert out.shape[1] == 1


def test_passthrough_is_identity():
    x = np.linspace(-0.5, 0.5, 5000)
    out = _resample(x, 48000, 48000)[:, 0]
    assert np.allclose(out, x)


def test_dc_gain_is_unit_up_and_down():
    up = _resample(np.full(20000, 0.7), 44100, 48000)[1000:-1000]
    down = _resample(np.full(20000, 0.7), 48000, 24000)[1000:-1000]
    assert abs(up.mean() - 0.7) < 1e-9
    assert abs(down.mean() - 0.7) < 1e-9


def test_low_band_tone_survives_downsampling():
    sr = 48000
    t = np.arange(sr) / sr
    out = _resample(0.8 * np.sin(2 * np.pi * 1000 * t), sr, 24000)[:, 0]
    t2 = np.arange(len(out)) / 24000
    err = np.abs(out[2000:-2000] - 0.8 * np.sin(2 * np.pi * 1000 * t2[2000:-2000]))
    assert err.max() < 1e-3


def test_alias_above_output_nyquist_is_rejected():
    sr = 48000
    t = np.arange(sr) / sr
    out = _resample(0.8 * np.sin(2 * np.pi * 18000 * t), sr, 24000)
    assert np.sqrt(np.mean(out[1000:-1000] ** 2)) < 1e-3


def test_extreme_decimation_rejects_alias_keeps_band():
    n = 192000
    t = np.arange(n) / n
    kept = _resample(0.6 * np.sin(2 * np.pi * 500 * t), 192000, 8000)[:, 0]
    ideal = 0.6 * np.sin(2 * np.pi * 500 * np.arange(8000) / 8000)
    assert np.abs(kept[500:-500] - ideal[500:-500]).max() < 5e-3
    rejected = _resample(0.6 * np.sin(2 * np.pi * 20000 * t), 192000, 8000)
    assert np.sqrt(np.mean(rejected[200:-200] ** 2)) < 1e-3


@pytest.mark.parametrize("frames", [0, 1, 2, 3])
def test_tiny_inputs_trim_to_nominal_length(frames):
    x = np.zeros(frames)
    assert len(_resample(x, 48000, 24000)) == output_frame_count(frames, 48000, 24000)
    assert len(_resample(x, 24000, 48000)) == output_frame_count(frames, 24000, 48000)


def test_plan_rates_are_validated():
    with pytest.raises(ResampleError):
        build_plan(7999, 48000)
    with pytest.raises(ResampleError):
        build_plan(48000, 192001)


def test_plan_coefficients_are_read_only():
    plan = build_plan(44100, 48000)
    assert plan.phases.shape == (plan.up, 2 * HALF_TAPS)
    with pytest.raises(ValueError):
        plan.phases[0, 0] = 9.0


def test_shared_plan_keeps_independent_phase_state():
    plan = build_plan(48000, 24000)
    signal = np.sin(2 * np.pi * 440 * np.arange(4800) / 48000)[:, None]
    a = StreamingResampler(plan)
    b = StreamingResampler(plan)
    out_a = np.concatenate(
        [a.process_block(signal[:2400]), a.process_block(signal[2400:]), a.flush()]
    )
    out_b = np.concatenate([b.process_block(signal), b.flush()])
    assert np.allclose(out_a, out_b)


def test_streaming_chunking_matches_one_shot():
    plan = build_plan(44100, 48000)
    x = np.sin(2 * np.pi * 440 * np.arange(8820) / 44100)[:, None]
    a = StreamingResampler(plan)
    chunked = np.concatenate(
        [a.process_block(x[i : i + 37]) for i in range(0, len(x), 37)] + [a.flush()]
    )
    b = StreamingResampler(plan)
    one_shot = np.concatenate([b.process_block(x), b.flush()])
    assert chunked.shape == one_shot.shape
    assert np.allclose(chunked, one_shot)


def test_cutoff_tracks_rate_ratio():
    # Downsampling keeps the narrower input band fraction; upsampling keeps all.
    down = build_plan(48000, 24000)
    up = build_plan(24000, 48000)
    assert down.cutoff_beta == pytest.approx(0.5)
    assert up.cutoff_beta == pytest.approx(1.0)


def test_channel_count_change_is_rejected():
    state = StreamingResampler(build_plan(48000, 24000))
    state.process_block(np.zeros((10, 2)))
    with pytest.raises(ResampleError):
        state.process_block(np.zeros((10, 1)))
