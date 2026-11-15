"""Unit tests for model validation, resampling history and gain automation."""

from __future__ import annotations

import unittest

import numpy as np

from audiomix import AudioProcessError, ConfigError
from audiomix.automation import GainCurve
from audiomix.model import GainPoint, validate_project
from audiomix.resample import SincResampler


def _base_project(**overrides):
    raw = {
        "sample_rate": 48000,
        "frames": 1000,
        "tracks": [
            {"id": "t1", "path": "a.wav", "start_frame": 0, "gain": 1.0}
        ],
    }
    raw.update(overrides)
    return raw


class ModelValidationTests(unittest.TestCase):
    def test_valid_project(self):
        project = validate_project(_base_project())
        self.assertEqual(project.sample_rate, 48000)
        self.assertEqual(len(project.tracks), 1)

    def test_reject_duplicate_ids(self):
        raw = _base_project(tracks=[
            {"id": "x", "path": "a.wav"},
            {"id": "x", "path": "b.wav"},
        ])
        with self.assertRaisesRegex(ConfigError, "duplicate track id"):
            validate_project(raw)

    def test_reject_negative_frames_and_bad_rate(self):
        with self.assertRaisesRegex(ConfigError, "frames"):
            validate_project(_base_project(frames=-1))
        with self.assertRaisesRegex(ConfigError, "sample_rate"):
            validate_project(_base_project(sample_rate=7999))
        with self.assertRaisesRegex(ConfigError, "sample_rate"):
            validate_project(_base_project(sample_rate=192001))

    def test_reject_more_than_32_tracks(self):
        tracks = [{"id": f"t{i}", "path": f"{i}.wav"} for i in range(33)]
        with self.assertRaisesRegex(ConfigError, "track limit"):
            validate_project(_base_project(tracks=tracks))

    def test_automation_ordering_duplicates_and_finite(self):
        def project(points):
            raw = _base_project()
            raw["tracks"][0]["automation"] = points
            return validate_project(raw)

        with self.assertRaisesRegex(ConfigError, "out of order"):
            project([{"frame": 9, "gain": 1.0}, {"frame": 9, "gain": 0.5}])
        with self.assertRaisesRegex(ConfigError, "finite"):
            project([{"frame": 0, "gain": float("nan")}])
        with self.assertRaisesRegex(ConfigError, "finite"):
            project([{"frame": 0, "gain": float("inf")}])
        with self.assertRaisesRegex(ConfigError, "mode"):
            project([{"frame": 0, "gain": 1.0, "mode": "warp"}])
        with self.assertRaises(ConfigError):
            project([{"frame": -1, "gain": 1.0}])

    def test_block_frame_limits(self):
        with self.assertRaises(ConfigError):
            validate_project(_base_project(block_frames=0))
        with self.assertRaises(ConfigError):
            validate_project(_base_project(block_frames=8193))

    def test_sample_frame_outside_project(self):
        with self.assertRaisesRegex(ConfigError, "outside project"):
            validate_project(_base_project(automation_sample_frames=[1000]))


class ResamplerTests(unittest.TestCase):
    def setUp(self):
        self.signal = np.random.default_rng(1).standard_normal(4000)

    def _run(self, src, dst, block):
        resampler = SincResampler(src, dst)
        chunks = []
        for i in range(0, len(self.signal), block):
            chunks.append(resampler.process(self.signal[i:i + block]))
        while True:
            tail = resampler.flush(max_output=37)
            if tail.shape[0] == 0:
                break
            chunks.append(tail)
        out = np.concatenate(chunks)
        self.assertEqual(out.shape[0], (len(self.signal) * dst) // src)
        return out

    def test_block_size_parity_all_rate_pairs(self):
        for src, dst in ((44100, 48000), (48000, 44100),
                         (32000, 48000), (48000, 32000)):
            reference = self._run(src, dst, 10_000)
            for block in (1, 2, 7, 257, 1024):
                np.testing.assert_array_equal(
                    reference, self._run(src, dst, block),
                    err_msg=f"parity failed {src}->{dst} block={block}",
                )

    def test_passthrough_is_exact_and_preserves_shape(self):
        resampler = SincResampler(48000, 48000)
        np.testing.assert_array_equal(
            resampler.process(self.signal[:50]), self.signal[:50]
        )
        self.assertEqual(resampler.flush().shape[0], 0)
        stereo = np.random.default_rng(2).standard_normal((100, 2))
        out = SincResampler(44100, 44100).process(stereo)
        self.assertEqual(out.shape, (100, 2))
        np.testing.assert_array_equal(out, stereo)

    def test_invalid_rates_raise_process_error(self):
        with self.assertRaises(AudioProcessError):
            SincResampler(0, 48000)


class AutomationTests(unittest.TestCase):
    def test_hold_linear_and_endpoint_extension(self):
        points = (
            GainPoint(0, 0.0, "hold"),
            GainPoint(10, 1.0, "linear"),
            GainPoint(20, 0.5, "hold"),
        )
        values = GainCurve(2.0, points).sample_frames(0, 25)
        self.assertEqual(values[0], 0.0)
        self.assertEqual(values[9], 0.0)
        self.assertEqual(values[10], 2.0)
        self.assertAlmostEqual(values[15], 1.5)
        self.assertEqual(values[24], 1.0)

    def test_flat_curve_without_points(self):
        values = GainCurve(0.7, ()).sample_frames(0, 4)
        np.testing.assert_array_equal(values, np.full(4, 0.7))


if __name__ == "__main__":
    unittest.main()
