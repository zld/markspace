"""Algorithm boundaries for the detector and merge stages."""
from __future__ import annotations

import unittest

import numpy as np

from audiosplit.detector import (
    DetectorConfig,
    FrameEnergyDetector,
    amplitude_to_db,
    db_to_amplitude,
)
from audiosplit.merge import MergeConfig, build_segments, measure
from audiosplit.wav import WavReader

RATE = 48000


def detect(samples: np.ndarray, rate: int = RATE, **kwargs) -> object:
    detector = FrameEnergyDetector(rate, DetectorConfig(**kwargs))
    detector.push(samples.reshape(-1, 1))
    return detector.finish(int(samples.size))


def sine(size: int, amplitude: float, freq: float = 440.0, rate: int = RATE):
    t = np.arange(size, dtype=np.float64) / rate
    return (amplitude * np.sin(2 * np.pi * freq * t)).astype(np.float32)


class DetectorConfigTests(unittest.TestCase):
    def test_frame_and_hop_derive_from_rate(self) -> None:
        config = DetectorConfig(frame_ms=10)
        self.assertEqual(config.frame_samples(48000), 480)
        self.assertEqual(config.hop_samples(48000), 240)
        self.assertEqual(config.frame_samples(44100), 441)
        self.assertEqual(config.min_silence_samples(48000), 5760)

    def test_invalid_threshold_relationship_rejected(self) -> None:
        with self.assertRaisesRegex(ValueError, "off_db"):
            DetectorConfig(on_db=2, off_db=2).validate(RATE)
        with self.assertRaisesRegex(ValueError, "on_db"):
            DetectorConfig(on_db=0).validate(RATE)

    def test_invalid_frame_length_rejected(self) -> None:
        with self.assertRaises(ValueError):
            DetectorConfig(frame_ms=0).validate(RATE)
        with self.assertRaises(ValueError):
            DetectorConfig(frame_ms=float("nan")).validate(RATE)

    def test_db_round_trip(self) -> None:
        self.assertAlmostEqual(amplitude_to_db(db_to_amplitude(-6)), -6.0, places=6)
        self.assertEqual(amplitude_to_db(0.0), -120.0)


class DetectorStateTests(unittest.TestCase):
    def test_pure_silence_yields_no_events(self) -> None:
        result = detect(np.zeros(RATE, dtype=np.float32))
        self.assertEqual(result.events, [])
        self.assertEqual(result.total_samples, RATE)

    def test_steady_noise_yields_no_events(self) -> None:
        rng = np.random.default_rng(11)
        result = detect(rng.normal(0, 0.004, RATE).astype(np.float32))
        self.assertEqual(result.events, [])

    def test_single_sample_impulse_is_configurable(self) -> None:
        samples = np.zeros(RATE, dtype=np.float32)
        samples[20000] = 0.9
        raw = detect(samples)
        self.assertEqual(len(raw.events), 1)
        lenient = build_segments(
            raw.events,
            sample_rate=RATE,
            total_samples=RATE,
            config=MergeConfig(min_event_ms=0, min_silence_ms=0),
        )
        self.assertEqual(len(lenient), 1)
        strict = build_segments(
            raw.events,
            sample_rate=RATE,
            total_samples=RATE,
            config=MergeConfig(min_event_ms=30, min_silence_ms=0),
        )
        self.assertEqual(strict, [])

    def test_weak_event_survives_after_loud_event(self) -> None:
        rng = np.random.default_rng(5)
        samples = rng.normal(0, 0.003, RATE * 3).astype(np.float32)
        samples[4800:9600] += sine(4800, 0.30)
        samples[24000:28800] += sine(4800, 0.015, freq=300)
        result = detect(samples)
        self.assertEqual(len(result.events), 2)
        weak = result.events[1]
        self.assertLess(weak.start_sample, 24000)
        self.assertGreater(weak.end_sample, 28800)

    def test_event_running_to_eof_closes_at_last_sample(self) -> None:
        samples = np.zeros(RATE, dtype=np.float32)
        samples[30000:] = sine(RATE - 30000, 0.4)
        result = detect(samples)
        self.assertEqual(len(result.events), 1)
        self.assertEqual(result.events[0].end_sample, RATE)
        self.assertLessEqual(result.events[0].end_sample, result.total_samples)

    def test_attack_confirmation_needs_consecutive_frames(self) -> None:
        samples = np.zeros(RATE, dtype=np.float32)
        samples[10000:10500] = sine(500, 0.8)
        lenient = detect(samples, attack_frames=1)
        strict = detect(samples, attack_frames=8)
        self.assertEqual(len(lenient.events), 1)
        self.assertEqual(len(strict.events), 0)

    def test_int16_stereo_push_scales_correctly(self) -> None:
        rng = np.random.default_rng(7)
        mono = rng.normal(0, 0.004, RATE).astype(np.float32)
        pcm = np.repeat((mono * 32767).astype(np.int16)[:, None], 2, axis=1)
        detector = FrameEnergyDetector(RATE, DetectorConfig())
        detector.push(pcm)
        result = detector.finish(RATE)
        self.assertEqual(result.events, [])
        self.assertLess(detector.noise_floor_db, -35.0)


class MergeTests(unittest.TestCase):
    def _events(self, spans):
        from audiosplit.detector import DetectedEvent

        return [
            DetectedEvent(start, end, 0.5, 0.2, -50.0) for start, end in spans
        ]

    def test_short_gap_merges_and_long_gap_splits(self) -> None:
        events = self._events([(1000, 2000), (2100, 3000), (9000, 10000)])
        config = MergeConfig(min_silence_ms=100, min_event_ms=0, pre_roll=0, post_roll=0)
        segments = build_segments(
            events, sample_rate=1000, total_samples=20000, config=config
        )
        self.assertEqual(
            [(s.start_sample, s.end_sample) for s in segments],
            [(1000, 3000), (9000, 10000)],
        )

    def test_guard_samples_clip_to_file_bounds(self) -> None:
        events = self._events([(10, 50), (900, 990)])
        config = MergeConfig(min_silence_ms=0, min_event_ms=0, pre_roll=64, post_roll=64)
        segments = build_segments(
            events, sample_rate=1000, total_samples=1000, config=config
        )
        self.assertEqual(segments[0].start_sample, 0)
        self.assertEqual(segments[1].end_sample, 1000)

    def test_guard_samples_longer_than_file_rejected(self) -> None:
        config = MergeConfig(pre_roll=5000)
        with self.assertRaisesRegex(ValueError, "pre_roll"):
            build_segments(
                [], sample_rate=48000, total_samples=1000, config=config
            )

    def test_merge_keeps_lowest_noise_floor(self) -> None:
        from audiosplit.detector import DetectedEvent

        events = [
            DetectedEvent(0, 100, 0.4, 0.2, -40.0),
            DetectedEvent(150, 250, 0.4, 0.2, -55.0),
        ]
        segments = build_segments(
            events,
            sample_rate=1000,
            total_samples=1000,
            config=MergeConfig(min_silence_ms=100, min_event_ms=0),
        )
        self.assertEqual(len(segments), 1)
        self.assertAlmostEqual(segments[0].noise_floor_db, -55.0)

    def test_measure_reports_peak_and_rms(self) -> None:
        import tempfile
        from pathlib import Path

        from scripts.make_fixtures import write_pcm16

        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "tone.wav"
            samples = sine(RATE, 0.5)
            write_pcm16(path, samples, RATE)
            info = WavReader(path).info
            segments = build_segments(
                self._events([(0, info.total_samples)]),
                sample_rate=RATE,
                total_samples=info.total_samples,
                config=MergeConfig(min_event_ms=0),
            )
            measured = measure(str(path), info, segments)
        self.assertAlmostEqual(measured[0].peak, 0.5, delta=0.02)
        self.assertAlmostEqual(measured[0].rms, 0.5 / np.sqrt(2), delta=0.02)
        self.assertAlmostEqual(measured[0].duration_ms, 1000.0, delta=0.01)


if __name__ == "__main__":
    unittest.main()
