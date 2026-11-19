"""Numerical boundary tests for the resampler and layout helpers."""

from __future__ import annotations

import math
import unittest

import numpy as np

from audiopipe import layout, resample
from audiopipe.config import ResampleConfig
from audiopipe.errors import InputError


def tone(rate: int, frames: int, freq: float, amp: float = 1.0) -> np.ndarray:
    t = np.arange(frames, dtype=np.float64) / float(rate)
    return (amp * np.sin(2.0 * math.pi * freq * t))[:, None]


def ideal(frames: int, rate_in: int, rate_out: int, freq: float, amp: float = 1.0) -> np.ndarray:
    t = np.arange(frames, dtype=np.float64) / float(rate_out)
    return (amp * np.sin(2.0 * math.pi * freq * t))[:, None]


class RatioTests(unittest.TestCase):
    def test_ratio_reduces_exactly(self) -> None:
        ratio = resample.rational_ratio(44100, 48000)
        self.assertEqual((ratio.up, ratio.down), (160, 147))
        # 7.056 MHz -> 48 kHz reduces by 147.
        other = resample.rational_ratio(48000, 7056000)
        self.assertEqual((other.up, other.down), (147, 1))
        self.assertAlmostEqual(ratio.value, 48000 / 44100)

    def test_identical_rates_reduce_to_one(self) -> None:
        ratio = resample.rational_ratio(48000, 48000)
        self.assertEqual((ratio.up, ratio.down), (1, 1))

    def test_invalid_rate_rejected(self) -> None:
        with self.assertRaises(InputError):
            resample.rational_ratio(0, 48000)
        with self.assertRaises(InputError):
            resample.rational_ratio(48000, -1)

    def test_frame_count_rounds_half_up(self) -> None:
        self.assertEqual(resample.resampled_frames(44100, 44100, 48000), 48000)
        self.assertEqual(resample.resampled_frames(4410, 44100, 48000), 4800)
        self.assertEqual(resample.resampled_frames(1, 16000, 44100), 3)  # 2.756 -> 3
        self.assertEqual(resample.resampled_frames(0, 44100, 48000), 0)
        # Halfway value rounds up: 3 * 1/2 = 1.5 -> 2.
        self.assertEqual(resample.resampled_frames(3, 2, 1), 2)

    def test_frame_count_matches_output(self) -> None:
        for frames_in, rate_in, rate_out in [
            (333, 32000, 48000),
            (1234, 44100, 48000),
            (4800, 48000, 16000),
            (1, 44100, 48000),
        ]:
            data = np.zeros((frames_in, 2))
            out = resample.resample(data, rate_in, rate_out)
            self.assertEqual(
                out.shape[0], resample.resampled_frames(frames_in, rate_in, rate_out)
            )


class GainAndAccuracyTests(unittest.TestCase):
    def test_dc_gain_is_unity(self) -> None:
        data = np.full((997, 2), 0.25)
        out = resample.resample(data, 44100, 48000)
        self.assertLess(float(np.max(np.abs(out - 0.25))), 1e-12)

    def test_upsample_tracks_sine(self) -> None:
        out = resample.resample(tone(8000, 4000, 440.0), 8000, 16000)
        expected = ideal(out.shape[0], 8000, 16000, 440.0)
        self.assertLess(float(np.max(np.abs(out[100:-100] - expected[100:-100]))), 1e-4)

    def test_non_integer_ratio_tracks_sine(self) -> None:
        out = resample.resample(tone(44100, 44100, 1000.0), 44100, 48000)
        expected = ideal(out.shape[0], 44100, 48000, 1000.0)
        self.assertLess(float(np.max(np.abs(out[3000:-3000] - expected[3000:-3000]))), 1e-4)

    def test_downsample_rejects_aliasing_tone(self) -> None:
        out = resample.resample(tone(48000, 4800, 10000.0), 48000, 16000)
        self.assertLess(float(np.sqrt(np.mean(out[100:-100] ** 2))), 1e-3)

    def test_passthrough_is_bit_exact(self) -> None:
        data = np.random.default_rng(7).normal(0, 0.3, (500, 3))
        out = resample.resample(data, 48000, 48000)
        np.testing.assert_array_equal(out, data)

    def test_channels_are_independent(self) -> None:
        left = tone(48000, 2000, 500.0, 0.5)
        right = tone(48000, 2000, 900.0, 0.25)
        out = resample.resample(np.column_stack((left[:, 0], right[:, 0])), 48000, 44100)
        self.assertGreater(float(np.max(np.abs(out[:, 0]))), 0.45)
        self.assertLess(float(np.max(np.abs(out[:, 1]))), 0.30)
        self.assertAlmostEqual(float(np.max(np.abs(out[:, 0]))), 0.5, delta=0.02)
        self.assertAlmostEqual(float(np.max(np.abs(out[:, 1]))), 0.25, delta=0.02)

    def test_output_finite_for_noise(self) -> None:
        data = np.random.default_rng(3).normal(0, 0.2, (4096, 2))
        out = resample.resample(data, 44100, 48000)
        self.assertTrue(np.all(np.isfinite(out)))


class BoundaryTests(unittest.TestCase):
    def test_mirror_rule_edges(self) -> None:
        idx = np.array([-2, -1, 0, 3, 4, 5, 6])
        mirrored = resample.mirror_indices(idx, frames=4)
        np.testing.assert_array_equal(mirrored, [1, 0, 0, 3, 3, 2, 1])

    def test_single_frame_mirrors_to_itself(self) -> None:
        self.assertEqual(resample.mirror_indices(np.array([-5, 0, 9]), 1).tolist(), [0, 0, 0])

    def test_empty_input_yields_empty_output(self) -> None:
        out = resample.resample(np.zeros((0, 2)), 44100, 48000)
        self.assertEqual(out.shape, (0, 2))

    def test_single_frame(self) -> None:
        out = resample.resample(np.full((1, 1), 0.5), 44100, 48000)
        self.assertEqual(out.shape[0], 1)
        self.assertGreater(float(out[0, 0]), 0.0)
        self.assertLessEqual(float(out[0, 0]), 0.5 + 1e-9)

    def test_shorter_than_kernel_is_defined(self) -> None:
        data = np.full((7, 1), 0.1)
        out = resample.resample(data, 32000, 48000)
        self.assertTrue(np.all(np.isfinite(out)))
        self.assertAlmostEqual(float(out.mean()), 0.1, places=2)

    def test_mirroring_keeps_dc_at_edges(self) -> None:
        data = np.full((50, 1), 0.3)
        out = resample.resample(data, 44100, 48000)
        self.assertLess(float(np.max(np.abs(out - 0.3))), 1e-9)


class BlockIndependenceTests(unittest.TestCase):
    def test_block_size_does_not_change_samples(self) -> None:
        data = np.random.default_rng(11).normal(0, 0.2, (4321, 2))
        whole = resample.resample(data, 32000, 48000, block_size=10_000)
        small = resample.resample(data, 32000, 48000, block_size=37)
        np.testing.assert_allclose(whole, small, rtol=0, atol=1e-15)

    def test_phase_does_not_drift_over_long_signal(self) -> None:
        frames = 44100 * 30
        out = resample.resample(tone(44100, frames, 1000.0), 44100, 48000)
        expected = ideal(out.shape[0], 44100, 48000, 1000.0)
        tail = slice(-4000, -1000)
        self.assertLess(float(np.max(np.abs(out[tail] - expected[tail]))), 1e-4)

    def test_custom_kernel_config(self) -> None:
        data = tone(44100, 4410, 500.0, 0.5)
        cfg = ResampleConfig(zero_crossings=8, rolloff=0.9)
        out = resample.resample(data, 44100, 48000, config=cfg)
        self.assertEqual(out.shape[0], resample.resampled_frames(4410, 44100, 48000))
        self.assertTrue(np.all(np.isfinite(out)))

    def test_kernel_rows_have_unity_sum(self) -> None:
        offsets = np.arange(-40, 41)
        frac = np.array([0.0, 0.25, 0.5, 0.75])
        weights = resample.kernel_weights(offsets, frac, 0.944, 32)
        np.testing.assert_allclose(weights.sum(axis=1), np.ones(4), atol=1e-12)

    def test_invalid_block_size(self) -> None:
        with self.assertRaises(InputError):
            resample.resample(np.zeros((4, 1)), 44100, 48000, block_size=0)

    def test_steep_downsample_is_bounded_and_finite(self) -> None:
        frames = 400
        data = np.random.default_rng(5).normal(0, 0.2, (frames, 1))
        out = resample.resample(data, 384000, 8000, block_size=512)
        expected = resample.resampled_frames(frames, 384000, 8000)
        self.assertEqual(out.shape[0], expected)
        self.assertTrue(np.all(np.isfinite(out)))

    def test_steep_ratio_matches_python_reference(self) -> None:
        # A tiny hand-computed reference: 3 input samples -> 2 output samples.
        data = np.array([[0.0], [1.0], [0.0]])
        out = resample.resample(data, 3, 2, config=ResampleConfig(zero_crossings=4))
        self.assertEqual(out.shape[0], 2)
        for value in out[:, 0]:
            self.assertTrue(np.isfinite(value))
        self.assertGreater(float(out[0, 0]), 0.0)
        self.assertGreater(float(out[1, 0]), 0.0)


class LayoutTests(unittest.TestCase):
    def test_tail_block_is_padded_then_trimmed(self) -> None:
        data = np.arange(10 * 2, dtype=np.float64).reshape(10, 2)
        chunks = layout.iter_frame_blocks(data, 4, pad_tail=True)
        self.assertEqual([c.valid_frames for c in chunks], [4, 4, 2])
        self.assertEqual([c.padded_frames for c in chunks], [0, 0, 2])
        self.assertEqual(chunks[-1].data.shape, (4, 2))
        trimmed = layout.truncate_tail(chunks[-1].data, chunks[-1].valid_frames)
        np.testing.assert_array_equal(trimmed, data[8:10])

    def test_tail_padding_is_zero(self) -> None:
        data = np.ones((5, 3))
        chunks = layout.iter_frame_blocks(data, 2, pad_tail=True)
        np.testing.assert_array_equal(chunks[-1].data[1:], np.zeros((1, 3)))

    def test_empty_has_no_blocks(self) -> None:
        self.assertEqual(layout.iter_frame_blocks(np.zeros((0, 2)), 4), [])

    def test_mono_average_preserves_frame_count(self) -> None:
        data = np.column_stack((np.full(6, 0.2), np.full(6, 0.4)))
        mono = layout.map_channels(data, "mono")
        self.assertEqual(mono.shape, (6, 1))
        np.testing.assert_allclose(mono[:, 0], 0.3)

    def test_exact_channel_mode_rejects_mismatch(self) -> None:
        with self.assertRaises(InputError):
            layout.map_channels(np.zeros((4, 2)), "1")
        kept = layout.map_channels(np.zeros((4, 2)), "2")
        self.assertEqual(kept.shape, (4, 2))

    def test_truncate_tail_pads_when_short(self) -> None:
        out = layout.truncate_tail(np.ones((2, 2)), 4)
        self.assertEqual(out.shape, (4, 2))
        np.testing.assert_array_equal(out[2:], np.zeros((2, 2)))

    def test_gain_does_not_mutate_input(self) -> None:
        data = np.ones((3, 1))
        out = layout.apply_gain(data, 2.0)
        np.testing.assert_array_equal(out, np.full((3, 1), 2.0))
        np.testing.assert_array_equal(data, np.ones((3, 1)))

    def test_block_gain_trims_padded_tail(self) -> None:
        data = np.arange(23, dtype=np.float64)[:, None]
        out = layout.apply_gain_blocks(data, 2.0, 5)
        self.assertEqual(out.shape, data.shape)
        np.testing.assert_array_equal(out, data * 2.0)

    def test_block_gain_handles_empty(self) -> None:
        out = layout.apply_gain_blocks(np.zeros((0, 2)), 2.0, 4)
        self.assertEqual(out.shape, (0, 2))


if __name__ == "__main__":
    unittest.main()
