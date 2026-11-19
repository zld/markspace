"""Boundary tests for the resampling kernel."""
import numpy as np
import pytest

from audionorm.resampler import (BLOCK_FRAMES, estimate_true_peak,
                                 output_length, resample)


def sine(rate, seconds, freq, amp=0.5, frames=None):
    n = frames if frames is not None else int(round(rate * seconds))
    t = np.arange(n, dtype=np.float64) / rate
    return amp * np.sin(2.0 * np.pi * freq * t)


def test_output_length_ratio():
    assert output_length(44100, 44100, 48000) == 48000
    assert output_length(44101, 44100, 48000) == 48001  # odd input length
    assert output_length(1, 44100, 48000) == 1
    assert output_length(0, 44100, 48000) == 0
    assert output_length(48000, 48000, 44100) == 44100


def test_upsample_preserves_tone_level():
    x = sine(44100, 1.0, 1000.0)
    y = resample(x, 44100, 48000)
    assert y.shape == (48000, 1)
    steady = y[480:-480, 0]
    assert abs(np.sqrt(np.mean(steady**2)) - 0.5 / np.sqrt(2)) < 0.01


def test_downsample_attenuates_above_nyquist():
    # 20 kHz is above the 16 kHz Nyquist of the 32 kHz target.
    x = sine(48000, 0.5, 20000.0)
    y = resample(x, 48000, 32000)
    assert np.sqrt(np.mean(y[320:-320, 0] ** 2)) < 1e-3


def test_identity_rate_roundtrip():
    x = sine(48000, 0.25, 500.0)
    y = resample(x, 48000, 48000)
    assert y.shape == x.shape
    np.testing.assert_allclose(y[100:-100, 0], x[100:-100, 0], atol=1e-3)


def test_channels_do_not_crosstalk():
    left = sine(44100, 0.5, 440.0)
    right = np.zeros_like(left)
    x = np.stack([left, right], axis=1)
    y = resample(x, 44100, 48000)
    assert y.shape == (24000, 2)
    assert np.max(np.abs(y[:, 1])) == 0.0
    # Channel 0 matches a mono resample of the same signal exactly.
    np.testing.assert_array_equal(y[:, 0], resample(left, 44100, 48000)[:, 0])


def test_distinct_channels_stay_separated():
    x = np.stack([sine(44100, 0.5, 440.0), sine(44100, 0.5, 2000.0)], axis=1)
    y = resample(x, 44100, 48000)
    np.testing.assert_array_equal(y[:, 0], resample(x[:, 0], 44100, 48000)[:, 0])
    np.testing.assert_array_equal(y[:, 1], resample(x[:, 1], 44100, 48000)[:, 0])


def test_tail_not_multiple_of_block():
    # Length deliberately not a multiple of BLOCK_FRAMES, and odd.
    n = BLOCK_FRAMES * 2 + 1233
    x = sine(44100, 0, 440.0, frames=n)
    y = resample(x, 44100, 48000)
    assert y.shape[0] == output_length(n, 44100, 48000)
    # Interior agrees with a full-length reference computed in one shot.
    ref = resample(x, 44100, 48000, block=max(n, 1))
    np.testing.assert_allclose(y[:, 0], ref[:, 0], atol=1e-10)


def test_block_size_independence():
    x = sine(48000, 0.3, 700.0)
    a = resample(x, 48000, 32000, block=512)
    b = resample(x, 48000, 32000, block=8192)
    np.testing.assert_allclose(a, b, atol=1e-10)


def test_single_frame_and_empty():
    y = resample(np.array([[0.5, -0.5]]), 44100, 48000)
    assert y.shape == (1, 2)
    empty = resample(np.zeros((0, 2)), 44100, 48000)
    assert empty.shape == (0, 2)


def test_rejects_nonpositive_rates():
    with pytest.raises(ValueError):
        resample(np.zeros((10, 1)), 0, 48000)
    with pytest.raises(ValueError):
        resample(np.zeros((10, 1)), 44100, -1)


def test_true_peak_estimate():
    x = sine(48000, 0.1, 1000.0, amp=0.25)
    assert estimate_true_peak(x, 48000) == pytest.approx(0.25, abs=0.01)
    assert estimate_true_peak(np.zeros((0, 1)), 48000) == 0.0
