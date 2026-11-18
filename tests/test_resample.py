"""Numerical and boundary tests for the polyphase resampling kernel."""

import numpy as np
import pytest

from audiopack.resample import ResamplerKernel, output_length


def sine(freq, rate, frames, amp=0.8):
    t = np.arange(frames) / rate
    return amp * np.sin(2 * np.pi * freq * t)


def dominant_freq(x, rate):
    seg = x[len(x) // 4: -len(x) // 4] if len(x) > 8 else x
    spec = np.abs(np.fft.rfft(seg))
    return float(np.argmax(spec)) * rate / len(seg)


class TestOutputLength:
    @pytest.mark.parametrize(
        "frames,src,dst,expected",
        [
            (44100, 44100, 48000, 48000),
            (48000, 48000, 44100, 44100),
            (1000, 48000, 48000, 1000),
            (1, 44100, 48000, 1),
            (3, 48000, 16000, 1),
            (0, 44100, 48000, 0),
        ],
    )
    def test_rounding(self, frames, src, dst, expected):
        assert output_length(frames, src, dst) == expected

    def test_matches_ratio_rounding(self):
        for frames in (1, 7, 100, 12345, 96000):
            got = output_length(frames, 44100, 48000)
            assert abs(got - frames * 48000 / 44100) <= 0.5


class TestPassthrough:
    def test_same_rate_is_bit_exact(self):
        x = sine(440, 48000, 4096).astype(np.float32)
        kernel = ResamplerKernel(48000, 48000)
        y = kernel.resample_channel(x)
        np.testing.assert_array_equal(y, x.astype(np.float64))

    def test_interleaved_passthrough(self):
        frames = np.stack([sine(440, 48000, 512), sine(550, 48000, 512)], axis=1)
        y = ResamplerKernel(48000, 48000).resample(frames)
        np.testing.assert_allclose(y, frames, atol=1e-12)


class TestChannelIndependence:
    def test_impulse_does_not_leak_across_channels(self):
        frames = np.zeros((4096, 2))
        frames[2048, 0] = 1.0
        y = ResamplerKernel(44100, 48000).resample(frames)
        assert np.max(np.abs(y[:, 0])) > 0.5
        np.testing.assert_array_equal(y[:, 1], np.zeros(y.shape[0]))

    def test_distinct_sines_stay_separated(self):
        rate_in, rate_out, n = 44100, 48000, 44100
        left = sine(440, rate_in, n)
        right = sine(3200, rate_in, n)
        y = ResamplerKernel(rate_in, rate_out).resample(
            np.stack([left, right], axis=1)
        )
        assert dominant_freq(y[:, 0], rate_out) == pytest.approx(440, abs=2)
        assert dominant_freq(y[:, 1], rate_out) == pytest.approx(3200, abs=2)
        # Energy at the other channel's frequency stays negligible.
        for ch, other in ((0, 3200), (1, 440)):
            seg = y[len(y) // 4: -len(y) // 4, ch]
            spec = np.abs(np.fft.rfft(seg)) ** 2
            freqs = np.fft.rfftfreq(len(seg), 1 / rate_out)
            leak = spec[np.abs(freqs - other) < 20].sum()
            total = spec.sum()
            assert leak / total < 1e-3


class TestFrequencyAndPhase:
    @pytest.mark.parametrize("src,dst", [(44100, 48000), (48000, 44100), (48000, 16000), (16000, 48000)])
    def test_sine_frequency_preserved(self, src, dst):
        freq = 997 if dst >= 16000 else 997
        x = sine(freq, src, src)
        y = ResamplerKernel(src, dst).resample_channel(x)
        assert dominant_freq(y, dst) == pytest.approx(freq, rel=0.01)
        rms = float(np.sqrt(np.mean(y[len(y) // 4: -len(y) // 4] ** 2)))
        assert rms == pytest.approx(0.8 / np.sqrt(2), rel=0.02)

    def test_impulse_position_maps_with_continuous_phase(self):
        src, dst, n = 44100, 48000, 44100
        x = np.zeros(n)
        x[n // 2] = 1.0
        y = ResamplerKernel(src, dst).resample_channel(x)
        peak = int(np.argmax(np.abs(y)))
        expected = (n // 2) * dst / src
        assert abs(peak - expected) <= 1

    def test_deterministic_repeated_runs(self):
        x = sine(440, 44100, 8192)
        kernel = ResamplerKernel(44100, 48000)
        np.testing.assert_array_equal(
            kernel.resample_channel(x), kernel.resample_channel(x)
        )


class TestBoundaries:
    def test_out_of_range_samples_are_zero_padded(self):
        # Impulse at the very last input frame: tail must decay, not wrap.
        x = np.zeros(512)
        x[-1] = 1.0
        y = ResamplerKernel(48000, 96000).resample_channel(x)
        assert np.isfinite(y).all()
        assert np.argmax(np.abs(y)) >= y.shape[0] - 4

    def test_filter_support_is_finite(self):
        # Beyond the impulse response support the output is exactly zero.
        kernel = ResamplerKernel(48000, 96000)
        x = np.zeros(512)
        x[100] = 1.0
        y = kernel.resample_channel(x)
        last_touched_input = 100 + kernel.half
        first_silent_output = 2 * (last_touched_input + 1)
        np.testing.assert_array_equal(y[first_silent_output:], np.zeros(y.shape[0] - first_silent_output))

    def test_short_input(self):
        x = np.array([1.0, -1.0, 0.5])
        y = ResamplerKernel(44100, 48000).resample_channel(x)
        assert y.shape[0] == output_length(3, 44100, 48000)
        assert np.isfinite(y).all()

    def test_empty_input(self):
        y = ResamplerKernel(44100, 48000).resample_channel(np.zeros(0))
        assert y.shape[0] == 0

    def test_tail_length_stable_across_sizes(self):
        for n in (1000, 1001, 1002):
            y = ResamplerKernel(44100, 48000).resample_channel(sine(440, 44100, n))
            assert y.shape[0] == output_length(n, 44100, 48000)
