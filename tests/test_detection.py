"""Detector, boundary, WAV, and atomic-export edge cases."""

from __future__ import annotations

import struct
import unittest

import numpy as np

from audiosplit.audio_io import AudioError, read_wav
from audiosplit.config import ConfigError, DetectionConfig
from audiosplit.detector import detect
from audiosplit.exporter import ExportError, ExportSegment, export_run
from audiosplit.segments import Segment, SegmentPlan, finalize
from audiosplit.store import Store
from support import SR, Harness, write_wav


class EdgeCaseTests(Harness):
    def detect_plan(self, samples: np.ndarray, **kwargs):
        config = DetectionConfig(**kwargs)
        config.validate()
        result = detect(samples, SR, config)
        return finalize(result, samples, config)

    def assert_partition(self, plan) -> None:
        covered = [(s.start, s.end) for s in plan.segments] + list(plan.silence)
        covered.sort()
        if not covered:
            return
        self.assertEqual(covered[0][0], 0)
        self.assertEqual(covered[-1][1], plan.total_samples)
        for left, right in zip(covered, covered[1:]):
            self.assertEqual(left[1], right[0])

    def test_empty_file(self) -> None:
        plan = self.detect_plan(np.zeros(0))
        self.assertEqual(plan.segments, ())
        self.assertEqual(plan.silence, ())
        self.assertEqual(plan.silence_samples, 0)

    def test_all_silence(self) -> None:
        plan = self.detect_plan(np.zeros(SR))
        self.assertEqual(plan.segments, ())
        self.assertEqual(plan.silence, ((0, SR),))
        self.assertEqual(plan.silence_samples, SR)

    def test_single_sample_pulse_deterministic(self) -> None:
        samples = np.zeros(SR)
        samples[SR // 2] = 0.9
        options = dict(
            min_segment_ms=0.0, fade_ms=0.0, min_peak_dbfs=-120.0, onset_frames=1
        )
        first = self.detect_plan(samples, **options)
        second = self.detect_plan(samples, **options)
        self.assertEqual(first.segments, second.segments)
        self.assertEqual(first.silence, second.silence)
        self.assertEqual(len(first.segments), 1)
        self.assertLessEqual(first.segments[0].start, SR // 2)
        self.assertGreater(first.segments[0].end, SR // 2)
        self.assert_partition(first)

    def test_constant_noise_is_deterministic_and_quiet(self) -> None:
        rng = np.random.default_rng(7)
        samples = rng.normal(0.0, 0.002, SR * 2)
        first = self.detect_plan(samples)
        second = self.detect_plan(samples)
        self.assertEqual(first.segments, second.segments)
        self.assertEqual(len(first.segments), 0)
        self.assertEqual(first.silence_samples, SR * 2)

    def test_filtered_short_event_counted_as_silence(self) -> None:
        samples = np.zeros(SR)
        samples[1000:1882] = 0.4
        plan = self.detect_plan(
            samples, min_segment_ms=100.0, fade_ms=0.0, release_frames=0
        )
        self.assertEqual(plan.segments, ())
        self.assertEqual(plan.filtered_short, 1)
        self.assertEqual(plan.silence_samples, SR)

    def test_merge_short_gap(self) -> None:
        samples = np.zeros(SR)
        samples[1000:3000] = 0.4
        samples[3100:5000] = 0.4
        plan = self.detect_plan(samples, merge_ms=10.0)
        self.assertEqual(len(plan.segments), 1)
        self.assertLessEqual(plan.segments[0].start, 1000)
        self.assertGreaterEqual(plan.segments[0].end, 5000)

    def test_partition_is_exact_for_impulses(self) -> None:
        rng = np.random.default_rng(3)
        samples = rng.normal(0.0, 0.0005, SR)
        for start in (2000, 9000, 20000, 30000):
            samples[start : start + 300] += 0.2
        plan = self.detect_plan(samples, merge_ms=0.0)
        self.assert_partition(plan)
        self.assertGreater(len(plan.segments), 0)


class WavAndExportTests(Harness):
    def test_float_and_8bit_wav(self) -> None:
        write_wav(self.tmp / "f.wav", np.zeros(100), bits=32)
        write_wav(self.tmp / "b.wav", np.zeros(100), bits=8)
        config = DetectionConfig(min_sample_rate=1000)
        self.assertEqual(read_wav(self.tmp / "f.wav", config).frame_count, 100)
        self.assertEqual(read_wav(self.tmp / "b.wav", config).frame_count, 100)

    def test_non_finite_float_rejected(self) -> None:
        path = self.tmp / "bad.wav"
        pcm = np.array([0.0, np.nan, 0.0], dtype="<f4").tobytes()
        fmt = struct.pack("<HHIIHH", 3, 1, SR, SR * 4, 4, 32)
        payload = (
            b"RIFF"
            + struct.pack("<I", 4 + 24 + 8 + len(pcm))
            + b"WAVE"
            + b"fmt "
            + struct.pack("<I", len(fmt))
            + fmt
            + b"data"
            + struct.pack("<I", len(pcm))
            + pcm
        )
        path.write_bytes(payload)
        with self.assertRaises(AudioError):
            read_wav(path, DetectionConfig())

    def test_truncated_and_bad_header(self) -> None:
        path = self.tmp / "trunc.wav"
        path.write_bytes(
            b"RIFF" + struct.pack("<I", 100) + b"WAVEfmt " + struct.pack("<I", 16)
        )
        with self.assertRaises(AudioError):
            read_wav(path, DetectionConfig())
        with self.assertRaises(ConfigError):
            DetectionConfig(frame_ms=-1).validate()

    def test_export_failure_preserves_prior_manifest(self) -> None:
        samples = np.zeros(SR)
        samples[100:1100] = 0.2
        segment = ExportSegment(1, 0, 2000, 0, 0, -14.0, -20.0)
        config = DetectionConfig()
        kwargs = dict(
            samples=samples,
            sample_rate=SR,
            total_samples=SR,
            segments=[segment],
            silences=[(2000, SR)],
            config=config,
        )
        dest = self.tmp / "export"
        first = export_run(
            dest_root=dest, relative_path="a.wav", run_id=1, fingerprint="abc", **kwargs
        )
        before = first.manifest.read_bytes()
        emitter = self.tmp / "not_a_dir"
        emitter.write_text("x")
        with self.assertRaises(ExportError):
            export_run(
                dest_root=emitter,
                relative_path="a.wav",
                run_id=2,
                fingerprint="def",
                **kwargs,
            )
        self.assertEqual(first.manifest.read_bytes(), before)


class StoreTests(Harness):
    def test_rollback_keeps_previous_run(self) -> None:
        config = DetectionConfig()
        plan = SegmentPlan(
            segments=(Segment(1, 0, 100, 2, 2, -1.0, -2.0),),
            silence=((100, 200),),
            total_samples=200,
            sample_rate=SR,
            filtered_short=0,
            filtered_silent=0,
        )
        bad = SegmentPlan(
            segments=(Segment(1, 0, 200, 2, 2, -1.0, -2.0),),
            silence=((0, -5),),
            total_samples=200,
            sample_rate=SR,
            filtered_short=0,
            filtered_silent=0,
        )
        with Store(self.tmp / "db.sqlite") as store:
            store.save_run(
                batch_id="b1",
                fingerprint="f1",
                config=config,
                input_path="a",
                relative_path="a.wav",
                plan=plan,
            )
            with self.assertRaises(Exception):
                store.save_run(
                    batch_id="b2",
                    fingerprint="f2",
                    config=config,
                    input_path="b",
                    relative_path="b.wav",
                    plan=bad,
                )
            count = store.conn.execute("SELECT COUNT(*) FROM runs").fetchone()[0]
        self.assertEqual(count, 1)


if __name__ == "__main__":
    unittest.main()
