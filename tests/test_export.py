"""Tests for frame timing, checksum export, thread determinism and failure paths."""

from __future__ import annotations

import json
import tempfile
import unittest
from fractions import Fraction
from pathlib import Path

from shotline.assets import write_png
from shotline.cli import build_demo_document, main
from shotline.compositor import preload_assets
from shotline.exporter import ExportError, export_frames
from shotline.model import TimelineError, parse_timeline
from shotline.timebase import align_cues, build_clock, summarise_av


def demo_timeline(seed=17, **over):
    doc = build_demo_document(320, 180, Fraction(24, 1), 2.0, seed)
    doc.update(over)
    return parse_timeline(doc, base_dir=Path(tempfile.mkdtemp()))


def loader_for(seed=17):
    def load(layer):
        from shotline.assets import load_asset
        return load_asset(layer.source, demo_timeline(seed), seed=seed)
    return load


class ClockTests(unittest.TestCase):
    def test_ceiling_frame_count_and_last_frame_within_duration(self):
        tl = parse_timeline(
            {"width": 8, "height": 8, "fps": 24, "duration": 2.0,
             "layers": [{"id": "a", "source": "demo:badge", "z": 0,
                         "keyframes": [{"t": 0.0}]}]},
            base_dir=Path(tempfile.mkdtemp()),
        )
        clock = build_clock(tl)
        self.assertEqual(clock.count, 48)
        self.assertLess(clock.pts_seconds(47), 2.0)
        self.assertEqual(clock.pts_seconds(47), Fraction(47, 24))

    def test_partial_last_frame_rounds_up(self):
        tl = parse_timeline(
            {"width": 8, "height": 8, "fps": 10, "duration": 0.25,
             "layers": [{"id": "a", "source": "demo:badge", "z": 0,
                         "keyframes": [{"t": 0.0}]}]},
            base_dir=Path(tempfile.mkdtemp()),
        )
        self.assertEqual(build_clock(tl).count, 3)   # ceil(2.5)

    def test_rational_fps_pts_is_exact(self):
        tl = parse_timeline(
            {"width": 8, "height": 8, "fps": {"numerator": 24000, "denominator": 1001},
             "duration": 1.0,
             "layers": [{"id": "a", "source": "demo:badge", "z": 0,
                         "keyframes": [{"t": 0.0}]}]},
            base_dir=Path(tempfile.mkdtemp()),
        )
        clock = build_clock(tl)
        self.assertEqual(clock.pts_us(1), int(Fraction(1001, 24000) * 1_000_000))


class CueTests(unittest.TestCase):
    def test_aligned_cues_have_zero_error(self):
        tl = demo_timeline()
        clock = build_clock(tl)
        alignment = align_cues(tl, clock)
        self.assertEqual(len(alignment), 2)
        self.assertEqual([a.sample_index for a in alignment], [24000, 72000])
        summary = summarise_av(alignment)
        self.assertEqual(summary["max_abs_av_error_ms"], 0.0)
        self.assertEqual(summary["mean_abs_av_error_ms"], 0.0)
        self.assertEqual(alignment[0].frame_index, 12)       # 0.5s at 24fps
        self.assertEqual(alignment[1].frame_index, 36)       # 1.5s at 24fps

    def test_misaligned_cue_error_is_measured(self):
        doc = build_demo_document(320, 180, Fraction(24, 1), 2.0, 17)
        doc["cues"] = [{"sample_rate": 48000, "sample_index": 1000}]  # ~0.020833s
        tl = parse_timeline(doc, base_dir=Path(tempfile.mkdtemp()))
        alignment = align_cues(tl, build_clock(tl))
        self.assertAlmostEqual(alignment[0].abs_error_ms,
                               abs(1000 / 48000 - 0.0) * 1000, places=6)

    def test_cue_beyond_duration_rejected(self):
        with self.assertRaises(TimelineError):
            demo_timeline(cues=[{"sample_rate": 48000, "sample_index": 96001}])


class ExportTests(unittest.TestCase):
    def test_export_format_and_summary(self):
        tl = demo_timeline()
        assets = preload_assets(tl, loader_for())
        out = Path(tempfile.mkdtemp()) / "frames.jsonl"
        records, summary = export_frames(tl, assets, build_clock(tl), out, workers=1)
        lines = out.read_text(encoding="utf-8").splitlines()
        self.assertEqual(len(lines), 49)                        # 48 frames + summary
        for i, line in enumerate(lines[:-1]):
            rec = json.loads(line)
            self.assertEqual(rec["type"], "frame")
            self.assertEqual(rec["index"], i)
            self.assertIsInstance(rec["pts_us"], int)
            self.assertRegex(rec["pixel_sha256"], r"^[0-9a-f]{64}$")
        last = json.loads(lines[-1])
        self.assertEqual(last["type"], "summary")
        self.assertEqual(last["frame_count"], 48)
        self.assertEqual(last["fps"], "24/1")
        self.assertEqual(last["cue_count"], 2)
        self.assertEqual(last["max_abs_av_error_ms"], 0.0)
        self.assertEqual(last["mean_abs_av_error_ms"], 0.0)
        self.assertEqual(len(records), 48)

    def test_worker_counts_produce_identical_output(self):
        tl = demo_timeline()
        assets = preload_assets(tl, loader_for())
        outputs = []
        for w in (1, 2, 4, 8):
            out = Path(tempfile.mkdtemp()) / f"w{w}.jsonl"
            export_frames(tl, assets, build_clock(tl), out, workers=w)
            outputs.append(out.read_text(encoding="utf-8"))
        self.assertEqual(len(set(outputs)), 1)
        self.assertRaises(ExportError, export_frames, tl, assets, build_clock(tl),
                          Path(tempfile.mkdtemp()) / "bad.jsonl", 9)

    def test_bad_asset_aborts_and_cleans_temp_files(self):
        base = Path(tempfile.mkdtemp())
        png = base / "broken.png"
        write_png(png, __import__("numpy").zeros((4, 4, 4), dtype="uint8"))
        doc = build_demo_document(320, 180, Fraction(24, 1), 2.0, 17)
        doc["layers"][0]["source"] = "broken.png"
        tl = parse_timeline(doc, base_dir=base)

        class BoomLoader:
            def __call__(self, layer):
                from shotline.assets import load_asset
                return load_asset(layer.source, tl)

        out = base / "out.jsonl"
        assets = preload_assets(tl, BoomLoader())
        # Corrupt rendering by removing the asset's required shape mid-flight.
        assets[tl.layers[0].id] = __import__("numpy").zeros((2, 2, 3), dtype="uint8")
        with self.assertRaises(Exception):
            export_frames(tl, assets, build_clock(tl), out, workers=4)
        self.assertFalse(out.exists())
        self.assertFalse((base / "out.jsonl.tmp").exists())

    def test_demo_cli_end_to_end_and_exit_codes(self):
        out = Path(tempfile.mkdtemp()) / "cli.jsonl"
        code = main(["demo", "--width", "320", "--height", "180", "--fps", "24",
                     "--duration", "2", "--seed", "17", "--workers", "4",
                     "--output", str(out)])
        self.assertEqual(code, 0)
        self.assertEqual(len(out.read_text(encoding="utf-8").splitlines()), 49)
        code = main(["demo", "--width", "5000", "--height", "180", "--fps", "24",
                     "--duration", "2", "--output", str(out)])
        self.assertEqual(code, 2)                           # invalid argument
        code = main(["demo", "--width", "8", "--height", "8", "--fps", "24",
                     "--duration", "2", "--workers", "16",
                     "--output", str(out)])
        self.assertEqual(code, 2)                           # workers out of range

    def test_run_command_with_local_png(self):
        import numpy as np
        from shotline.assets import load_asset
        base = Path(tempfile.mkdtemp())
        write_png(base / "card.png", np.full((12, 12, 4), 200, dtype="uint8"))
        tl_path = base / "timeline.json"
        tl_path.write_text(json.dumps({
            "width": 16, "height": 16, "fps": 12, "duration": 0.5,
            "layers": [{"id": "card", "source": "card.png", "z": 0,
                        "keyframes": [{"t": 0.0, "x": 0, "y": 0,
                                       "scale": 1.0, "opacity": 1.0}]}],
        }), encoding="utf-8")
        out = base / "out.jsonl"
        code = main(["run", "--timeline", str(tl_path), "--workers", "2",
                     "--output", str(out)])
        self.assertEqual(code, 0)
        self.assertEqual(len(out.read_text(encoding="utf-8").splitlines()), 7)  # 6 + summary
        # Path traversal must be rejected with the runtime exit code.
        bad = base / "bad.json"
        bad.write_text(json.dumps({
            "width": 16, "height": 16, "fps": 12, "duration": 0.5,
            "layers": [{"id": "card", "source": "../secret.png", "z": 0,
                        "keyframes": [{"t": 0.0}]}],
        }), encoding="utf-8")
        code = main(["run", "--timeline", str(bad),
                     "--output", str(base / "x.jsonl")])
        self.assertEqual(code, 3)


if __name__ == "__main__":
    unittest.main()
