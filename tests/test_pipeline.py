"""End-to-end pipeline tests: synth -> scan -> measure -> ladder -> report."""

from __future__ import annotations

import contextlib
import io
import json
import tempfile
import threading
import time
import unittest
from pathlib import Path

from ladderforge.__main__ import main
from ladderforge.encode import EncoderInfo, detect_encoder
from ladderforge.errors import EXIT_FAILURE, EXIT_INTERRUPTED, EXIT_OK
from ladderforge.ladder import Candidate, build_ladder
from ladderforge.measure import compute_psnr, measure, probe
from ladderforge.plan import Resolution, build_plan, materialise_jobs, source_id
from ladderforge.scheduler import Control, Scheduler
from ladderforge.store import Store
from ladderforge.synth import generate_clip, read_reference_luma

SMALL = "320x180"
FRAMES = 12


def encoder_or_skip(testcase: unittest.TestCase) -> EncoderInfo:
    try:
        return detect_encoder()
    except Exception as exc:  # pragma: no cover - depends on host tooling
        testcase.skipTest(f"FFmpeg/libx264 unavailable: {exc}")


def run_cli(args: list[str]) -> tuple[int, str]:
    buffer = io.StringIO()
    with contextlib.redirect_stdout(buffer), contextlib.redirect_stderr(buffer):
        code = main(args)
    return code, buffer.getvalue()


class SynthTests(unittest.TestCase):
    def test_clip_is_deterministic_and_sized(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            res = Resolution(320, 180)
            first = generate_clip(res, 6, 24, 11, root / "a")
            second = generate_clip(res, 6, 24, 11, root / "b")
            self.assertEqual(first.sha256, second.sha256)
            self.assertEqual(first.path.stat().st_size, first.expected_size)
            self.assertEqual(first.expected_size, 320 * 180 * 3 // 2 * 6)

    def test_different_seed_changes_bytes(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            res = Resolution(320, 180)
            a = generate_clip(res, 3, 24, 1, Path(tmp) / "a")
            b = generate_clip(res, 3, 24, 2, Path(tmp) / "b")
            self.assertNotEqual(a.sha256, b.sha256)

    def test_reference_luma_shape_and_range(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            res = Resolution(320, 180)
            clip = generate_clip(res, 4, 24, 3, Path(tmp))
            luma = read_reference_luma(clip)
            self.assertEqual(luma.shape, (4, 180, 320))
            self.assertGreaterEqual(int(luma.min()), 16)
            self.assertLessEqual(int(luma.max()), 235)


class EncodeMeasureTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls) -> None:
        cls.encoder = detect_encoder()

    def setUp(self) -> None:
        self._tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self._tmp.cleanup)
        self.root = Path(self._tmp.name)
        self.res = Resolution(320, 180)
        self.clip = generate_clip(self.res, FRAMES, 24, 5, self.root / "src")
        self.reference = read_reference_luma(self.clip)

    def test_encode_and_measure_produce_contract_values(self) -> None:
        from ladderforge.encode import encode
        from ladderforge.plan import JobSpec

        job = JobSpec(self.res, 500, "ultrafast")
        output = self.root / "out.mp4"
        encode(self.encoder, self.clip, job, output_path=output, timeout=60)
        probed = probe(self.encoder, output, self.res)
        self.assertGreater(probed.duration_s, 0)
        self.assertEqual((probed.width, probed.height), (320, 180))

        result = measure(
            self.encoder, output, self.res, frames=FRAMES, reference_luma=self.reference
        )
        self.assertIsInstance(result.actual_bps, int)
        self.assertGreater(result.actual_bps, 0)
        self.assertAlmostEqual(
            result.actual_bps,
            round(result.encoded_bytes * 8 / result.duration_s),
            delta=1,
        )
        self.assertTrue(0.0 < result.quality_psnr_db < 100.0)

    def test_higher_target_improves_psnr(self) -> None:
        from ladderforge.encode import encode
        from ladderforge.plan import JobSpec

        scores = []
        for kbps in (200, 1500):
            job = JobSpec(self.res, kbps, "medium")
            out = self.root / f"{kbps}.mp4"
            encode(self.encoder, self.clip, job, output_path=out, timeout=60)
            scores.append(
                measure(
                    self.encoder, out, self.res, frames=FRAMES, reference_luma=self.reference
                )
            )
        self.assertGreater(scores[1].actual_bps, scores[0].actual_bps)
        self.assertGreaterEqual(scores[1].quality_psnr_db, scores[0].quality_psnr_db)

    def test_psnr_of_identical_frames_is_finite_and_high(self) -> None:
        frame = self.reference[:1]
        self.assertEqual(compute_psnr(frame, frame), 100.0)

    def test_psnr_rejects_shape_mismatch(self) -> None:
        from ladderforge.errors import TaskError

        with self.assertRaises(TaskError):
            compute_psnr(self.reference[:1], self.reference[:2])

    def test_missing_output_is_a_task_error(self) -> None:
        from ladderforge.errors import TaskError

        with self.assertRaises(TaskError):
            probe(self.encoder, self.root / "nope.mp4", self.res)


class PipelineTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls) -> None:
        cls.encoder = detect_encoder()

    def setUp(self) -> None:
        self._tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self._tmp.cleanup)
        self.root = Path(self._tmp.name)
        self.res = Resolution(320, 180)
        self.plan = build_plan(
            frames=FRAMES,
            resolutions=SMALL,
            bitrates="200,500,1000",
            presets="ultrafast",
            workers=2,
        )
        self.clip = generate_clip(self.res, FRAMES, 24, 9, self.root / "src")
        self.jobs = materialise_jobs(
            self.plan, {self.res: self.clip.sha256}, self.encoder.version
        )
        self.db = self.root / "scan.sqlite"

    def make_scheduler(self, store: Store, control: Control | None = None) -> Scheduler:
        return Scheduler(
            store=store,
            encoder=self.encoder,
            jobs=self.jobs,
            clips={self.res: self.clip},
            output_dir=self.root / "encoded",
            control=control,
            timeout=60,
        )

    def test_full_scan_builds_monotonic_ladder(self) -> None:
        with Store(self.db) as store:
            store.sync_plan(self.jobs)
            summary = self.make_scheduler(store).run(workers=2)
            keys = tuple(job.key for job in self.jobs)
            self.assertEqual(summary.state, "COMPLETE")
            self.assertEqual(summary.completed, 3)
            self.assertEqual(summary.failed, 0)

            rows = store.results(keys)
            candidates = [
                Candidate(
                    task_key=row["task_key"],
                    resolution=row["resolution"],
                    width=row["width"],
                    height=row["height"],
                    target_bps=row["target_bps"],
                    preset=row["preset"],
                    actual_bps=row["actual_bps"],
                    quality_psnr_db=row["quality_psnr_db"],
                )
                for row in rows
            ]
            ladder = build_ladder(candidates, self.plan.targets)
            self.assertEqual(len(ladder.rungs), 3)
            self.assertEqual(ladder.monotonicity, "PASS")
            previous = 0
