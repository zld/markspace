"""Unit tests for plan normalisation, task keys and guard rails."""

from __future__ import annotations

import unittest

from ladderforge.errors import ConfigError
from ladderforge.plan import (
    JobSpec,
    Resolution,
    build_plan,
    materialise_jobs,
    normalise_bitrates,
    parse_resolution,
    source_id,
    task_key,
)


class ResolutionTests(unittest.TestCase):
    def test_parses_label(self) -> None:
        res = parse_resolution("640x360")
        self.assertEqual(res, Resolution(640, 360))
        self.assertEqual(res.label, "640x360")

    def test_rejects_odd_dimensions(self) -> None:
        with self.assertRaises(ConfigError):
            parse_resolution("641x360")

    def test_rejects_oversized(self) -> None:
        with self.assertRaises(ConfigError):
            parse_resolution("4096x2160")

    def test_rejects_garbage(self) -> None:
        for raw in ("640", "axb", "0x240", "-4x240"):
            with self.subTest(raw=raw), self.assertRaises(ConfigError):
                parse_resolution(raw)


class BitrateTests(unittest.TestCase):
    def test_collapses_duplicates_into_aliases(self) -> None:
        targets, duplicates = normalise_bitrates("250,500,1000,1000")
        self.assertEqual([t.bitrate_kbps for t in targets], [250, 500, 1000])
        self.assertEqual([t.aliases for t in targets], [0, 0, 1])
        self.assertEqual(duplicates, 1)

    def test_counts_multiple_aliases(self) -> None:
        targets, duplicates = normalise_bitrates("500,500,500")
        self.assertEqual(targets[0].aliases, 2)
        self.assertEqual(duplicates, 2)

    def test_rejects_out_of_range(self) -> None:
        with self.assertRaises(ConfigError):
            normalise_bitrates("0")
        with self.assertRaises(ConfigError):
            normalise_bitrates("200000")


class PlanTests(unittest.TestCase):
    def test_demo_plan_shape(self) -> None:
        plan = build_plan(
            frames=48,
            resolutions="426x240,640x360",
            bitrates="250,500,1000,1000",
            presets="ultrafast,medium",
            workers=2,
        )
        # 2 resolutions x 2 presets x 3 unique targets
        self.assertEqual(len(plan.jobs), 12)
        self.assertEqual(plan.duplicate_targets, 1)
        self.assertEqual(len(plan.targets), 3)

    def test_preset_dedupe_preserves_order(self) -> None:
        plan = build_plan(
            frames=8,
            resolutions="320x180",
            bitrates="250",
            presets="medium,medium,ultrafast",
            workers=1,
        )
        self.assertEqual(plan.presets, ("medium", "ultrafast"))
        self.assertEqual(len(plan.jobs), 2)

    def test_unknown_preset_rejected(self) -> None:
        with self.assertRaises(ConfigError):
            build_plan(
                frames=8,
                resolutions="320x180",
                bitrates="250",
                presets="turbo",
            )

    def test_frame_limit_enforced(self) -> None:
        with self.assertRaises(ConfigError):
            build_plan(frames=100_000, resolutions="320x180", bitrates="250", presets="fast")

    def test_combination_limit_enforced(self) -> None:
        # 6 resolutions x 8 bitrates x 6 presets = 288 > 256
        with self.assertRaises(ConfigError):
            build_plan(
                frames=8,
                resolutions="320x180,426x240,640x360,854x480,1280x720,1600x900",
                bitrates="100,150,200,250,300,400,500,600",
                presets="ultrafast,superfast,veryfast,fast,medium,slow",
            )


class TaskKeyTests(unittest.TestCase):
    def setUp(self) -> None:
        self.res = Resolution(426, 240)
        self.job = JobSpec(self.res, 500, "medium")
        self.hash = source_id(self.res, 24, 24, 7)

    def key(self, **overrides) -> str:
        kwargs = dict(
            source_sha256=self.hash,
            job=self.job,
            ffmpeg_version="9.0.2",
            frames=24,
            fps=24,
        )
        kwargs.update(overrides)
        return task_key(**kwargs)

    def test_stable_for_same_inputs(self) -> None:
        self.assertEqual(self.key(), self.key())

    def test_changes_with_source_hash(self) -> None:
        self.assertNotEqual(self.key(), self.key(source_sha256="deadbeef"))

    def test_changes_with_source_geometry(self) -> None:
        other = source_id(Resolution(640, 360), 24, 24, 7)
        self.assertNotEqual(self.hash, other)

    def test_changes_with_ffmpeg_version(self) -> None:
        self.assertNotEqual(self.key(), self.key(ffmpeg_version="9.1.0"))

    def test_changes_with_parameters(self) -> None:
        other = JobSpec(self.res, 1000, "medium")
        self.assertNotEqual(self.key(), self.key(job=other))

    def test_materialise_matches_plan_order(self) -> None:
        plan = build_plan(
            frames=8,
            resolutions="320x180",
            bitrates="250,500",
            presets="fast",
        )
        hashes = {Resolution(320, 180): "abc"}
        planned = materialise_jobs(plan, hashes, "9.0.2")
        self.assertEqual(len(planned), 2)
        self.assertNotEqual(planned[0].key, planned[1].key)
        self.assertEqual(planned[0].frames, 8)
        self.assertEqual(len(planned[0].insert_row()), 11)


if __name__ == "__main__":
    unittest.main()
