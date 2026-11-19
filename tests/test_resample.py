"""Unit tests for the self-implemented polyphase resampling kernel."""

from __future__ import annotations

import math
import unittest

import numpy as np

from audiopipe.resample import (
    KAISER_BETA,
    PolyphaseResampler,
    design_filter,
    reduce_ratio,
    resample_array,
)
from tests.support import sine


def _run(data: np.ndarray, in_rate: int, out_rate: int, block: int | None = None):
    """Stream ``data`` through a fresh resampler in fixed-size blocks."""
    resampler = PolyphaseResampler(in_rate, out_rate, data.shape[1])
    step = len(data) if block is None else block
    parts = [
        resampler.process_chunk(data[start : start + step])
        for start in range(0, len(data), max(step, 1))
    ]
    parts.append(resampler.finish())
    return np.concatenate(parts, axis=0) if parts else np.zeros((0, data.shape[1])), resampler


class RatioTests(unittest.TestCase):
    def test_reduction(self) -> None:
        self.assertEqual(reduce_ratio(44100, 48000), reduce_ratio(44100, 48000))
        ratio = reduce_ratio(44100, 48000)
        self.assertEqual((ratio.numerator, ratio.denominator), (160, 147))
        self.assertEqual(math.gcd(ratio.numerator, ratio.denominator), 1)
        self.assertEqual(reduce_ratio(48000, 48000).numerator, 1)

    def test_invalid_rates(self) -> None:
        for bad in (0, -1, 1.5, "48000", True):
            with self.assertRaises(ValueError):
                reduce_ratio(44100, bad)

    def test_frame_count_is_ratio_rounded(self) -> None:
        data = np.zeros((1000, 2))
        out, resampler = _run(data, 44100, 48000)
        self.assertEqual(out.shape[0], (1000 * 160) // 147)
        self.assertEqual(resampler.output_frames_for(1000), (1000 * 160) // 147)


class KernelDesignTests(unittest.TestCase):
    def test_every_phase_has_unit_dc_gain(self) -> None:
        taps, spec = design_filter(44100, 48000)
        self.assertEqual(taps.shape, (160, spec.taps_per_phase + 1))
        np.testing.assert_allclose(taps.sum(axis=1), 1.0, atol=1e-12)
        self.assertEqual(spec.window, "kaiser")
        self.assertAlmostEqual(spec.window_beta, KAISER_BETA)
        self.assertAlmostEqual(spec.cutoff_hz, 0.48 * 44100, places=6)

    def test_design_is_deterministic(self) -> None:
        first, spec_a = design_filter(48000, 44100)
        second, spec_b = design_filter(48000, 44100)
        np.testing.assert_array_equal(first, second)
        self.assertEqual(spec_a, spec_b)

    def test_decimation_uses_more_taps(self) -> None:
        _taps, up = design_filter(48000, 96000)
        _taps, down = design_filter(96000, 48000)
        self.assertEqual(up.taps_per_phase, 32)
        self.assertEqual(down.taps_per_phase, 64)


class FidelityTests(unittest.TestCase):
    def test_dc_passes_unchanged(self) -> None:
        data = np.full((5000, 2), 0.25)
        out, _ = _run(data, 44100, 48000)
        interior = out[500:-500]
        np.testing.assert_allclose(interior, 0.25, atol=1e-12)

    def test_sine_passes_with_unit_gain_and_no_delay(self) -> None:
        out, _ = _run(sine(440.0, 44100, 22050)[:, None], 44100, 48000)
        reference = sine(440.0, 48000, out.shape[0])
        segment = slice(400, out.shape[0] - 400)
        gain = float(np.dot(out[segment, 0], reference[segment])) / float(
            np.dot(reference[segment], reference[segment])
        )
        self.assertAlmostEqual(gain, 1.0, places=4)
        self.assertLess(float(np.max(np.abs(out[segment, 0] - reference[segment]))), 1e-3)

    def test_aliasing_is_suppressed_on_downsampling(self) -> None:
        out, _ = _run(sine(20000.0, 44100, 44100)[:, None], 44100, 22050)
        energy = float(np.sqrt(np.mean(out[500:-500, 0] ** 2)))
        self.assertLess(energy, 1e-3)

    def test_output_is_block_size_invariant(self) -> None:
        data = np.random.default_rng(7).standard_normal((3000, 3))
        whole, _ = _run(data, 44100, 48000, block=3000)
        chunked, _ = _run(data, 44100, 48000, block=97)
        np.testing.assert_array_equal(whole, chunked)

    def test_identical_rates_pass_through(self) -> None:
        data = np.random.default_rng(1).standard_normal((250, 4))
        out, resampler = _run(data, 48000, 48000)
        np.testing.assert_array_equal(out, data)
        self.assertEqual(resampler.padding_frames, 0)

    def test_channels_are_not_mixed(self) -> None:
        data = np.zeros((4000, 4))
        for channel in range(4):
            data[:, channel] = sine(200.0 + 100.0 * channel, 44100, 4000)
        out, _ = _run(data, 44100, 32000)
        for channel in range(4):
            reference = sine(200.0 + 100.0 * channel, 32000, out.shape[0])
            segment = slice(400, out.shape[0] - 400)
            self.assertGreater(float(np.dot(out[segment, channel], reference[segment])), 0.0)
            other = sine(200.0 + 100.0 * ((channel + 1) % 4), 32000, out.shape[0])
            self.assertLess(
                abs(float(np.dot(out[segment, channel], other[segment]))),
                abs(float(np.dot(out[segment, channel], reference[segment]))) * 0.05,
            )


class EdgeCaseTests(unittest.TestCase):
    def test_empty_input(self) -> None:
        out, resampler = _run(np.zeros((0, 2)), 44100, 48000)
        self.assertEqual(out.shape, (0, 2))
        self.assertEqual(resampler.padding_frames, 0)

    def test_single_frame(self) -> None:
        out, resampler = _run(np.array([[0.5]]), 44100, 48000)
        self.assertEqual(out.shape, (1, 1))
        self.assertTrue(np.all(np.isfinite(out)))
        self.assertGreaterEqual(resampler.padding_frames, 1)

    def test_shorter_than_filter(self) -> None:
        for frames in (2, 5, 33, 64):
            out, _ = _run(np.ones((frames, 1)), 44100, 48000)
            self.assertEqual(out.shape[0], (frames * 160) // 147)
            self.assertTrue(np.all(np.isfinite(out)))

    def test_padding_frames_reported(self) -> None:
        _out, resampler = _run(np.zeros((1000, 1)), 44100, 48000)
        left, right = resampler.padding_breakdown
        self.assertEqual(left, resampler.spec.taps_per_phase // 2)
        self.assertGreaterEqual(right, 0)
        self.assertEqual(resampler.padding_frames, left + right)

    def test_non_finite_input_rejected(self) -> None:
        resampler = PolyphaseResampler(44100, 48000, 1)
        with self.assertRaises(ValueError):
            resampler.process_chunk(np.array([[np.nan]]))
        resampler = PolyphaseResampler(44100, 48000, 1)
        with self.assertRaises(ValueError):
            resampler.process_chunk(np.array([[np.inf]]))

    def test_channel_count_bounds(self) -> None:
        for channels in (0, 9):
            with self.assertRaises(ValueError):
                PolyphaseResampler(44100, 48000, channels)

    def test_wrong_shape_rejected(self) -> None:
        resampler = PolyphaseResampler(44100, 48000, 2)
        with self.assertRaises(ValueError):
            resampler.process_chunk(np.zeros((10, 1), dtype=np.float64))
        with self.assertRaises(ValueError):
            resampler.process_chunk(np.zeros(10, dtype=np.float64))

    def test_no_output_after_finish(self) -> None:
        resampler = PolyphaseResampler(44100, 48000, 1)
        resampler.process_chunk(np.ones((10, 1)))
        resampler.finish()
        with self.assertRaises(RuntimeError):
            resampler.process_chunk(np.ones((10, 1)))


class UtilityTests(unittest.TestCase):
    def test_resample_array_matches_streaming(self) -> None:
        data = np.random.default_rng(11).standard_normal((1200, 2))
        whole, resampler = resample_array(data, 48000, 44100, block_frames=256)
        streamed, _ = _run(data, 48000, 44100, block=256)
        np.testing.assert_array_equal(whole, streamed)
        self.assertEqual(whole.shape[0], resampler.output_frames_for(1200))


if __name__ == "__main__":
    unittest.main()
