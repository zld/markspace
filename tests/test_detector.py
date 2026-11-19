"""Unit tests for detector boundaries, hysteresis and merge rules."""

import unittest

import numpy as np

from audioslice.detector import detect, frame_rms
from audioslice.model import AnalysisParams, AudioData, Region

SR = 1000  # 1 kHz test rate: 1 sample == 1 ms, easy arithmetic


def audio(samples: np.ndarray, rate: int = SR) -> AudioData:
    return AudioData(
        samples=np.asarray(samples, dtype=np.float64),
        sample_rate=rate,
        channels=1,
        sample_width_bytes=2,
    )


def params(**overrides) -> AnalysisParams:
    defaults = dict(frame_ms=10.0, min_segment_ms=30.0, min_silence_ms=50.0)
    defaults.update(overrides)
    return AnalysisParams.from_milliseconds(sample_rate=SR, **defaults)


def burst(n: int, amplitude: float = 0.5) -> np.ndarray:
    return np.full(n, amplitude, dtype=np.float64)


class FrameRmsTests(unittest.TestCase):
    def test_frames_cover_tail_partial_frame(self) -> None:
        samples = np.ones(25, dtype=np.float64)
        rms, edges = frame_rms(samples, 10)
        self.assertEqual(rms.shape[0], 3)
        np.testing.assert_array_equal(edges, [0, 10, 20, 25])
        self.assertAlmostEqual(rms[-1], 1.0)
        self.assertTrue(np.all(np.isfinite(rms)))


class TilingInvariantTests(unittest.TestCase):
    def _assert_tiling(self, result, total: int) -> None:
        cursor = 0
        previous = None
        for region in result.regions:
            self.assertFalse(region.is_empty(), "no zero-length regions allowed")
            self.assertEqual(region.start_sample, cursor)
            self.assertNotEqual(region.kind, previous)
            cursor = region.end_sample
            previous = region.kind
        self.assertEqual(cursor, total)

    def test_leading_and_trailing_silence_recorded(self) -> None:
        samples = np.concatenate([np.zeros(100), burst(100), np.zeros(100)])
        result = detect(audio(samples), params())
        kinds = [r.kind for r in result.regions]
        self.assertEqual(kinds, ["silence", "segment", "silence"])
        self.assertEqual(result.regions[0].start_sample, 0)
        self.assertEqual(result.regions[-1].end_sample, 300)
        self._assert_tiling(result, 300)

    def test_signal_starting_and_ending_loud(self) -> None:
        samples = burst(200)
        result = detect(audio(samples), params())
        self.assertEqual([r.kind for r in result.regions], ["segment"])
        self.assertEqual((result.regions[0].start_sample, result.regions[0].end_sample), (0, 200))
        self._assert_tiling(result, 200)

    def test_pure_silence_is_one_silence_region(self) -> None:
        samples = np.zeros(500, dtype=np.float64)
        result = detect(audio(samples), params())
        self.assertEqual(len(result.regions), 1)
        self.assertEqual(result.regions[0], Region("silence", 0, 500))
        self.assertEqual(result.segments, ())

    def test_low_level_room_tone_not_classified_as_speech(self) -> None:
        rng = np.random.default_rng(7)
        tone = rng.normal(0.0, 0.002, size=800)
        result = detect(audio(tone), params())
        self.assertEqual([r.kind for r in result.regions], ["silence"])
        self.assertLess(result.enter_threshold_rms, 0.01)


class MergeRuleTests(unittest.TestCase):
    def test_short_gap_merges_into_one_segment(self) -> None:
        # 30 ms gap < min_silence 50 ms -> single merged segment.
        samples = np.concatenate([burst(80), np.zeros(30), burst(80)])
        result = detect(audio(samples), params())
        self.assertEqual(len(result.segments), 1)
        segment = result.segments[0]
        self.assertEqual(segment.start_sample, 0)
        self.assertEqual(segment.end_sample, 190)
        self.assertEqual(result.silences, ())

    def test_long_silence_splits_into_two_segments(self) -> None:
        # A 80 ms gap (>= min_silence 50 ms) splits the material into two
        # segments with a real interior silence between them.  Frame-level
        # hysteresis can shift the exact edge by a frame, so assert on the
        # structural outcome rather than a hard-coded sample count.
        samples = np.concatenate([np.zeros(20), burst(80), np.zeros(80), burst(80), np.zeros(20)])
        result = detect(audio(samples), params())
        kinds = [r.kind for r in result.regions]
        self.assertEqual(kinds, ["silence", "segment", "silence", "segment", "silence"])
        interior = result.regions[2]
        self.assertEqual(interior.kind, "silence")
        self.assertGreaterEqual(interior.length_samples, params().min_silence_samples - params().frame_samples)
        self.assertEqual(len(result.segments), 2)

    def test_sub_minimum_isolated_sound_absorbed(self) -> None:
        # A 10 ms blip (< min_segment 30 ms) alone in silence must not survive.
        samples = np.concatenate([np.zeros(60), burst(10), np.zeros(60)])
        result = detect(audio(samples), params())
        self.assertEqual(result.segments, ())
        self.assertEqual(len(result.silences), 1)
        self.assertEqual(result.silences[0], Region("silence", 0, 130))

    def test_short_segment_between_two_merges_into_neighbor(self) -> None:
        # An 8 ms blip (well under min_segment 30 ms, even after frame-grid
        # refinement), separated from a real segment by a long 70 ms silence,
        # is isolated and must be absorbed into silence rather than emitted as
        # its own sub-minimum segment.
        samples = np.concatenate([burst(60), np.zeros(70), burst(8), np.zeros(60)])
        result = detect(audio(samples), params())
        # The only surviving segment is the long leading one; no extra segment.
        self.assertEqual(len(result.segments), 1)
        self.assertEqual(result.segments[0].start_sample, 0)
        # Everything from the end of that segment onward is tiled silence.
        self.assertTrue(all(r.kind == "silence" for r in result.regions[1:]))


class BoundaryRefinementTests(unittest.TestCase):
    def test_fade_in_boundary_inside_frame_not_at_frame_edge(self) -> None:
        # Linear fade over the first frame: onset must land inside [0, 10),
        # never snapped to the frame boundary at sample 10.
        samples = np.zeros(120, dtype=np.float64)
        ramp = np.linspace(0.0, 0.8, 10)
        samples[:10] = ramp
        samples[10:80] = 0.8
        result = detect(audio(samples), params())
        start = result.segments[0].start_sample
        self.assertGreater(start, 0)
        self.assertLess(start, 10)

    def test_fade_out_boundary_inside_frame(self) -> None:
        rng = np.random.default_rng(11)
        samples = rng.normal(0.0, 0.002, size=120)  # faint room tone, never digital zero
        samples[:80] += 0.8
        samples[80:90] += np.linspace(0.8, 0.0, 10)  # 10 ms fade-out in one frame
        result = detect(audio(samples), params())
        end = result.segments[0].end_sample
        # The crossing is resolved at sample resolution within the fade frame,
        # not snapped to the next frame grid line at sample 100.  A one-sample
        # sliding-window overshoot past sample 90 is an acceptable discrete edge.
        self.assertGreater(end, 80)
        self.assertLessEqual(end, 91)
        self.assertLess(end, 100)

    def test_hysteresis_uses_lower_exit_threshold(self) -> None:
        result = detect(audio(burst(100)), params(margin_db=12.0, hysteresis_db=6.0))
        self.assertLess(result.exit_threshold_rms, result.enter_threshold_rms)
        ratio = result.enter_threshold_rms / result.exit_threshold_rms
        self.assertAlmostEqual(ratio, 10.0 ** (6.0 / 20.0), places=5)

    def test_non_finite_samples_rejected(self) -> None:
        samples = np.full(50, 0.5)
        samples[20] = np.nan
        with self.assertRaises(ValueError):
            detect(audio(samples), params())


if __name__ == "__main__":
    unittest.main()
