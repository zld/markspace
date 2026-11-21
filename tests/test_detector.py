"""Detector edge cases: hysteresis, pop merging, fades and defined empties."""

from __future__ import annotations

import unittest

import numpy as np

from audioslice.calibration import calibrate_rms
from audioslice.detector import (
    DetectorConfig,
    WindowAnalyzer,
    detect_samples,
    label_windows,
)

SR = 48000


def config(**overrides) -> DetectorConfig:
    defaults = dict(sample_rate=SR, window_ms=20, noise_margin_db=8,
                    min_silence_ms=120, min_segment_ms=40, block_frames=4096)
    defaults.update(overrides)
    return DetectorConfig(**defaults)


class WindowAnalyzerTest(unittest.TestCase):
    def test_block_size_does_not_change_windows(self) -> None:
        rng = np.random.default_rng(3)
        signal = rng.normal(0, 50, SR)
        window = config().window_frames
        reference = None
        for block_frames in (1, 97, 960, window, window + 1, 100_000):
            analyzer = WindowAnalyzer(window)
            metrics = []
            for start in range(0, signal.size, block_frames):
                metrics.extend(analyzer.process(signal[start:start + block_frames]))
            metrics.extend(analyzer.flush())
            summary = [(m.start_sample, m.length, round(m.rms, 9), round(m.peak, 9))
                       for m in metrics]
            if reference is None:
                reference = summary
            else:
                self.assertEqual(summary, reference)

    def test_partial_final_window_is_emitted(self) -> None:
        window = config().window_frames
        analyzer = WindowAnalyzer(window)
        analyzer.process(np.zeros(window + 7))
        tail = analyzer.flush()
        self.assertEqual(len(tail), 1)
        self.assertEqual(tail[0].length, 7)
        self.assertEqual(tail[0].start_sample, window)
        self.assertEqual(analyzer.flush(), [])

    def test_carry_state_across_tiny_blocks(self) -> None:
        window = config().window_frames
        analyzer = WindowAnalyzer(window)
        out = []
        for sample in np.ones(window * 2):
            out.extend(analyzer.process(np.array([sample])))
        self.assertEqual(len(out), 2)
        self.assertTrue(all(m.rms == 1.0 and m.peak == 1.0 for m in out))


class HysteresisTest(unittest.TestCase):
    def test_dual_threshold_holds_state_between_bands(self) -> None:
        on, off = 10.0, 7.0
        rms = np.array([1.0, 11.0, 8.0, 8.0, 6.0, 1.0, 11.0])
        labels = label_windows(rms, on, off)
        np.testing.assert_array_equal(
            labels, [False, True, True, True, False, False, True]
        )

    def test_calibration_orders_thresholds(self) -> None:
        rms = np.concatenate([np.full(40, 8.0), np.full(40, 2000.0)])
        cal = calibrate_rms(rms, noise_margin_db=8, hysteresis_db=3)
        self.assertGreater(cal.on_threshold, cal.off_threshold)
        self.assertGreater(cal.off_threshold, cal.noise_rms)
        self.assertEqual(cal.window_count, 80)


class SegmentationTest(unittest.TestCase):
    def test_two_segments_with_annotated_silence(self) -> None:
        rng = np.random.default_rng(17)
        signal = rng.normal(0, 6, 39360)
        for lo, hi, freq in ((4800, 19200, 440.0), (24000, 38400, 554.0)):
            idx = np.arange(lo, hi)
            signal[lo:hi] += 2200 * np.sin(2 * np.pi * freq * idx / SR)
        result = detect_samples(signal, config())
        self.assertEqual(
            [s.as_dict() for s in result.segments],
            [{"start_sample": 4800, "end_sample": 19200},
             {"start_sample": 24000, "end_sample": 38400}],
        )
        self.assertEqual(result.silence_total_seconds, 0.22)
        self.assertEqual(
            [s.as_dict() for s in result.silences],
            [{"start_sample": 0, "end_sample": 4800},
             {"start_sample": 19200, "end_sample": 24000},
             {"start_sample": 38400, "end_sample": 39360}],
        )

    def test_empty_and_short_inputs_are_defined(self) -> None:
        empty = detect_samples(np.zeros(0), config())
        self.assertEqual(empty.segments, ())
        self.assertEqual(empty.silences, ())
        self.assertEqual(empty.total_frames, 0)

        partial = detect_samples(np.zeros(config().window_frames - 5), config())
        self.assertEqual(partial.window_count, 1)
        self.assertEqual(partial.segments, ())
        self.assertEqual(partial.silences[0].length, config().window_frames - 5)

    def test_all_silence_and_all_voiced(self) -> None:
        silent = detect_samples(np.zeros(SR), config())
        self.assertEqual(silent.segments, ())
        self.assertEqual(silent.silences[0].as_dict(),
                         {"start_sample": 0, "end_sample": SR})
        voiced = detect_samples(np.full(SR, 3000.0), config())
        self.assertEqual(voiced.silences, ())
        self.assertEqual(voiced.segments[0].as_dict(),
                         {"start_sample": 0, "end_sample": SR})

    def test_fade_in_out_keeps_single_segment(self) -> None:
        window = config().window_frames
        signal = np.zeros(SR)
        ramp = np.linspace(0, 2000, 10 * window)
        body = np.concatenate([ramp, np.full(15 * window, 2000.0), ramp[::-1]])
        signal[5 * window:5 * window + body.size] = body
        result = detect_samples(signal, config())
        self.assertEqual(len(result.segments), 1)

    def test_short_pops_below_min_segment_are_ignored_alone(self) -> None:
        window = config().window_frames
        signal = np.zeros(SR)
        signal[10 * window:10 * window + window] = 3000  # one 20 ms pop
        result = detect_samples(signal, config(min_segment_ms=40))
        self.assertEqual(result.segments, ())

    def test_pop_merges_across_short_silence_only(self) -> None:
        window = config().window_frames
        signal = np.zeros(SR)
        signal[10 * window:30 * window] = 3000
        signal[32 * window:33 * window] = 3000  # 40 ms gap < 120 ms cap
        merged = detect_samples(signal, config())
        self.assertEqual(len(merged.segments), 1)
        self.assertEqual(merged.segments[0].end_sample, 33 * window)

        signal[20 * window:21 * window] = 3000  # elsewhere, 200 ms gap >= cap
        split = detect_samples(signal, config())
        self.assertFalse(
            any(s.start_sample <= 10 * window and s.end_sample == 21 * window
                for s in split.segments)
        )

    def test_parameter_changes_change_segments(self) -> None:
        rng = np.random.default_rng(5)
        signal = rng.normal(0, 6, 2 * SR)
        idx = np.arange(SR)
        signal[SR // 4:SR // 2] += 1500 * np.sin(2 * np.pi * 330 * idx[:SR // 4] / SR)
        strict = detect_samples(signal, config(min_segment_ms=200))
        loose = detect_samples(signal, config(min_segment_ms=20))
        self.assertGreaterEqual(len(loose.segments), len(strict.segments))

    def test_stereo_downmix_matches_mono(self) -> None:
        rng = np.random.default_rng(17)
        mono = rng.normal(0, 6, 39360)
        for lo, hi, freq in ((4800, 19200, 440.0), (24000, 38400, 554.0)):
            idx = np.arange(lo, hi)
            mono[lo:hi] += 2200 * np.sin(2 * np.pi * freq * idx / SR)
        stereo = np.repeat(mono[:, None], 2, axis=1)
        mono_result = detect_samples(mono, config())
        stereo_result = detect_samples(stereo, config())
        self.assertEqual(
            [s.as_dict() for s in stereo_result.segments],
            [s.as_dict() for s in mono_result.segments],
        )


if __name__ == "__main__":
    unittest.main()
