from __future__ import annotations

import unittest
from timeline_composer.clock import ClockError, FrameClock
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


if __name__ == "__main__":
    unittest.main()
