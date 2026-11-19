"""Tests for loudness measurement and gain planning."""

from __future__ import annotations

import unittest

import numpy as np

from audiopipe.loudness import (
    LevelMeter,
    analyse_blocks,
    decide_gain,
    k_weighting_coefficients,
    lufs_from_energy,
)
from tests.support import sine


class KWeightingTests(unittest.TestCase):
    def test_matches_published_48k_response(self) -> None:
        """Our analog-prototype design must track the BS.1770 48 kHz table."""
        b, a = k_weighting_coefficients(48000)
        reference_b = np.array(
            [
                [1.53512485958697, -2.69169618940638, 1.19839281085285],
                [1.0, -2.0, 1.0],
            ]
        )
        reference_a = np.array(
            [
                [1.0, -1.69065929318241, 0.73248077421585],
                [1.0, -1.99004745483398, 0.99007225036621],
            ]
        )

        def response(bq: np.ndarray, aq: np.ndarray, omega: np.ndarray) -> np.ndarray:
            z = np.exp(-1j * omega)
            return (bq[0] + bq[1] * z + bq[2] * z * z) / (1.0 + aq[1] * z + aq[2] * z * z)

        omega = np.linspace(1e-3, np.pi * 0.95, 2000)
        ours = np.abs(response(b[0], a[0], omega) * response(b[1], a[1], omega))
        theirs = np.abs(
            response(reference_b[0], reference_a[0], omega)
            * response(reference_b[1], reference_a[1], omega)
        )
        deviation_db = np.max(np.abs(20.0 * np.log10(ours / theirs)))
        self.assertLess(float(deviation_db), 0.01)

    def test_stable_at_other_rates(self) -> None:
        for rate in (8000, 22050, 44100, 96000, 192000):
            b, a = k_weighting_coefficients(rate)
            self.assertTrue(np.all(np.isfinite(b)) and np.all(np.isfinite(a)))
            poles = np.roots([1.0, a[0, 1], a[0, 2]])
            self.assertTrue(np.all(np.abs(poles) < 1.0))

    def test_invalid_rate(self) -> None:
        with self.assertRaises(ValueError):
            k_weighting_coefficients(0)


class MeterTests(unittest.TestCase):
    def test_rms_and_peak(self) -> None:
        data = np.array([[0.5, -0.25], [0.5, 0.25], [-0.5, 0.25], [-0.5, -0.25]])
        snapshot = analyse_blocks([data], 48000, 2)
        np.testing.assert_allclose(snapshot.rms_per_channel, [0.5, 0.25])
        self.assertAlmostEqual(snapshot.rms, float(np.sqrt((0.25 + 0.0625) / 2)))
        self.assertAlmostEqual(snapshot.peak, 0.5)

    def test_channel_energies_are_summed(self) -> None:
        """BS.1770 sums per-channel energy: N identical channels add 10log10(N)."""
        mono = sine(997.0, 48000, 48000, 0.1)[:, None]
        reference = analyse_blocks([mono], 48000, 1).lufs
        for channels in (2, 6, 8):
            data = np.repeat(mono, channels, axis=1)
            measured = analyse_blocks([data], 48000, channels).lufs
            self.assertAlmostEqual(
                measured, reference + 10.0 * np.log10(channels), places=6
            )

    def test_silence_has_no_loudness(self) -> None:
        snapshot = analyse_blocks([np.zeros((128, 2))], 48000, 2)
        self.assertIsNone(snapshot.lufs)
        self.assertEqual(snapshot.rms, 0.0)
        self.assertEqual(snapshot.peak, 0.0)

    def test_empty_input(self) -> None:
        snapshot = analyse_blocks([], 48000, 1)
        self.assertEqual(snapshot.frames, 0)
        self.assertIsNone(snapshot.lufs)

    def test_clipping_is_counted_per_sample(self) -> None:
        data = np.array([[1.5, -1.5], [0.5, -0.5], [1.0, -1.0], [1.0001, 0.0]])
        snapshot = analyse_blocks([data], 48000, 2)
        self.assertEqual(snapshot.clipped_samples, 3)

    def test_chunking_does_not_change_loudness(self) -> None:
        data = np.repeat(sine(997.0, 48000, 24000, 0.1)[:, None], 2, axis=1)
        whole = analyse_blocks([data], 48000, 2)
        chunked = analyse_blocks(
            [data[i : i + 157] for i in range(0, len(data), 157)], 48000, 2
        )
        self.assertAlmostEqual(whole.lufs, chunked.lufs, places=9)

    def test_reference_level(self) -> None:
        """A 997 Hz stereo sine at -20 dBFS per channel measures about -20.04 LUFS.

        BS.1770 sums channel energy, so two identical -20 dBFS channels
        read 3.01 LU above the mono value of about -23.05 LUFS (the
        ungated approximation is 0.04 dB below the published table).
        """
        data = np.repeat(sine(997.0, 48000, 48000, 10 ** (-20 / 20))[:, None], 2, axis=1)
        snapshot = analyse_blocks([data], 48000, 2)
        self.assertAlmostEqual(snapshot.lufs, -20.04, delta=0.1)

    def test_preroll_does_not_bias_short_files(self) -> None:
        """Filter priming must not contribute to the loudness denominator."""
        short = sine(997.0, 48000, 100, 0.5)[:, None]
        direct = analyse_blocks([short], 48000, 1)
        primed = LevelMeter(48000, 1, preroll=512)
        primed.feed(short)
        self.assertAlmostEqual(direct.lufs, primed.snapshot().lufs, places=12)

    def test_single_frame_loudness_is_finite(self) -> None:
        meter = LevelMeter(48000, 1)
        meter.feed(np.array([[0.5]]))
        snapshot = meter.snapshot()
        self.assertTrue(np.isfinite(snapshot.lufs))
        self.assertGreater(snapshot.lufs, -60.0)

    def test_rejects_bad_input(self) -> None:
        meter = LevelMeter(48000, 2)
        with self.assertRaises(ValueError):
            meter.feed(np.zeros((10, 1)))
        with self.assertRaises(ValueError):
            meter.feed(np.full((10, 2), np.nan))
        with self.assertRaises(ValueError):
            LevelMeter(48000, 9)

    def test_lufs_from_energy(self) -> None:
        self.assertIsNone(lufs_from_energy(0.0))
        self.assertIsNone(lufs_from_energy(float("nan")))
        self.assertAlmostEqual(lufs_from_energy(1.0), -0.691)


class GainTests(unittest.TestCase):
    def test_target_gain(self) -> None:
        decision = decide_gain(-24.0, 0.5, -18.0, max_gain_db=12.0)
        self.assertAlmostEqual(decision.gain_db, 6.0)
        self.assertEqual(decision.reason, "target")
        self.assertFalse(decision.max_gain_limited)

    def test_max_gain_cap(self) -> None:
        decision = decide_gain(-40.0, 0.05, -18.0, max_gain_db=12.0)
        self.assertAlmostEqual(decision.gain_db, 12.0)
        self.assertTrue(decision.max_gain_limited)
        self.assertEqual(decision.reason, "max_gain_limit")

    def test_peak_rollback(self) -> None:
        decision = decide_gain(-30.0, 0.9, -18.0, max_gain_db=24.0, peak_ceiling=0.999)
        self.assertTrue(decision.peak_limited)
        self.assertEqual(decision.reason, "peak_limit")
        self.assertLessEqual(decision.predicted_peak, 0.999 + 1e-12)

    def test_silence_gets_no_gain(self) -> None:
        decision = decide_gain(None, 0.0, -18.0)
        self.assertEqual(decision.gain_db, 0.0)
        self.assertEqual(decision.reason, "no_loudness")

    def test_invalid_arguments(self) -> None:
        with self.assertRaises(ValueError):
            decide_gain(-20.0, float("nan"), -18.0)
        with self.assertRaises(ValueError):
            decide_gain(-20.0, -0.1, -18.0)
        with self.assertRaises(ValueError):
            decide_gain(-20.0, 0.5, float("inf"))


if __name__ == "__main__":
    unittest.main()
