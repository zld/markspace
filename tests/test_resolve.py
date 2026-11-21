"""Tests for the model and the conflict resolver."""

from __future__ import annotations

import unittest
from fractions import Fraction

from cutline.model import Clip, Gap, MediaRef, ModelError, Timeline, Track, validate
from cutline.resolve import find_conflicts, resolve


def make_clip(clip_id, start, duration, priority=0, media_duration=10_000):
    return Clip(
        clip_id=clip_id,
        name=clip_id,
        start=Fraction(start),
        duration=Fraction(duration),
        source_in=Fraction(0),
        source_out=Fraction(duration),
        media=MediaRef(name=f"{clip_id}.mov", rate=Fraction(24),
                       duration=Fraction(media_duration)),
        priority=priority,
    )


def timeline_with(items, mode="exclusive", rate=24):
    return Timeline(name="t", rate=Fraction(rate),
                    tracks=[Track(name="v", kind="video", mode=mode, items=items)])


class TestModel(unittest.TestCase):
    def test_negative_duration_rejected(self):
        tl = timeline_with([Gap(start=Fraction(0), duration=Fraction(-1))])
        with self.assertRaises(ModelError):
            validate(tl)

    def test_source_out_of_media_range_rejected(self):
        tl = timeline_with([make_clip("c", 0, 100, media_duration=50)])
        with self.assertRaisesRegex(ModelError, "exceeds"):
            validate(tl)

    def test_duplicate_clip_id_rejected(self):
        tl = timeline_with([make_clip("c", 0, 10), make_clip("c", 20, 10)])
        with self.assertRaisesRegex(ModelError, "duplicate"):
            validate(tl)

    def test_clip_limit_enforced(self):
        tl = timeline_with([make_clip(f"c{i}", i * 10, 10) for i in range(10_001)])
        with self.assertRaisesRegex(ModelError, "exceeds limit"):
            validate(tl)

    def test_zero_duration_clip_is_valid(self):
        tl = timeline_with([make_clip("z", 5, 0)])
        self.assertIs(validate(tl), tl)

    def test_negative_start_allowed(self):
        clip = make_clip("neg", -50, 100)
        tl = timeline_with([clip])
        validate(tl)
        self.assertEqual(tl.tracks[0].clips()[0].start, Fraction(-50))


class TestConflicts(unittest.TestCase):
    def test_overlap_chain_reports_pairs(self):
        tl = timeline_with([make_clip("a", 0, 100),
                            make_clip("b", 70, 70),
                            make_clip("c", 120, 40)])
        conflicts = find_conflicts(tl)
        self.assertEqual([(c.clip_a, c.clip_b) for c in conflicts],
                         [("a", "b"), ("b", "c")])

    def test_abutting_clips_are_not_conflicts(self):
        tl = timeline_with([make_clip("a", 0, 100), make_clip("b", 100, 50)])
        self.assertEqual(find_conflicts(tl), [])

    def test_layered_track_overlap_allowed(self):
        tl = timeline_with([make_clip("a", 0, 120), make_clip("b", 60, 120)],
                           mode="layered")
        self.assertEqual(find_conflicts(tl), [])

    def test_fractional_overlap_detected(self):
        tl = timeline_with([make_clip("a", 0, 100),
                            make_clip("b", Fraction(199, 2), 10)])
        conflicts = find_conflicts(tl)
        self.assertEqual(len(conflicts), 1)
        self.assertEqual(conflicts[0].overlap_end, Fraction(100))


class TestResolve(unittest.TestCase):
    def test_chain_shifts_right_preserving_durations(self):
        tl = timeline_with([make_clip("a", 0, 100),
                            make_clip("b", 70, 70),
                            make_clip("c", 120, 40)])
        result = resolve(tl)
        clips = {c.clip_id: c for c in result.timeline.tracks[0].clips()}
        self.assertEqual((clips["a"].start, clips["a"].end), (0, 100))
        self.assertEqual((clips["b"].start, clips["b"].end), (100, 170))
        self.assertEqual((clips["c"].start, clips["c"].end), (170, 210))
        self.assertEqual(result.duration_delta_seconds, Fraction(50, 24))

    def test_source_range_preserved_after_move(self):
        tl = timeline_with([make_clip("a", 0, 100), make_clip("b", 50, 60)])
        result = resolve(tl)
        b = result.timeline.tracks[0].clips()[1]
        self.assertEqual((b.source_in, b.source_out), (Fraction(0), Fraction(60)))

    def test_priority_breaks_start_ties(self):
        tl = timeline_with([make_clip("lo", 0, 100, priority=1),
                            make_clip("hi", 0, 100, priority=9)])
        result = resolve(tl)
        order = [c.clip_id for c in result.timeline.tracks[0].clips()]
        self.assertEqual(order, ["hi", "lo"])

    def test_idempotent(self):
        tl = timeline_with([make_clip("a", 0, 100),
                            make_clip("b", 70, 70),
                            make_clip("c", 120, 40)])
        once = resolve(tl).timeline
        twice = resolve(once)
        self.assertEqual(twice.moves, [])
        self.assertEqual(find_conflicts(once), [])

    def test_gaps_kept_and_sorted(self):
        tl = timeline_with([Gap(Fraction(200), Fraction(50)),
                            make_clip("a", 0, 100)])
        result = resolve(tl)
        items = result.timeline.tracks[0].items
        self.assertIsInstance(items[0], Clip)
        self.assertIsInstance(items[1], Gap)


if __name__ == "__main__":
    unittest.main()
