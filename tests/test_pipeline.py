from __future__ import annotations

import hashlib
import json
import tempfile
import unittest
from fractions import Fraction
from pathlib import Path

import numpy as np
from PIL import Image

from timeline_composer import cli
from timeline_composer.assets import AssetError, AssetStore
from timeline_composer.audio_sync import align_events, nearest_frame
from timeline_composer.clock import ClockError, FrameClock
from timeline_composer.color import (
    blend,
    encode_frame,
    linear_to_srgb8,
    srgb8_to_linear,
)
from timeline_composer.compositor import CompositorError, FrameCompositor
from timeline_composer.exporter import ExportError, export_summary, read_summary
from timeline_composer.fixtures import DEMO_LAYER_ASSETS, build_demo, write_demo_assets
from timeline_composer.schema import SchemaError, parse_timeline


class FrameClockTests(unittest.TestCase):
    def test_exact_rational_pts_has_no_float_drift(self) -> None:
        clock = FrameClock.from_rate("30000/1001", Fraction(120 * 1001, 30000))
        self.assertEqual(clock.frame_count, 120)
        self.assertEqual(clock.pts(0), Fraction(0))
        self.assertEqual(clock.pts(120 - 1), Fraction(119 * 1001, 30000))
        self.assertEqual(clock.frame_period, Fraction(1001, 30000))

    def test_unaligned_duration_rounds_frame_count_up(self) -> None:
        clock = FrameClock.from_rate(24, Fraction(1, 1))
        self.assertEqual(clock.frame_count, 24)

        clock = FrameClock.from_rate(24, Fraction(1, 48))
        self.assertEqual(clock.frame_count, 1)

        clock = FrameClock.from_rate(24, Fraction(0))
        self.assertEqual(clock.frame_count, 0)

    def test_half_open_coverage_and_lookup(self) -> None:
        clock = FrameClock.from_rate(10, 1)
        self.assertEqual(clock.frame_count, 10)
        self.assertEqual(clock.frame_index_at(Fraction(0)), 0)
        self.assertEqual(clock.frame_index_at(Fraction(1, 10)), 1)
        self.assertEqual(clock.frame_index_at(Fraction(99, 100)), 9)
        self.assertEqual(clock.frame_index_at(Fraction(-5)), 0)
        self.assertEqual(clock.frame_interval(3), (Fraction(3, 10), Fraction(4, 10)))

    def test_invalid_rate_and_duration_are_rejected(self) -> None:
        for fps in (0, -24, "nan", "1/0", "abc", True):
            with self.subTest(fps=fps), self.assertRaises(ClockError):
                FrameClock.from_rate(fps, 1)
        for duration in (-1, "inf", "x"):
            with self.subTest(duration=duration), self.assertRaises(ClockError):
                FrameClock.from_rate(24, duration)

    def test_out_of_range_index_is_rejected(self) -> None:
        clock = FrameClock.from_rate(24, 1)
        for index in (-1, clock.frame_count, True, 1.0):
            with self.subTest(index=index), self.assertRaises(ClockError):
                clock.pts(index)  # type: ignore[arg-type]


class SchemaTests(unittest.TestCase):
    def _minimal(self, **overrides):
        document = {
            "canvas": {"width": 320, "height": 180},
            "fps": "30000/1001",
            "frames": 120,
            "layers": [],
        }
        document.update(overrides)
        return document

    def test_frames_take_precedence_over_duration_and_are_exact(self) -> None:
        timeline = parse_timeline(self._minimal())
        self.assertEqual(timeline.frame_count, 120)
        self.assertEqual(timeline.duration, Fraction(120 * 1001, 30000))

    def test_duration_without_frames_ceils(self) -> None:
        timeline = parse_timeline({
            "canvas": {"width": 4, "height": 4},
            "fps": 24,
            "duration_seconds": "1/48",
        })
        self.assertEqual(timeline.frame_count, 1)

    def test_draw_order_is_stable_for_equal_ranks(self) -> None:
        timeline = parse_timeline(self._minimal(layers=[
            {"id": "b", "source": "b.png", "order": 1},
            {"id": "a", "source": "a.png", "order": 1},
            {"id": "top", "source": "t.png", "order": 5},
        ]))
        self.assertEqual([layer.id for layer in timeline.draw_order()], ["b", "a", "top"])

    def test_track_interpolation_and_clamping(self) -> None:
        timeline = parse_timeline(self._minimal(layers=[{
            "id": "a", "source": "a.png",
            "position": {"keyframes": [
                {"time": 0, "x": 0, "y": 10},
                {"time": 2, "x": 100, "y": 30},
            ]},
        }]))
        track = timeline.layers[0].position
        self.assertEqual(track.evaluate(Fraction(-1)), (Fraction(0), Fraction(10)))
        self.assertEqual(track.evaluate(Fraction(1)), (Fraction(50), Fraction(20)))
        self.assertEqual(track.evaluate(Fraction(9)), (Fraction(100), Fraction(30)))

    def test_layer_activity_is_half_open(self) -> None:
        timeline = parse_timeline(self._minimal(layers=[{
            "id": "a", "source": "a.png", "start": 0, "end": 1,
        }]))
        layer = timeline.layers[0]
        self.assertTrue(layer.is_active(Fraction(0)))
        self.assertFalse(layer.is_active(Fraction(1)))

    def test_rejections(self) -> None:
        bad_documents = [
            self._minimal(fps="abc"),
            self._minimal(fps=0),
            self._minimal(frames=100_001),
            self._minimal(canvas={"width": 5000, "height": 5000}),
            self._minimal(canvas={"width": 0, "height": 10}),
            self._minimal(layers=[{"id": "a", "source": "a.png", "blend": "overlay"}]),
            self._minimal(layers=[{"id": "a", "source": "/etc/a.png"}]),
            self._minimal(layers=[{"id": "a", "source": "../a.png"}]),
            self._minimal(layers=[{"id": "a", "source": "a.jpg"}]),
            self._minimal(layers=[{"id": "a", "source": "a.png"}, {"id": "a", "source": "b.png"}]),
            self._minimal(layers=[{"id": "a", "source": "a.png", "start": 2, "end": 1}]),
            self._minimal(layers=[{
                "id": "a", "source": "a.png",
                "opacity": {"keyframes": [{"time": 1, "value": 1}, {"time": 1, "value": 0}]},
            }]),
            self._minimal(audio_events=[{"sample": 0, "sample_rate": 0}]),
            self._minimal(layers=[{"id": "a", "source": "a.png", "position": 5}]),
            self._minimal(layers=[{
                "id": "a", "source": "a.png",
                "position": {"keyframes": [{"time": 0, "value": 1}]},
            }]),
            self._minimal(layers=[{
                "id": "a", "source": "a.png",
                "scale": {"keyframes": [{"time": 0, "x": 1, "y": 1}]},
            }]),
        ]
        for document in bad_documents:
            with self.subTest(document=document), self.assertRaises(SchemaError):
                parse_timeline(document)


if __name__ == "__main__":
    unittest.main()


def _solid_rgba(width: int, height: int, color: tuple[int, int, int, int]) -> np.ndarray:
    frame = np.empty((height, width, 4), dtype=np.uint8)
    frame[...] = color
    return frame


class ColorTests(unittest.TestCase):
    def test_srgb_linear_round_trip_is_exact_for_8_bit(self) -> None:
        codes = np.arange(256, dtype=np.uint8)
        linear = srgb8_to_linear(codes)
        self.assertEqual(linear_to_srgb8(linear).tolist(), codes.tolist())

    def test_midpoint_matches_reference_formula(self) -> None:
        expected = ((128 / 255 + 0.055) / 1.055) ** 2.4
        self.assertAlmostEqual(float(srgb8_to_linear(np.array([128]))[0]), expected, places=6)

    def test_normal_blend_is_source_over_in_linear_light(self) -> None:
        dst_rgb = np.zeros((1, 1, 3), dtype=np.float32)
        dst_a = np.zeros((1, 1), dtype=np.float32)
        red = srgb8_to_linear(np.array([[[255, 0, 0]]], dtype=np.uint8))
        out_rgb, out_a = blend(dst_rgb, dst_a, red, np.ones((1, 1), np.float32), "normal")
        np.testing.assert_allclose(out_rgb, red, rtol=0, atol=1e-6)
        np.testing.assert_allclose(out_a, 1.0, rtol=0, atol=1e-6)

    def test_transparent_source_leaves_destination_untouched(self) -> None:
        dst_rgb = np.array([[[0.2, 0.4, 0.6]]], dtype=np.float32)
        dst_a = np.ones((1, 1), dtype=np.float32)
        out_rgb, out_a = blend(
            dst_rgb, dst_a, np.zeros((1, 1, 3), np.float32), np.zeros((1, 1), np.float32), "screen"
        )
        np.testing.assert_allclose(out_rgb, dst_rgb, atol=1e-6)
        np.testing.assert_allclose(out_a, 1.0, atol=1e-6)

    def test_multiply_and_screen_against_straight_colours(self) -> None:
        dst = np.array([[[0.5, 0.5, 0.5]]], dtype=np.float32)
        dst_a = np.ones((1, 1), dtype=np.float32)
        src = np.array([[[0.5, 0.5, 0.5]]], dtype=np.float32)
        src_a = np.ones((1, 1), dtype=np.float32)
        mult, _ = blend(dst, dst_a, src, src_a, "multiply")
        screen, _ = blend(dst, dst_a, src, src_a, "screen")
        np.testing.assert_allclose(mult, 0.25, atol=1e-6)
        np.testing.assert_allclose(screen, 0.75, atol=1e-6)

    def test_unknown_mode_is_rejected(self) -> None:
        with self.assertRaises(ValueError):
            blend(np.zeros((1, 1, 3), np.float32), np.zeros((1, 1), np.float32),
                  np.zeros((1, 1, 3), np.float32), np.zeros((1, 1), np.float32), "overlay")

    def test_encode_rounding_is_half_up_and_transparent_zeroes(self) -> None:
        rgb = np.zeros((1, 2, 3), dtype=np.float32)
        alpha = np.array([[0.0, 1.0]], dtype=np.float32)
        rgb[0, 1] = srgb8_to_linear(np.array([255, 128, 0]))
        encoded = encode_frame(rgb, alpha)
        self.assertEqual(encoded[0, 0].tolist(), [0, 0, 0, 0])
        self.assertEqual(encoded[0, 1].tolist(), [255, 128, 0, 255])


class AssetTests(unittest.TestCase):
    def setUp(self) -> None:
        self._tmp = tempfile.TemporaryDirectory()
        self.root = Path(self._tmp.name) / "assets"
        self.root.mkdir()
        Image.fromarray(_solid_rgba(4, 3, (10, 20, 30, 255))).save(self.root / "ok.png")

    def tearDown(self) -> None:
        self._tmp.cleanup()

    def test_loads_and_caches_rgba(self) -> None:
        store = AssetStore(self.root, cache_size=1)
        first = store.load("ok.png")
        second = store.load("ok.png")
        self.assertIs(first, second)
        self.assertEqual(first.shape, (3, 4, 4))
        self.assertEqual(first[0, 0].tolist(), [10, 20, 30, 255])
        self.assertFalse(first.flags.writeable)

    def test_rejects_paths_outside_root(self) -> None:
        outside = Path(self._tmp.name) / "secret.png"
        Image.fromarray(_solid_rgba(1, 1, (0, 0, 0, 0))).save(outside)
        (self.root / "link.png").symlink_to(outside)
        store = AssetStore(self.root)
        for source in ("../secret.png", str(outside), "link.png", "a\\b.png", "ok.txt"):
            with self.subTest(source=source), self.assertRaises(AssetError):
                store.load(source)

    def test_corrupt_png_raises_asset_error(self) -> None:
        (self.root / "broken.png").write_bytes(b"not a png")
        with self.assertRaises(AssetError):
            AssetStore(self.root).load("broken.png")

    def test_missing_file_raises_asset_error(self) -> None:
        with self.assertRaises(AssetError):
            AssetStore(self.root).load("absent.png")

    def test_eviction_respects_bound(self) -> None:
        Image.fromarray(_solid_rgba(2, 2, (1, 2, 3, 255))).save(self.root / "second.png")
        store = AssetStore(self.root, cache_size=1)
        store.load("ok.png")
        store.load("second.png")
        self.assertEqual(len(store), 1)


class CompositorTests(unittest.TestCase):
    def setUp(self) -> None:
        self._tmp = tempfile.TemporaryDirectory()
        self.root = Path(self._tmp.name)
        Image.fromarray(_solid_rgba(8, 8, (255, 0, 0, 255))).save(self.root / "red.png")
        Image.fromarray(_solid_rgba(8, 8, (0, 0, 255, 255))).save(self.root / "blue.png")
        Image.fromarray(_solid_rgba(8, 8, (0, 0, 0, 0))).save(self.root / "clear.png")

    def tearDown(self) -> None:
        self._tmp.cleanup()

    def _compose(self, layers, width=16, height=16, frame=0):
        timeline = parse_timeline({
            "canvas": {"width": width, "height": height},
            "fps": 24, "frames": 2, "layers": layers,
        })
        return FrameCompositor(timeline, AssetStore(self.root)).compose(frame)

    def test_declaration_order_breaks_rank_ties(self) -> None:
        red_then_blue = self._compose([
            {"id": "r", "source": "red.png", "order": 0},
            {"id": "b", "source": "blue.png", "order": 0},
        ])
        blue_then_red = self._compose([
            {"id": "b", "source": "blue.png", "order": 0},
            {"id": "r", "source": "red.png", "order": 0},
        ])
        self.assertEqual(red_then_blue[0, 0].tolist(), [0, 0, 255, 255])
        self.assertEqual(blue_then_red[0, 0].tolist(), [255, 0, 0, 255])

    def test_higher_order_draws_on_top(self) -> None:
        frame = self._compose([
            {"id": "b", "source": "blue.png", "order": 9},
            {"id": "r", "source": "red.png", "order": 1},
        ])
        self.assertEqual(frame[0, 0].tolist(), [0, 0, 255, 255])

    def test_no_layers_yields_transparent_canvas(self) -> None:
        frame = self._compose([])
        self.assertTrue((frame == 0).all())

    def test_fully_transparent_layer_is_a_no_op(self) -> None:
        frame = self._compose([
            {"id": "r", "source": "red.png", "order": 0},
            {"id": "c", "source": "clear.png", "order": 1},
        ])
        self.assertEqual(frame[0, 0].tolist(), [255, 0, 0, 255])

    def test_zero_opacity_layer_is_a_no_op(self) -> None:
        frame = self._compose([
            {"id": "r", "source": "red.png", "order": 0},
            {"id": "b", "source": "blue.png", "order": 1, "opacity": 0},
        ])
        self.assertEqual(frame[0, 0].tolist(), [255, 0, 0, 255])

    def test_off_canvas_and_negative_positions_clip(self) -> None:
        partial = self._compose([{"id": "r", "source": "red.png", "position": {"x": -4, "y": 12}}])
        self.assertEqual(partial[12, 0].tolist(), [255, 0, 0, 255])   # visible strip
        self.assertEqual(partial[0, 0].tolist(), [0, 0, 0, 0])        # above the strip
        fully_out = self._compose([{"id": "r", "source": "red.png", "position": {"x": 99, "y": 0}}])
        self.assertTrue((fully_out == 0).all())

    def test_scale_changes_rendered_extent(self) -> None:
        half = self._compose([{"id": "r", "source": "red.png", "scale": {"value": "1/2"}}])
        self.assertEqual(half[0, 0].tolist(), [255, 0, 0, 255])
        self.assertEqual(half[5, 5].tolist(), [0, 0, 0, 0])

    def test_layer_window_boundary_is_half_open(self) -> None:
        layers = [{"id": "r", "source": "red.png", "start": "1/24", "end": "2/24"}]
        timeline = parse_timeline({
            "canvas": {"width": 4, "height": 4}, "fps": 24, "frames": 2, "layers": layers,
        })
        composer = FrameCompositor(timeline, AssetStore(self.root))
        self.assertTrue((composer.compose(0) == 0).all())
        self.assertEqual(composer.compose(1)[0, 0].tolist(), [255, 0, 0, 255])

    def test_missing_asset_surfaces_as_asset_error(self) -> None:
        timeline = parse_timeline({
            "canvas": {"width": 4, "height": 4}, "fps": 24, "frames": 1,
            "layers": [{"id": "x", "source": "nope.png"}],
        })
        with self.assertRaises(AssetError):
            FrameCompositor(timeline, AssetStore(self.root)).compose(0)


class AudioSyncTests(unittest.TestCase):
    def test_nearest_frame_and_tie_goes_earlier(self) -> None:
        clock = FrameClock.from_rate(10, 1)  # frame period 0.1 s
        self.assertEqual(nearest_frame(clock, Fraction(0)), 0)
        self.assertEqual(nearest_frame(clock, Fraction(1, 20)), 0)   # exact tie -> earlier
        self.assertEqual(nearest_frame(clock, Fraction(3, 20)), 1)
        self.assertEqual(nearest_frame(clock, Fraction(99, 1000)), 1)
        self.assertEqual(nearest_frame(clock, Fraction(-1)), 0)
        self.assertEqual(nearest_frame(clock, Fraction(5)), 9)       # clamp to last frame

    def test_signed_error_is_positive_when_cue_is_late(self) -> None:
        clock = FrameClock.from_rate(10, 1)
        events = parse_timeline({
            "canvas": {"width": 2, "height": 2}, "fps": 10, "frames": 10,
            "audio_events": [
                {"label": "late", "sample": 105, "sample_rate": 1000},   # 105 ms -> frame 1 (100 ms)
                {"label": "early", "sample": 95, "sample_rate": 1000},   # 95 ms -> frame 1 (100 ms)
            ],
        }).audio_events
        summary = align_events(clock, events)
        self.assertEqual([a.frame_index for a in summary.alignments], [1, 1])
        self.assertAlmostEqual(summary.alignments[0].error_ms, 5.0, places=9)
        self.assertAlmostEqual(summary.alignments[1].error_ms, -5.0, places=9)
        self.assertAlmostEqual(summary.max_abs_error_ms, 5.0, places=9)

    def test_demo_max_error_matches_expected_contract(self) -> None:
        with tempfile.TemporaryDirectory() as workspace:
            timeline, _ = build_demo(workspace, 120, "30000/1001", 320, 180)
        summary = align_events(timeline.clock, timeline.audio_events)
        self.assertEqual(summary.event_count, 3)
        self.assertAlmostEqual(summary.max_abs_error_ms, 6.008333333, places=6)


class ExporterTests(unittest.TestCase):
    def setUp(self) -> None:
        self._tmp = tempfile.TemporaryDirectory()
        self.root = Path(self._tmp.name)
        for name, color in (
            ("red.png", (255, 0, 0, 255)),
            ("half.png", (0, 255, 0, 128)),
        ):
            Image.fromarray(_solid_rgba(8, 8, color)).save(self.root / name)

    def tearDown(self) -> None:
        self._tmp.cleanup()

    def _timeline(self, frames=6, layers=None):
        document = {
            "canvas": {"width": 12, "height": 12}, "fps": 24, "frames": frames,
            "layers": layers if layers is not None else [
                {"id": "r", "source": "red.png", "order": 0},
                {"id": "h", "source": "half.png", "order": 1, "blend": "screen"},
            ],
            "audio_events": [{"label": "c", "sample": 10, "sample_rate": 1000}],
        }
        return parse_timeline(document)

    def _composer(self, timeline):
        return FrameCompositor(timeline, AssetStore(self.root))

    def test_summary_has_header_and_one_row_per_frame_in_order(self) -> None:
        target = self.root / "out" / "summary.jsonl"
        timeline = self._timeline()
        result = export_summary(self._composer(timeline), target, workers=3)
        header, rows = read_summary(target)
        self.assertEqual(header["frame_count"], result.frame_count)
        self.assertEqual([row["frame"] for row in rows], list(range(6)))
        self.assertEqual([row["pts"] for row in rows], [
            f"{Fraction(i, 24).numerator}/{Fraction(i, 24).denominator}" for i in range(6)
        ])
        self.assertTrue(all(len(row["pixel_sha256"]) == 64 for row in rows))

    def test_parallel_and_serial_outputs_are_identical(self) -> None:
        timeline = self._timeline()
        serial = self.root / "serial.jsonl"
        parallel = self.root / "parallel.jsonl"
        export_summary(self._composer(timeline), serial, workers=1)
        export_summary(self._composer(timeline), parallel, workers=4)
        self.assertEqual(serial.read_bytes(), parallel.read_bytes())

    def test_digest_matches_direct_hash_of_composed_frame(self) -> None:
        timeline = self._timeline(frames=2)
        composer = self._composer(timeline)
        pixels = composer.compose(0)
        expected = hashlib.sha256(pixels.tobytes()).hexdigest()
        target = self.root / "one.jsonl"
        export_summary(composer, target, workers=1)
        _, rows = read_summary(target)
        self.assertEqual(rows[0]["pixel_sha256"], expected)

    def test_worker_failure_leaves_no_destination_file(self) -> None:
        timeline = self._timeline(frames=8, layers=[{"id": "r", "source": "red.png"}])
        composer = self._composer(timeline)
        target = self.root / "broken.jsonl"
        original = composer.compose
        calls = {"n": 0}

        def flaky(index):
            calls["n"] += 1
            if index == 3:
                raise CompositorError("boom at frame 3")
            return original(index)

        composer.compose = flaky  # type: ignore[method-assign]
        with self.assertRaises(CompositorError):
            export_summary(composer, target, workers=2)
        self.assertFalse(target.exists())
        self.assertEqual(list(self.root.glob("*.tmp")), [])
        self.assertEqual(list(self.root.glob(".*.tmp")), [])

    def test_existing_destination_survives_failed_export(self) -> None:
        timeline = self._timeline(frames=4, layers=[{"id": "r", "source": "red.png"}])
        target = self.root / "kept.jsonl"
        target.write_text("sentinel\n", encoding="utf-8")
        composer = self._composer(timeline)
        composer.compose = lambda index: (_ for _ in ()).throw(CompositorError("nope"))  # type: ignore[method-assign]
        with self.assertRaises(CompositorError):
            export_summary(composer, target, workers=2)
        self.assertEqual(target.read_text(encoding="utf-8"), "sentinel\n")

    def test_empty_timeline_writes_header_only_summary(self) -> None:
        timeline = self._timeline(frames=0)
        target = self.root / "zero.jsonl"
        result = export_summary(self._composer(timeline), target, workers=2)
        header, rows = read_summary(target)
        self.assertEqual(result.frame_count, 0)
        self.assertEqual(rows, [])
        self.assertEqual(header["frame_count"], 0)

    def test_read_summary_rejects_malformed_input(self) -> None:
        bad = self.root / "bad.jsonl"
        bad.write_text("not json\n", encoding="utf-8")
        with self.assertRaises(ExportError):
            read_summary(bad)
        with self.assertRaises(ExportError):
            read_summary(self.root / "missing.jsonl")


class CliEndToEndTests(unittest.TestCase):
    def setUp(self) -> None:
        self._tmp = tempfile.TemporaryDirectory()
        self.root = Path(self._tmp.name)
        self.summary = self.root / "summary.jsonl"

    def tearDown(self) -> None:
        self._tmp.cleanup()

    def _demo(self, *extra: str) -> int:
        return cli.main([
            "demo", "--fps", "30000/1001", "--frames", "120", "--canvas", "64x36",
            "--workers", "3", "--summary", str(self.summary), *extra,
        ])

    def test_demo_then_verify_roundtrip(self) -> None:
        with self.assertRedirectsOutput():
            self.assertEqual(self._demo(), 0)
        header, rows = read_summary(self.summary)
        self.assertEqual(header["frame_count"], 120)
        self.assertEqual(header["audio_events"], 3)
        self.assertAlmostEqual(header["av_max_abs_error_ms"], 6.008333333, places=6)
        self.assertEqual(len(rows), 120)
        self.assertTrue(all(len(row["pixel_sha256"]) == 64 for row in rows))
        with self.assertRedirectsOutput():
            code = cli.main([
                "verify", "--demo", "--fps", "30000/1001", "--frames", "120",
                "--canvas", "64x36", "--workers", "2", "--summary", str(self.summary),
            ])
        self.assertEqual(code, 0)

    def test_verify_detects_tampering(self) -> None:
        with self.assertRedirectsOutput():
            self.assertEqual(self._demo(), 0)
        lines = self.summary.read_text(encoding="utf-8").splitlines()
        record = json.loads(lines[5])
        record["pixel_sha256"] = "0" * 64
        lines[5] = json.dumps(record, sort_keys=True, separators=(",", ":"))
        self.summary.write_text("\n".join(lines) + "\n", encoding="utf-8")
        with self.assertRedirectsOutput():
            code = cli.main([
                "verify", "--demo", "--frames", "120", "--canvas", "64x36",
                "--summary", str(self.summary),
            ])
        self.assertEqual(code, 4)

    def test_argument_errors_exit_two(self) -> None:
        cases = [
            ["demo", "--fps", "nope", "--summary", str(self.summary)],
            ["demo", "--workers", "0", "--summary", str(self.summary)],
            ["demo", "--canvas", "oops", "--summary", str(self.summary)],
            ["demo", "--frames", "100001", "--summary", str(self.summary)],
            ["verify", "--summary", str(self.summary)],  # missing --demo
        ]
        for argv in cases:
            with self.subTest(argv=argv), self.assertRedirectsOutput():
                self.assertEqual(cli.main(argv), 2)
        self.assertFalse(self.summary.exists())

    def test_missing_summary_exits_four(self) -> None:
        with self.assertRedirectsOutput():
            code = cli.main(["verify", "--demo", "--summary", str(self.root / "absent.jsonl")])
        self.assertEqual(code, 4)

    def test_bad_asset_root_exits_three(self) -> None:
        timeline_path = self.root / "timeline.json"
        timeline_path.write_text(json.dumps({
            "canvas": {"width": 4, "height": 4}, "fps": 24, "frames": 2,
            "layers": [{"id": "x", "source": "missing.png"}],
        }), encoding="utf-8")
        (self.root / "assets").mkdir()
        with self.assertRedirectsOutput():
            code = cli.main([
                "compose", "--timeline", str(timeline_path),
                "--assets", str(self.root / "assets"), "--summary", str(self.summary),
            ])
        self.assertEqual(code, 3)
        self.assertFalse(self.summary.exists())

    def test_compose_from_explicit_timeline(self) -> None:
        assets = self.root / "assets"
        assets.mkdir()
        Image.fromarray(_solid_rgba(4, 4, (0, 128, 255, 200))).save(assets / "sq.png")
        timeline_path = self.root / "timeline.json"
        timeline_path.write_text(json.dumps({
            "canvas": {"width": 8, "height": 8}, "fps": "24000/1001", "frames": 10,
            "layers": [{"id": "sq", "source": "sq.png", "scale": {"value": "1/2"}}],
            "audio_events": [{"label": "hit", "sample": 1001, "sample_rate": 24000}],
        }), encoding="utf-8")
        with self.assertRedirectsOutput():
            code = cli.main([
                "compose", "--timeline", str(timeline_path), "--assets", str(assets),
                "--workers", "2", "--summary", str(self.summary),
            ])
        self.assertEqual(code, 0)
        header, rows = read_summary(self.summary)
        self.assertEqual(header["frame_count"], 10)
        self.assertEqual(header["fps"], "24000/1001")
        self.assertEqual(rows[1]["audio_errors_ms"], [0.0])

    def assertRedirectsOutput(self):
        import contextlib
        import io

        @contextlib.contextmanager
        def _redirect():
            with contextlib.redirect_stdout(io.StringIO()), contextlib.redirect_stderr(io.StringIO()):
                yield

        return _redirect()


class LimitAndDeterminismTests(unittest.TestCase):
    def test_canvas_pixel_limit_rejects_just_over_sixteen_megapixels(self) -> None:
        document = {
            "canvas": {"width": 4001, "height": 4000}, "fps": 24, "frames": 1,
            "layers": [],
        }
        self.assertGreater(4001 * 4000, 16_000_000)
        with self.assertRaises(SchemaError):
            parse_timeline(document)
        # At the boundary it is accepted.
        parse_timeline({
            "canvas": {"width": 4000, "height": 4000}, "fps": 24, "frames": 1,
            "layers": [],
        })

    def test_frame_limit_boundary(self) -> None:
        document = {
            "canvas": {"width": 2, "height": 2}, "fps": 24, "frames": 100_000,
            "layers": [],
        }
        self.assertEqual(parse_timeline(document).frame_count, 100_000)
        document["frames"] = 100_001
        with self.assertRaises(SchemaError):
            parse_timeline(document)

    def test_fixtures_are_byte_deterministic(self) -> None:
        with tempfile.TemporaryDirectory() as first, tempfile.TemporaryDirectory() as second:
            root_a = write_demo_assets(first)
            root_b = write_demo_assets(second)
            for name, _ in DEMO_LAYER_ASSETS:
                self.assertEqual(
                    (root_a / name).read_bytes(), (root_b / name).read_bytes(), name
                )

    def test_parallel_render_is_stable_across_runs(self) -> None:
        with tempfile.TemporaryDirectory() as workspace:
            timeline, root = build_demo(workspace, 40, "30000/1001", 96, 54)
            store = AssetStore(root, cache_size=1)  # forces eviction churn
            composer = FrameCompositor(timeline, store)
            out_a = Path(workspace) / "a.jsonl"
            out_b = Path(workspace) / "b.jsonl"
            export_summary(composer, out_a, workers=8)
            store.clear()
            export_summary(composer, out_b, workers=8)
            self.assertEqual(out_a.read_bytes(), out_b.read_bytes())

    def test_compose_writes_nothing_but_the_summary(self) -> None:
        with tempfile.TemporaryDirectory() as workspace:
            timeline, root = build_demo(workspace, 6, 24, 32, 18)
            assets_before = sorted(p.name for p in root.iterdir())
            out_dir = Path(workspace) / "out"
            export_summary(FrameCompositor(timeline, AssetStore(root)), out_dir / "s.jsonl")
            self.assertEqual(
                sorted(p.name for p in root.iterdir() if p.is_file()), assets_before
            )
            self.assertEqual([p.name for p in out_dir.iterdir()], ["s.jsonl"])
