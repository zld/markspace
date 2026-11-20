from __future__ import annotations

import unittest
from timeline_composer.clock import ClockError, FrameClock
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


if __name__ == "__main__":
    unittest.main()
