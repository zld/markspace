"""Loudness measurement and gain-decision tests."""

from __future__ import annotations

import math
import unittest

import numpy as np

from audiorepro.loudness import (
    analyze_array,
    analyze_reader,
    gain_to_target,
    k_weighting_coefficients,
)


class KWeightingTests(unittest.TestCase):
    def test_coefficients_match_bs1770_at_48k(self) -> None:
        shelf, highpass = k_weighting_coefficients(48000)
        np.testing.assert_allclose(
            shelf["b"], [1.53512485958697, -2.69169618940638, 1.19839281085285], atol=1e-6
        )
        np.testing.assert_allclose(
            shelf["a"], [1.0, -1.69065929318241, 0.73248077421585], atol=1e-6
        )
        np.testing.assert_allclose(
            highpass["a"], [1.0, -1.99004745483398, 0.99007225036621], atol=1e-6
        )

    def test_coefficients_are_rate_dependent(self) -> None:
        at_48 = k_weighting_coefficients(48000)[1]["a"]
        at_44 = k_weighting_coefficients(44100)[1]["a"]
        self.assertNotAlmostEqual(at_48[1], at_44[1], places=6)


class CalibrationTests(unittest.TestCase):
    def test_stereo_calibration_is_minus_23_lufs(self) -> None:
        rate = 48000
        amplitude = 10 ** (-23.0 / 20.0)
        tone = amplitude * np.sin(2 * np.pi * 1000 * np.arange(rate * 2) / rate)
        analysis = analyze_array(np.stack([tone, tone], axis=1), rate)
        self.assertAlmostEqual(analysis.lufs, -23.0, delta=0.1)
        self.assertAlmostEqual(analysis.rms_dbfs, -26.0, delta=0.1)
        self.assertAlmostEqual(analysis.peak_dbfs, -23.0, delta=0.1)

    def test_calibration_holds_at_44k(self) -> None:
        rate = 44100
        # A single channel is not doubled by the summing rule, so a
        # -17 dBFS 1 kHz tone measures -20 LUFS.
        amplitude = 10 ** (-17.0 / 20.0)
        tone = amplitude * np.sin(2 * np.pi * 1000 * np.arange(rate * 2) / rate)
        self.assertAlmostEqual(analyze_array(tone, rate).lufs, -20.0, delta=0.1)

    def test_low_frequencies_are_k_weighted_down(self) -> None:
        rate = 48000
        t = np.arange(rate * 2) / rate
        amplitude = 0.1
        low = analyze_array(amplitude * np.sin(2 * np.pi * 60 * t), rate)
        high = analyze_array(amplitude * np.sin(2 * np.pi * 1000 * t), rate)
        # The BS.1770 shelf and RLB high-pass attenuate 60 Hz by ~3.6 dB
        # relative to 1 kHz.
        self.assertLess(low.lufs, high.lufs - 2.5)


class GatingTests(unittest.TestCase):
    def test_short_and_silent_have_no_lufs(self) -> None:
        self.assertIsNone(analyze_array(np.zeros(4800), 48000).lufs)
        self.assertIsNone(analyze_array(np.full((19199, 1), 0.1), 48000).lufs)
        self.assertIsNotNone(analyze_array(np.full((19200, 1), 0.1), 48000).lufs)

    def test_silence_statistics(self) -> None:
        analysis = analyze_array(np.zeros((48000, 2)), 48000)
        self.assertIsNone(analysis.rms_dbfs)
        self.assertIsNone(analysis.peak_dbfs)
        self.assertIsNone(analysis.lufs)
        self.assertEqual(analysis.clip_samples, 0)

    def test_clipped_samples_count_at_full_scale(self) -> None:
        samples = np.array([[0.5], [1.0], [-1.2], [0.99]])
        self.assertEqual(analyze_array(samples, 48000).clip_samples, 2)

    def test_unsupported_layout_raises(self) -> None:
        with self.assertRaises(ValueError):
            analyze_array(np.zeros((100, 3)), 48000)

    def test_lfe_channel_is_not_measured(self) -> None:
        rate = 48000
        n = rate
        tones = [0.1 * np.sin(2 * np.pi * 500 * np.arange(n) / rate)] * 3
        silence = np.zeros(n)
        without_lfe = np.stack(tones + [silence, tones[0], tones[0]], axis=1)
        with_lfe = np.stack(tones + [0.9 * np.ones(n), tones[0], tones[0]], axis=1)
        self.assertAlmostEqual(
            analyze_array(without_lfe, rate).lufs,
            analyze_array(with_lfe, rate).lufs,
            places=6,
        )

    def test_streaming_matches_single_read(self) -> None:
        rng = np.random.default_rng(3)
        data = rng.normal(0.0, 0.05, (50000, 2))
        whole = analyze_array(data, 48000, chunk_frames=65536)

        def reader(start: int, count: int) -> np.ndarray:
            return data[start : start + count]

        streamed = analyze_reader(reader, data.shape[0], 2, 48000, chunk_frames=997)
        self.assertAlmostEqual(whole.lufs, streamed.lufs, places=6)
        self.assertAlmostEqual(whole.rms_dbfs, streamed.rms_dbfs, places=6)
        self.assertEqual(whole.clip_samples, streamed.clip_samples)


class GainTests(unittest.TestCase):
    def test_peak_ceiling_limits_gain(self) -> None:
        gain, normalized = gain_to_target(-20.0, 0.9, -14.0, -1.0)
        self.assertTrue(normalized)
        self.assertAlmostEqual(gain, 20 * math.log10(10 ** (-1 / 20) / 0.9), places=6)

    def test_unmeasurable_audio_gets_no_gain(self) -> None:
        gain, normalized = gain_to_target(None, 0.5, -14.0, -1.0)
        self.assertEqual(gain, 0.0)
        self.assertFalse(normalized)

    def test_normal_gain_reaches_target(self) -> None:
        gain, normalized = gain_to_target(-30.0, 0.01, -23.0, -1.0)
        self.assertTrue(normalized)
        self.assertAlmostEqual(gain, 7.0, places=6)


if __name__ == "__main__":
    unittest.main()
