"""Equivalence and bounded-memory guarantees of the streaming segmenter."""

from __future__ import annotations

import tracemalloc
import unittest

import numpy as np

from audioslice.calibration import Calibrator
from audioslice.detector import (
    DetectorConfig,
    WindowAnalyzer,
    _build_segments,
    detect_from_blocks,
    label_windows,
)
from audioslice.streaming import StreamingSegmenter

SR = 48000


def _next_label(rms: float, calibration, voiced: bool, forced: bool | None) -> bool:
    if forced is not None:
        return forced
    if not voiced and rms >= calibration.on_threshold:
        return True
    if voiced and rms <= calibration.off_threshold:
        return False
    return voiced


def _run_pair(signal: np.ndarray, cfg: DetectorConfig):
    analyzer = WindowAnalyzer(cfg.window_frames)
    calibrator = Calibrator(cfg.noise_margin_db, cfg.hysteresis_db)
    metrics = []
    for metric in analyzer.process(signal):
        metrics.append(metric)
        calibrator.add(np.array([metric.rms]))
    metrics.extend(analyzer.flush())
    calibration = calibrator.finalize()
    forced = calibrator.homogeneous_label(calibration.on_threshold)
    rms = np.array([m.rms for m in metrics])
    labels = (
        np.full(rms.shape, forced, dtype=bool)
        if forced is not None
        else label_windows(rms, calibration.on_threshold, calibration.off_threshold)
    )
    offline = _build_segments(metrics, labels, cfg)

    analyzer = WindowAnalyzer(cfg.window_frames)
    segmenter = StreamingSegmenter(cfg)
    voiced = False
    for metric in analyzer.process(signal):
        voiced = _next_label(metric.rms, calibration, voiced, forced)
        segmenter.add(metric, voiced)
    for metric in analyzer.flush():
        voiced = _next_label(metric.rms, calibration, voiced, forced)
        segmenter.add(metric, voiced)
    return offline, segmenter.finalize()


class StreamingEquivalenceTest(unittest.TestCase):
    def setUp(self) -> None:
        self.cfg = DetectorConfig(
            sample_rate=SR, window_ms=20, min_silence_ms=120, min_segment_ms=40
        )

    def _random_signal(self, seed: int) -> np.ndarray:
        rng = np.random.default_rng(seed)
        frames = int(rng.integers(1, 300)) * self.cfg.window_frames
        signal = rng.normal(0, 5, frames)
        pos = 0
        while pos < frames:
            run_w = int(rng.integers(0, 12))
            gap_w = int(rng.integers(0, 12))
            pos += gap_w * self.cfg.window_frames
            end = min(frames, pos + run_w * self.cfg.window_frames)
            if end > pos:
                signal[pos:end] += rng.uniform(50, 3000)
            pos = end
        return signal

    def test_streaming_matches_offline_across_random_signals(self) -> None:
        for seed in range(300):
            with self.subTest(seed=seed):
                offline, streaming = _run_pair(self._random_signal(seed), self.cfg)
                self.assertEqual(
                    [s.as_dict() for s in offline[0]],
                    [s.as_dict() for s in streaming[0]],
                )
                self.assertEqual(
                    [s.as_dict() for s in offline[1]],
                    [s.as_dict() for s in streaming[1]],
                )

    def test_memory_does_not_grow_with_duration(self) -> None:
        cfg = DetectorConfig(sample_rate=SR, block_frames=65536,
                             window_ms=20, min_silence_ms=120)
        rng = np.random.default_rng(7)
        block = cfg.block_frames
        peaks = []
        for minutes in (2, 20):
            total = SR * 60 * minutes

            def factory():
                produced = 0
                while produced < total:
                    n = min(block, total - produced)
                    data = rng.normal(0, 6, (n, 2))
                    if (produced // SR) % 2 == 0:
                        data += 1500
                    produced += n
                    yield data

            tracemalloc.start()
            result = detect_from_blocks(factory, cfg)
            _, peak = tracemalloc.get_traced_memory()
            tracemalloc.stop()
            peaks.append(peak)
            self.assertEqual(result.total_frames, total)
        # Tenfold more audio must not meaningfully increase retained memory.
        self.assertLess(peaks[1], peaks[0] * 2.0 + 2 * 1024 * 1024)


if __name__ == "__main__":
    unittest.main()
