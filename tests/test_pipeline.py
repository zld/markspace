from __future__ import annotations

import unittest
import tempfile
from pathlib import Path
from PIL import Image
import numpy as np
from timeline_composer.assets import AssetError, AssetStore
from timeline_composer.audio_sync import align_events, nearest_frame
from timeline_composer.clock import ClockError, FrameClock
from timeline_composer.compositor import CompositorError, FrameCompositor
from timeline_composer.schema import SchemaError, parse_timeline
from fractions import Fraction


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


if __name__ == "__main__":
    unittest.main()
