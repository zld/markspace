"""Boundary and behaviour tests for the silence detector."""
import unittest

import numpy as np

from audioslice.detector import DetectParams, SilenceDetector
from audioslice.signal import frame_rms, rms_to_db

SR = 8000


def tone_burst(n_samples, intervals, amp=0.2, freq=300.0, sr=SR):
    """Build a signal with sinusoidal bursts on half-open sample intervals."""
    sig = np.zeros(n_samples, dtype=np.float64)
    for start, end in intervals:
        idx = np.arange(end - start)
        sig[start:end] = amp * np.sin(2.0 * np.pi * freq * idx / sr)
    return sig


class FramingTests(unittest.TestCase):
    def test_trailing_partial_frame_uses_actual_length(self):
        x = np.arange(10.0)
        rms, starts = frame_rms(x, frame_len=4, hop=4)
        self.assertEqual(starts.tolist(), [0, 4, 8])
        # final frame covers only 2 samples but still produces an RMS value
        self.assertEqual(rms.shape, (3,))
        self.assertTrue(np.isfinite(rms).all())

    def test_input_shorter_than_one_frame(self):
        rms, starts = frame_rms(np.ones(3), frame_len=16, hop=8)
        self.assertEqual(starts.tolist(), [0])
        self.assertEqual(rms.shape, (1,))

    def test_rms_db_is_finite(self):
        db = rms_to_db(np.array([0.0, 1e-9, 0.5]))
        self.assertTrue(np.isfinite(db).all())
        self.assertGreaterEqual(db.min(), -120.0 - 1e-9)


class BoundaryTests(unittest.TestCase):
    def setUp(self):
        self.detector = SilenceDetector(
            DetectParams(
                adaptive=False,
                threshold_db=-40.0,
                confirm_ms=10.0,
                min_silence_ms=20.0,
                merge_gap_ms=0.0,
            )
        )

    def test_segments_are_integer_half_open_within_bounds(self):
        sig = tone_burst(SR, [(1000, 3000), (5000, 7000)])
        result = self.detector.detect(sig, SR)
        self.assertGreaterEqual(len(result.segments), 2)
        for seg in result.segments:
            self.assertIsInstance(seg.start_sample, int)
            self.assertIsInstance(seg.end_sample, int)
            self.assertGreater(seg.end_sample, seg.start_sample)
            self.assertGreaterEqual(seg.start_sample, 0)
            self.assertLessEqual(seg.end_sample, result.n_samples)

    def test_head_and_tail_silence_regions(self):
        sig = tone_burst(SR, [(2000, 4000)])
        result = self.detector.detect(sig, SR)
        self.assertEqual(result.silence_regions[0].start_sample, 0)
        self.assertEqual(result.silence_regions[-1].end_sample, SR)

    def test_silence_and_speech_tile_recording_without_overlap(self):
        sig = tone_burst(SR, [(1000, 2000), (4400, 5400)])
        result = self.detector.detect(sig, SR)
        tiles = sorted(
            [(r.start_sample, r.end_sample) for r in result.silence_regions]
            + [(r.start_sample, r.end_sample) for r in result.raw_segments]
        )
        self.assertEqual(tiles[0][0], 0)
        self.assertEqual(tiles[-1][1], SR)
        for left, right in zip(tiles, tiles[1:]):
            self.assertEqual(left[1], right[0])
        voiced = sum(
            s.end_sample - s.start_sample for s in result.raw_segments
        )
        self.assertEqual(result.silence_total_samples, SR - voiced)

    def test_all_silence_recording(self):
        result = self.detector.detect(np.zeros(SR), SR)
        self.assertEqual(result.segments, [])
        self.assertEqual(result.raw_segments, [])
        self.assertEqual(len(result.silence_regions), 1)
        self.assertEqual(result.silence_total_samples, SR)


class HysteresisTests(unittest.TestCase):
    def test_short_burst_below_confirm_time_does_not_open(self):
        det = SilenceDetector(
            DetectParams(
                adaptive=False,
                threshold_db=-40.0,
                confirm_ms=60.0,
                min_silence_ms=60.0,
            )
        )
        sig = np.zeros(SR)
        sig[500:510] = 0.5  # 1.25 ms, far below the 60 ms confirmation
        self.assertEqual(det.detect(sig, SR).segments, [])

    def test_internal_low_dip_does_not_split_utterance(self):
        det = SilenceDetector(
            DetectParams(
                adaptive=False,
                threshold_db=-40.0,
                confirm_ms=10.0,
                min_silence_ms=120.0,
                merge_gap_ms=0.0,
            )
        )
        sig = tone_burst(SR, [(1000, 5000)])
        sig[2800:3000] *= 0.05  # 25 ms dip below the close threshold
        result = det.detect(sig, SR)
        self.assertEqual(len(result.raw_segments), 1)


class MergeTests(unittest.TestCase):
    def setUp(self):
        self.base = dict(
            adaptive=False,
            threshold_db=-40.0,
            confirm_ms=10.0,
            min_silence_ms=50.0,
        )

    def test_gap_below_merge_threshold_merges_and_keeps_raw(self):
        sig = tone_burst(SR, [(1000, 2000), (4400, 5400)])
        det = SilenceDetector(DetectParams(merge_gap_ms=400.0, **self.base))
        result = det.detect(sig, SR)
        self.assertEqual(len(result.raw_segments), 2)
        self.assertEqual(len(result.segments), 1)
        self.assertEqual(
            result.segments[0].start_sample,
            result.raw_segments[0].start_sample,
        )
        self.assertEqual(
            result.segments[0].end_sample,
            result.raw_segments[-1].end_sample,
        )

    def test_gap_above_merge_threshold_stays_separate(self):
        sig = tone_burst(SR, [(1000, 2000), (4400, 5400)])
        det = SilenceDetector(DetectParams(merge_gap_ms=50.0, **self.base))
        result = det.detect(sig, SR)
        self.assertEqual(len(result.segments), 2)

    def test_silence_total_uses_raw_not_merged_gap(self):
        sig = tone_burst(SR, [(1000, 2000), (4400, 5400)])
        det = SilenceDetector(DetectParams(merge_gap_ms=400.0, **self.base))
        merged = det.detect(sig, SR)
        det2 = SilenceDetector(DetectParams(merge_gap_ms=50.0, **self.base))
        unmerged = det2.detect(sig, SR)
        self.assertEqual(
            merged.silence_total_samples, unmerged.silence_total_samples
        )


class AdaptiveThresholdTests(unittest.TestCase):
    def test_adaptive_finds_weak_speech_missed_by_fixed_threshold(self):
        rng = np.random.default_rng(1)
        sig = rng.normal(0.0, 0.002, SR * 2)
        sig[4000:8000] += 0.012 * np.sin(
            2.0 * np.pi * 300.0 * np.arange(4000) / SR
        )
        adaptive = SilenceDetector(DetectParams(threshold_db=-38.0)).detect(sig, SR)
        fixed = SilenceDetector(
            DetectParams(threshold_db=-38.0, adaptive=False)
        ).detect(sig, SR)
        self.assertEqual(len(adaptive.segments), 1)
        self.assertEqual(len(fixed.segments), 0)

    def test_single_burst_does_not_raise_global_threshold(self):
        rng = np.random.default_rng(1)
        sig = rng.normal(0.0, 0.002, SR * 2)
        sig[4000:8000] += 0.012 * np.sin(
            2.0 * np.pi * 300.0 * np.arange(4000) / SR
        )
        det = SilenceDetector(DetectParams(threshold_db=-38.0))
        baseline = det.detect(sig, SR)
        sig[12000:12080] += 0.9  # short burst, under confirmation time
        burst = det.detect(sig, SR)
        self.assertAlmostEqual(
            burst.close_threshold_db, baseline.close_threshold_db, places=6
        )

    def test_param_fingerprint_changes_with_parameters(self):
        self.assertNotEqual(
            DetectParams(threshold_db=-38.0).fingerprint(),
            DetectParams(threshold_db=-30.0).fingerprint(),
        )

    def test_invalid_parameters_rejected(self):
        for kwargs in (
            {"hop_ms": 30.0, "frame_ms": 20.0},
            {"noise_percentile": 120.0},
            {"confirm_ms": -1.0},
        ):
            with self.assertRaises(ValueError):
                DetectParams(**kwargs).validate()


if __name__ == "__main__":
    unittest.main()
