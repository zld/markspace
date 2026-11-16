"""End-to-end and unit tests for the timeline_audit pipeline."""

from __future__ import annotations

import io
import json
import tempfile
import unittest
from contextlib import redirect_stderr, redirect_stdout
from fractions import Fraction
from pathlib import Path

import numpy as np

from timeline_audit import assets, audio, compositor, curves, model, timebase
from timeline_audit.__main__ import main
from timeline_audit.exporter import SummaryWriter, frame_sha256
from timeline_audit.renderer import render_all


def demo_spec(frames=12, fps="30000/1001", width=64, height=48):
    duration = Fraction(frames) / timebase.parse_fps(fps)
    return {
        "fps": fps,
        "width": width,
        "height": height,
        "duration": str(duration),
        "layers": [
            {"id": "base", "asset": "base.png", "start": 0,
             "end": str(duration), "position": [0, 0], "scale": 0.25,
             "blend": "normal", "opacity": 1.0},
            {"id": "glow", "asset": "glow.png", "start": 0,
             "end": str(duration), "position": [-10, 4], "scale": 0.5,
             "blend": "screen",
             "opacity": [{"frame": 0, "value": 0.0, "interp": "linear"},
                         {"frame": 6, "value": 1.0, "interp": "hold"},
                         {"frame": 11, "value": 1.0, "interp": "linear"}]},
            {"id": "fx", "asset": "particles.png", "start": 0,
             "end": str(duration), "position": [30, 20], "scale": 0.0,
             "blend": "add", "opacity": 0.5},
            {"id": "gone", "asset": "missing.png", "start": 0,
             "end": str(duration), "position": [0, 0], "scale": 1.0,
             "blend": "multiply", "opacity": 1.0},
        ],
        "audio": {"sample_rate": 48000, "beeps": [
            {"time": str(duration * Fraction(1, 3)), "freq": 880.0,
             "duration": 0.05}]},
    }


class TimebaseTests(unittest.TestCase):
    def test_rational_fps(self):
        fps = timebase.parse_fps("30000/1001")
        self.assertEqual(fps, Fraction(30000, 1001))
        self.assertEqual(timebase.format_fps(fps), "30000/1001")
        self.assertEqual(timebase.parse_fps("30"), 30)

    def test_invalid_fps(self):
        for bad in ("abc", "1/0", "0", "-24", None):
            with self.assertRaises(ValueError):
                timebase.parse_fps(bad)

    def test_frame_count_partial_tail_and_zero(self):
        fps = timebase.parse_fps("30000/1001")
        self.assertEqual(timebase.frame_count(fps, Fraction(0)), 0)
        self.assertEqual(timebase.frame_count(fps, Fraction(1, 2**20)), 1)
        self.assertEqual(timebase.frame_count(fps, Fraction(1001, 30000)), 1)
        self.assertEqual(timebase.frame_count(fps, Fraction(2)), 60)

    def test_nearest_frame(self):
        fps = timebase.parse_fps("30000/1001")
        self.assertEqual(timebase.sample_to_frame(fps, 48000, 24024), 15)


class ModelTests(unittest.TestCase):
    def test_valid(self):
        tl = model.parse_timeline(demo_spec())
        self.assertEqual(tl.frames, 12)
        self.assertEqual(len(tl.layers), 4)

    def test_invalid_fps_located(self):
        spec = demo_spec(fps="0")
        with self.assertRaises(model.TimelineError) as ctx:
            model.parse_timeline(spec)
        self.assertIn("fps", str(ctx.exception))

    def test_negative_duration(self):
        spec = demo_spec()
        spec["duration"] = "-1"
        with self.assertRaises(model.TimelineError) as ctx:
            model.parse_timeline(spec)
        self.assertIn("duration", str(ctx.exception))

    def test_conflicting_duplicate_keyframes(self):
        spec = demo_spec()
        spec["layers"][1]["opacity"] = [
            {"frame": 3, "value": 0.2},
            {"frame": 3, "value": 0.7},
        ]
        with self.assertRaises(model.TimelineError) as ctx:
            model.parse_timeline(spec)
        msg = str(ctx.exception)
        self.assertIn("layers[1]", msg)
        self.assertIn("frame 3", msg)

    def test_identical_duplicate_keyframes_deduped(self):
        spec = demo_spec()
        spec["layers"][1]["opacity"] = [
            {"frame": 3, "value": 0.5},
            {"frame": 3, "value": 0.5},
        ]
        tl = model.parse_timeline(spec)
        self.assertEqual(len(tl.layers[1].opacity), 1)


class CurveTests(unittest.TestCase):
    def test_linear_and_hold(self):
        keys = (curves.Keyframe(0, 0.0, "linear"),
                curves.Keyframe(10, 1.0, "hold"),
                curves.Keyframe(20, 0.0, "linear"))
        self.assertAlmostEqual(curves.evaluate(keys, 5), 0.5)
        self.assertAlmostEqual(curves.evaluate(keys, 15), 1.0)
        self.assertAlmostEqual(curves.evaluate(keys, -3), 0.0)
        self.assertAlmostEqual(curves.evaluate(keys, 99), 0.0)


class CompositorTests(unittest.TestCase):
    def test_source_over_half_alpha(self):
        canvas = np.zeros((1, 1, 4), dtype=np.uint8)
        canvas[0, 0] = (0, 0, 0, 255)
        layer = np.full((1, 1, 4), (255, 255, 255, 128), dtype=np.uint8)
        compositor.composite_region(canvas, layer, 0, 0, 1.0, "normal")
        r, _, _, a = canvas[0, 0]
        self.assertEqual(a, 255)
        self.assertIn(int(r), (127, 128))  # not darkened toward black

    def test_blend_modes(self):
        for mode, expect in (("multiply", 32), ("screen", 223), ("add", 255)):
            canvas = np.full((1, 1, 4), (64, 64, 64, 255), dtype=np.uint8)
            layer = np.full((1, 1, 4), (192, 192, 192, 255), dtype=np.uint8)
            compositor.composite_region(canvas, layer, 0, 0, 1.0, mode)
            self.assertEqual(int(canvas[0, 0, 0]), expect, mode)

    def test_clipping_and_zero_size(self):
        canvas = np.zeros((4, 4, 4), dtype=np.uint8)
        layer = np.full((4, 4, 4), (10, 20, 30, 255), dtype=np.uint8)
        compositor.composite_region(canvas, layer, -2, -2, 1.0, "normal")
        self.assertEqual(int(canvas[0, 0, 0]), 10)
        self.assertEqual(int(canvas[3, 3, 0]), 0)
        compositor.composite_region(canvas, layer[:0], 0, 0, 1.0, "normal")
        compositor.composite_region(canvas, layer, 99, 99, 1.0, "normal")
        self.assertEqual(int(canvas.sum()), 4 * 3 * 10 + 4 * 3 * 20
                         + 4 * 3 * 30 + 4 * 255)


class AssetTests(unittest.TestCase):
    def test_generate_and_load(self):
        with tempfile.TemporaryDirectory() as tmp:
            names = assets.generate_demo_assets(tmp)
            arr = assets.load_png(tmp, names["base"])
            self.assertEqual(arr.shape, (144, 256, 4))
            self.assertEqual(arr.dtype, np.uint8)

    def test_path_escape_rejected(self):
        with tempfile.TemporaryDirectory() as tmp:
            assets.generate_demo_assets(tmp)
            with self.assertRaises(assets.AssetError):
                assets.load_png(tmp, "../outside.png")
            with self.assertRaises(assets.AssetError):
                assets.load_png(tmp, "base.txt")


class AudioTests(unittest.TestCase):
    def test_alignment_within_half_frame(self):
        tl = model.parse_timeline(demo_spec(frames=60))
        err = audio.max_alignment_error_ms(tl)
        half_frame_ms = float(1000 * Fraction(1001, 30000) / 2)
        self.assertLessEqual(err, half_frame_ms + 1e-9)
        self.assertGreater(err, 0.0)

    def test_synth_length_and_zero_frames(self):
        tl = model.parse_timeline(demo_spec(frames=30))
        pcm = audio.synth_beeps(tl)
        self.assertEqual(pcm.dtype, np.int16)
        self.assertEqual(len(pcm), round(float(tl.duration) * 48000))
        self.assertGreater(np.abs(pcm).max(), 0)
        tl0 = model.parse_timeline(demo_spec(frames=0))
        self.assertEqual(len(audio.synth_beeps(tl0)), 0)
        self.assertEqual(audio.max_alignment_error_ms(tl0), 0.0)


class PipelineTests(unittest.TestCase):
    def _run_demo(self, tmp, frames=12, workers=1, extra=()):
        out, err = io.StringIO(), io.StringIO()
        summary = str(Path(tmp) / "summary.json")
        argv = ["demo", "--width", "64", "--height", "48",
                "--fps", "30000/1001", "--frames", str(frames),
                "--workers", str(workers), "--summary", summary, *extra]
        with redirect_stdout(out), redirect_stderr(err):
            code = main(argv)
        return code, out.getvalue(), err.getvalue(), summary

    def test_demo_success_and_determinism(self):
        with tempfile.TemporaryDirectory() as tmp:
            code, out, _, path_a = self._run_demo(
                tmp, workers=1, extra=("--print-hashes",))
            self.assertEqual(code, 0)
            self.assertIn("frame_count=12", out)
            self.assertIn("fps=30000/1001", out)
            hash_lines = [l for l in out.splitlines() if "sha256=" in l]
            self.assertEqual(len(hash_lines), 12)
            for i, line in enumerate(hash_lines):
                self.assertIn(f"frame_index={i} ", line)
                self.assertEqual(len(line.rsplit("=", 1)[1]), 64)
            data = json.loads(Path(path_a).read_text())
            self.assertEqual(data["frame_count"], 12)
            self.assertLessEqual(data["av_sync"]["max_abs_error_ms"], 16.694)
            self.assertEqual(len(data["frames"]), 12)

            code, _, _, path_b = self._run_demo(tmp, workers=4)
            self.assertEqual(code, 0)
            self.assertEqual(Path(path_a).read_bytes(),
                             Path(path_b).read_bytes())

    def test_zero_frames(self):
        with tempfile.TemporaryDirectory() as tmp:
            code, out, _, path = self._run_demo(tmp, frames=0)
            self.assertEqual(code, 0)
            self.assertIn("frame_count=0", out)
            data = json.loads(Path(path).read_text())
            self.assertEqual(data["frames"], [])

    def test_invalid_args_exit_2(self):
        with tempfile.TemporaryDirectory() as tmp:
            code, _, err, _ = self._run_demo(tmp)
            self.assertEqual(code, 0)
            out, err = io.StringIO(), io.StringIO()
            with redirect_stdout(out), redirect_stderr(err):
                code = main(["demo", "--fps", "not-a-rate", "--frames", "5",
                             "--summary", str(Path(tmp) / "s.json")])
            self.assertEqual(code, 2)
            self.assertIn("fps", err.getvalue())

    def test_summary_write_failure_exit_3_and_cleanup(self):
        with tempfile.TemporaryDirectory() as tmp:
            bad = str(Path(tmp) / "no-such-dir" / "summary.json")
            out, err = io.StringIO(), io.StringIO()
            with redirect_stdout(out), redirect_stderr(err):
                code = main(["demo", "--width", "32", "--height", "32",
                             "--frames", "3", "--summary", bad])
            self.assertEqual(code, 3)
            self.assertFalse(Path(bad + ".tmp").exists())

    def test_render_all_order_and_consistency(self):
        tl = model.parse_timeline(demo_spec(frames=10))
        with tempfile.TemporaryDirectory() as tmp:
            assets.generate_demo_assets(tmp)
            loaded = {l.asset: assets.load_png(tmp, l.asset)
                      for l in tl.layers if l.asset != "missing.png"}
            seq1, seq4 = [], []
            render_all(tl, loaded, 1, lambda i, b: seq1.append((i, frame_sha256(b))))
            render_all(tl, loaded, 4, lambda i, b: seq4.append((i, frame_sha256(b))))
            self.assertEqual([i for i, _ in seq4], list(range(10)))
            self.assertEqual(seq1, seq4)

    def test_summary_writer_atomic(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "s.json"
            w = SummaryWriter(path)
            w.write({"a": 1})
            self.assertEqual(json.loads(path.read_text()), {"a": 1})
            self.assertFalse(Path(str(path) + ".tmp").exists())


if __name__ == "__main__":
    unittest.main()
