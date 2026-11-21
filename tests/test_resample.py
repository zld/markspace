"""Resampling kernel and coefficient-cache tests."""

from __future__ import annotations

import unittest

import numpy as np

from audiorepro.resample import (
    CoefficientCache,
    Resampler,
    ResamplerConfig,
    output_length,
)

from support import TempDirTest


class OutputLengthTests(unittest.TestCase):
    def test_output_length_rounds_up(self) -> None:
        self.assertEqual(output_length(1000, 44100, 48000), 1089)
        self.assertEqual(output_length(6615, 22050, 48000), 14400)
        self.assertEqual(output_length(0, 44100, 48000), 0)
        self.assertEqual(output_length(10, 48000, 48000), 10)


class SignalTests(TempDirTest):
    def test_identity_rate_preserves_length_and_signal(self) -> None:
        rate = 48000
        samples = 0.5 * np.sin(2 * np.pi * 1000 * np.arange(rate) / rate)
        out = Resampler().resample_array(samples, rate, rate)
        self.assertEqual(out.shape, (rate, 1))
        np.testing.assert_allclose(out[100:-100, 0], samples[100:-100], atol=1e-3)

    def test_antialiasing_attenuates_out_of_band_tone(self) -> None:
        rate = 48000
        t = np.arange(rate * 2) / rate
        audible = Resampler().resample_array(np.sin(2 * np.pi * 3000 * t), rate, 8000)
        alias = Resampler().resample_array(np.sin(2 * np.pi * 8000 * t), rate, 8000)
        audible_peak = np.abs(audible[1000:-1000]).max()
        alias_peak = np.abs(alias[1000:-1000]).max()
        self.assertGreater(audible_peak, 0.8)
        self.assertLess(alias_peak, 0.05)
        self.assertLess(alias_peak, audible_peak / 10)

    def test_multichannel_order_is_preserved(self) -> None:
        rate = 48000
        n = rate // 2
        t = np.arange(n) / rate
        levels_in = [0.1, 0.2, 0.3, 0.4, 0.5, 0.6]
        samples = np.stack([c * np.sin(2 * np.pi * 440 * t) for c in levels_in], axis=1)
        out = Resampler().resample_array(samples, rate, rate)
        self.assertEqual(out.shape, (n, 6))
        levels = [np.abs(out[1000:-1000, ch]).max() for ch in range(6)]
        for got, expected in zip(levels, levels_in):
            self.assertAlmostEqual(got, expected, delta=0.01)

    def test_zero_padded_tail_keeps_last_frame(self) -> None:
        rate = 8000
        out = Resampler().resample_array(np.ones((10, 1)), rate, rate * 2)
        self.assertEqual(out.shape[0], 20)
        self.assertTrue(np.all(np.isfinite(out)))
        self.assertGreater(out[-1, 0], 0.0)

    def test_non_finite_input_is_not_hidden(self) -> None:
        samples = np.array([[0.0], [np.inf], [0.0]])
        out = Resampler().resample_array(samples, 48000, 48000)
        self.assertFalse(np.isfinite(out).all())

    def test_chunk_boundaries_match_single_chunk(self) -> None:
        rng = np.random.default_rng(11)
        samples = rng.normal(0.0, 0.1, (12345, 2))
        whole = Resampler().resample_array(samples, 44100, 48000, chunk_frames=65536)
        split = Resampler().resample_array(samples, 44100, 48000, chunk_frames=777)
        np.testing.assert_allclose(whole, split, atol=1e-10)


class CoefficientCacheTests(unittest.TestCase):
    def test_hit_and_metadata(self) -> None:
        cache = CoefficientCache(ResamplerConfig(half_width=32, phases=64), max_entries=2)
        first = cache.get(44100, 48000)
        second = cache.get(44100, 48000)
        self.assertIs(first, second)
        stats = cache.stats()
        self.assertEqual(stats["misses"], 1)
        self.assertEqual(stats["hits"], 1)
        self.assertEqual(first.coeffs.shape, (64, 64))
        self.assertAlmostEqual(first.step, 44100 / 48000)
        self.assertTrue(np.allclose(first.coeffs.sum(axis=1), 1.0))

    def test_eviction_is_bounded(self) -> None:
        cache = CoefficientCache(ResamplerConfig(half_width=16, phases=8), max_entries=2)
        for rates in ((44100, 48000), (48000, 44100), (96000, 48000)):
            cache.get(*rates)
        self.assertLessEqual(cache.stats()["entries"], 2)

    def test_cached_table_is_reused_across_resamplers(self) -> None:
        cache = CoefficientCache(ResamplerConfig(half_width=8, phases=8))
        first = Resampler(cache).resample_array(np.ones((100, 1)), 44100, 48000)
        second = Resampler(cache).resample_array(np.ones((100, 1)), 44100, 48000)
        np.testing.assert_array_equal(first, second)
        self.assertEqual(cache.stats()["misses"], 1)


if __name__ == "__main__":
    unittest.main()
