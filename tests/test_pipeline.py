"""End-to-end and boundary tests for the timeline summary pipeline."""
from __future__ import annotations

import json
import os
import subprocess
import sys
import tempfile
import unittest
from fractions import Fraction
from pathlib import Path

import numpy as np

from timeline.audio_sync import (
    AudioCueConfig,
    AudioSyncError,
    measure_alignment,
)
from timeline.clock import ClockError, FrameClock
from timeline.compositor import compose_frame
from timeline.exporter import (
    SummaryWriteError,
    atomic_write_json,
    export_summary,
)
from timeline.fixture import build_demo_timeline
from timeline.model import Clip, Keyframe, Layer, Timeline, TimelineError

REPO_ROOT = Path(__file__).resolve().parent.parent


def solid_clip(name, mode, rgba, start=0, end=1, keyframes=()):
    image = np.zeros((1, 1, 4), dtype=np.uint8)
    image[0, 0] = rgba
    return Clip(name, start, end, mode, keyframes, image)


def tiny_timeline(clips, frame_count=1, size=(1, 1)):
    return Timeline(24, frame_count, size[0], size[1], [Layer("l", clips)])


class ClockTests(unittest.TestCase):
    def test_fps_bounds(self):
        FrameClock(1)
        FrameClock(120)
        for bad in (0, 121, -24):
            with self.assertRaises(ClockError):
                FrameClock(bad)

    def test_rational_intervals_are_exact_and_contiguous(self):
        clock = FrameClock(24)
        start, end = clock.frame_interval(47)
        self.assertEqual(start, Fraction(47, 24))
        self.assertEqual(end, Fraction(48, 24))
        self.assertEqual(end, clock.frame_start(48))

    def test_frames_for_duration(self):
        self.assertEqual(FrameClock(24).frames_for_duration(2.0), 48)
        self.assertEqual(FrameClock(24).frames_for_duration(0.01), 0)


class ModelTests(unittest.TestCase):
    def test_invalid_blend_mode_rejected(self):
        with self.assertRaises(TimelineError):
            solid_clip("bad", "overlay", (0, 0, 0, 255))

    def test_zero_frame_timeline_rejected(self):
        with self.assertRaises(TimelineError):
            tiny_timeline([], frame_count=0).validate()

    def test_oversized_timeline_rejected(self):
        with self.assertRaises(TimelineError):
            Timeline(24, 1, 99999, 1, []).validate()

    def test_keyframe_interpolation(self):
        clip = solid_clip(
            "k", "source-over", (0, 0, 0, 255), keyframes=(
                Keyframe(0, 0.0, 0.0, 0.0), Keyframe(10, 1.0, 20.0, -10.0),
            )
        )
        opacity, x, y = clip.sample(5)
        self.assertAlmostEqual(opacity, 0.5)
        self.assertAlmostEqual(x, 10.0)
        self.assertAlmostEqual(y, -5.0)


class CompositorTests(unittest.TestCase):
    def compose_pixel(self, clips):
        return compose_frame(tiny_timeline(clips), 0)[0, 0].tolist()

    def test_source_over(self):
        px = self.compose_pixel([
            solid_clip("bg", "source-over", (10, 20, 30, 255)),
            solid_clip("fg", "source-over", (200, 100, 50, 128)),
        ])
        self.assertEqual(px, [105, 60, 40, 255])

    def test_multiply(self):
        px = self.compose_pixel([
            solid_clip("bg", "source-over", (128, 128, 128, 255)),
            solid_clip("fg", "multiply", (128, 128, 128, 255)),
        ])
        self.assertEqual(px, [64, 64, 64, 255])

    def test_screen(self):
        px = self.compose_pixel([
            solid_clip("bg", "source-over", (128, 128, 128, 255)),
            solid_clip("fg", "screen", (128, 128, 128, 255)),
        ])
        self.assertEqual(px, [192, 192, 192, 255])

    def test_empty_clip_is_skipped(self):
        empty = solid_clip("e", "screen", (255, 255, 255, 255), start=3, end=3)
        px = self.compose_pixel([solid_clip("bg", "source-over", (7, 7, 7, 255)), empty])
        self.assertEqual(px, [7, 7, 7, 255])

    def test_off_canvas_clip_is_clipped(self):
        image = np.full((2, 2, 4), 255, dtype=np.uint8)
        clip = Clip("off", 0, 1, "source-over",
                    (Keyframe(0, 1.0, 50.0, 50.0),), image)
        frame = compose_frame(tiny_timeline([clip], size=(4, 4)), 0)
        self.assertTrue((frame == 0).all())

    def test_compositing_is_deterministic(self):
        timeline = build_demo_timeline(24, 48, 320, 180)
        first = compose_frame(timeline, 17).tobytes()
        second = compose_frame(timeline, 17).tobytes()
        self.assertEqual(first, second)


class AudioSyncTests(unittest.TestCase):
    def test_alignment_reports_offset_plus_drift(self):
        stats = measure_alignment(
            FrameClock(24), 48, AudioCueConfig(48000, 13.0, 25.0)
        )
        self.assertEqual(stats.cues_detected, 48)
        self.assertGreaterEqual(stats.av_max_error_ms, 13.0)
        self.assertLessEqual(stats.av_max_error_ms, 13.2)

    def test_zero_frames_rejected(self):
        with self.assertRaises(AudioSyncError):
            measure_alignment(FrameClock(24), 0, AudioCueConfig(48000, 0.0, 0.0))


class ExporterTests(unittest.TestCase):
    def test_summary_contains_only_hashes_and_stats(self):
        timeline = build_demo_timeline(24, 48, 320, 180)
        stats = measure_alignment(FrameClock(24), 48, AudioCueConfig(48000, 13.0, 25.0))
        with tempfile.TemporaryDirectory() as tmp:
            path = os.path.join(tmp, "demo.json")
            summary = export_summary(timeline, stats, path)
            self.assertEqual(summary["frame_count"], 48)
            self.assertEqual(summary["frame_sha256_count"], 48)
            self.assertEqual(len(summary["frames"]), 48)
            self.assertTrue(all(len(f["sha256"]) == 64 for f in summary["frames"]))
            self.assertEqual(os.listdir(tmp), ["demo.json"])  # no frame/audio files
            with open(path, encoding="utf-8") as fh:
                on_disk = json.load(fh)
            self.assertEqual(on_disk["frame_count"], 48)

    def test_failed_write_keeps_previous_summary(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = os.path.join(tmp, "demo.json")
            atomic_write_json(path, {"frame_count": 1})
            os.chmod(tmp, 0o500)
            try:
                with self.assertRaises(SummaryWriteError):
                    atomic_write_json(path, {"frame_count": 2})
            finally:
                os.chmod(tmp, 0o700)
            with open(path, encoding="utf-8") as fh:
                self.assertEqual(json.load(fh), {"frame_count": 1})


class CliTests(unittest.TestCase):
    def run_cli(self, *argv):
        return subprocess.run(
            [sys.executable, "-m", "timeline.cli", *argv],
            cwd=REPO_ROOT, capture_output=True, text=True,
        )

    def test_demo_end_to_end(self):
        with tempfile.TemporaryDirectory() as tmp:
            summary_path = os.path.join(tmp, "demo.json")
            proc = self.run_cli(
                "demo", "--fps", "24", "--duration", "2", "--size", "320x180",
                "--sample-rate", "48000", "--offset-ms", "13", "--drift-ppm", "25",
                "--summary", summary_path,
            )
            self.assertEqual(proc.returncode, 0, proc.stderr)
            frame_lines = [l for l in proc.stdout.splitlines() if l.startswith("frame_index=")]
            self.assertEqual(len(frame_lines), 48)
            self.assertIn("frame_count=48", proc.stdout)
            self.assertIn("frame_sha256_count=48", proc.stdout)
            with open(summary_path, encoding="utf-8") as fh:
                data = json.load(fh)
            self.assertEqual(len(data["frames"]), 48)
            self.assertGreaterEqual(data["audio"]["av_max_error_ms"], 13.0)
            self.assertLessEqual(data["audio"]["av_max_error_ms"], 13.2)

    def test_invalid_fps_exits_2(self):
        proc = self.run_cli("demo", "--fps", "240", "--duration", "1", "--size", "8x8")
        self.assertEqual(proc.returncode, 2)

    def test_zero_frames_exits_2(self):
        proc = self.run_cli("demo", "--fps", "24", "--duration", "0.001", "--size", "8x8")
        self.assertEqual(proc.returncode, 2)

    def test_write_failure_exits_4(self):
        with tempfile.TemporaryDirectory() as tmp:
            blocker = os.path.join(tmp, "blocker")
            Path(blocker).write_text("not a directory")
            proc = self.run_cli(
                "demo", "--fps", "24", "--duration", "1", "--size", "8x8",
                "--summary", os.path.join(blocker, "demo.json"),
            )
            self.assertEqual(proc.returncode, 4)


if __name__ == "__main__":
    unittest.main()
