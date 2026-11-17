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
from ladderforge.encode import detect_encoder
from ladderforge.errors import EXIT_FAILURE, EXIT_OK
from ladderforge.ladder import Candidate, build_ladder
from ladderforge.measure import compute_psnr, measure, probe
from ladderforge.plan import Resolution, build_plan, materialise_jobs
from ladderforge.scheduler import Control, Scheduler
from ladderforge.store import Store
from ladderforge.synth import generate_clip, read_reference_luma

SMALL = "320x180"
FRAMES = 12


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
            for rung in ladder.rungs:
                self.assertGreater(rung.actual_bps, 0)
                self.assertGreaterEqual(rung.actual_bps, previous)
                previous = rung.actual_bps

    def test_limit_then_resume_reuses_completed(self) -> None:
        with Store(self.db) as store:
            store.sync_plan(self.jobs)
            first = self.make_scheduler(store).run(workers=1, limit_jobs=2)
            self.assertEqual(first.completed, 2)
            self.assertEqual(first.pending, 1)

            second = self.make_scheduler(store).run(workers=2)
            self.assertEqual(second.state, "COMPLETE")
            self.assertEqual(second.reused, 2)
            self.assertEqual(second.newly_completed, 1)

    def test_failed_job_does_not_block_others(self) -> None:
        with Store(self.db) as store:
            store.sync_plan(self.jobs)
            failing = self.jobs[0]
            original_complete = store.complete

            def flaky_complete(**kwargs):
                if kwargs["task_key"] == failing.key:
                    raise RuntimeError("simulated failure")
                return original_complete(**kwargs)

            store.complete = flaky_complete  # type: ignore[assignment]
            summary = self.make_scheduler(store).run(workers=1)
            self.assertGreaterEqual(summary.failed, 1)
            self.assertGreaterEqual(summary.completed, 1)

    def test_sigint_like_stop_returns_jobs_to_pending(self) -> None:
        control = Control()
        with Store(self.db) as store:
            store.sync_plan(self.jobs)
            scheduler = self.make_scheduler(store, control)
            timer = threading.Timer(0.02, lambda: control.stop("interrupt"))
            timer.start()
            try:
                summary = scheduler.run(workers=1)
            finally:
                timer.cancel()
            keys = tuple(job.key for job in self.jobs)
            counts = store.state_counts(keys)
            self.assertEqual(summary.state, "PENDING")
            self.assertEqual(counts["running"], 0)
            self.assertEqual(counts["failed"], 0)
            self.assertEqual(counts["pending"], len(keys))

    def test_pause_then_resume_completes(self) -> None:
        control = Control()
        with Store(self.db) as store:
            store.sync_plan(self.jobs)
            scheduler = self.make_scheduler(store, control)
            control.pause()
            thread = threading.Thread(target=lambda: scheduler.run(workers=1))
            thread.start()
            time.sleep(0.15)
            # Nothing should have been claimed while paused.
            keys = tuple(job.key for job in self.jobs)
            self.assertEqual(store.state_counts(keys)["pending"], len(keys))
            control.resume()
            thread.join(timeout=60)
            self.assertFalse(thread.is_alive())
            self.assertEqual(store.state_counts(keys)["completed"], len(keys))


class CliTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls) -> None:
        cls.encoder = detect_encoder()

    def test_cli_limit_then_resume(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            db = Path(tmp) / "demo.sqlite"
            common = [
                "run",
                "--db",
                str(db),
                "--frames",
                str(FRAMES),
                "--resolutions",
                SMALL,
                "--bitrates-kbps",
                "200,500,1000,1000",
                "--presets",
                "ultrafast",
                "--workers",
                "2",
            ]
            code, out = run_cli(common + ["--limit-jobs", "2"])
            self.assertEqual(code, EXIT_OK)
            self.assertIn("state=PARTIAL", out)
            self.assertIn("jobs_total=3", out)

            code, out = run_cli(common + ["--resume"])
            self.assertEqual(code, EXIT_OK)
            self.assertIn("state=COMPLETE", out)
            self.assertIn("duplicate_targets=1", out)
            self.assertIn("reused=2", out)
            self.assertIn("monotonicity=PASS", out)

    def test_cli_rejects_bad_configuration(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            db = Path(tmp) / "demo.sqlite"
            code, out = run_cli(
                ["run", "--db", str(db), "--resolutions", "641x361", "--frames", "8"]
            )
            self.assertEqual(code, EXIT_FAILURE)
            self.assertIn("error=", out)

    def test_cli_refuses_resume_after_plan_change(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            db = Path(tmp) / "demo.sqlite"
            base = ["run", "--db", str(db), "--frames", str(FRAMES), "--presets", "ultrafast"]
            code, _ = run_cli(base + ["--resolutions", SMALL, "--limit-jobs", "1"])
            self.assertEqual(code, EXIT_OK)
            code, out = run_cli(
                base + ["--resolutions", "480x270", "--resume"]
            )
            self.assertEqual(code, EXIT_FAILURE)
            self.assertIn("refusing to resume", out)

    def test_cli_json_report_written(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            db = Path(tmp) / "demo.sqlite"
            json_out = Path(tmp) / "report.json"
            code, _ = run_cli(
                [
                    "run",
                    "--db",
                    str(db),
                    "--frames",
                    str(FRAMES),
                    "--resolutions",
                    SMALL,
                    "--bitrates-kbps",
                    "200,500,1000",
                    "--presets",
                    "ultrafast",
                    "--json-out",
                    str(json_out),
                ]
            )
            self.assertEqual(code, EXIT_OK)
            payload = json.loads(json_out.read_text())
            self.assertEqual(payload["run"]["state"], "COMPLETE")
            self.assertEqual(len(payload["ladder"]["rungs"]), 3)
            for rung in payload["ladder"]["rungs"]:
                self.assertIsInstance(rung["actual_bps"], int)
                self.assertTrue(0.0 < rung["quality_psnr_db"] < 200.0)

    def test_cli_exits_two_on_non_monotonic_ladder(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            db = Path(tmp) / "demo.sqlite"
            args = [
                "run",
                "--db",
                str(db),
                "--frames",
                str(FRAMES),
                "--resolutions",
                SMALL,
                "--bitrates-kbps",
                "200,500,1000",
                "--presets",
                "ultrafast",
            ]
            # Build the real plan so task keys match, then seed results that
            # violate monotonicity (PSNR falls as bitrate rises).
            plan = build_plan(
                frames=FRAMES,
                resolutions=SMALL,
                bitrates="200,500,1000",
                presets="ultrafast",
            )
            res = Resolution(320, 180)
            clip = generate_clip(res, FRAMES, 24, 9, Path(tmp) / "seed")
            encoder = self.encoder
            jobs = materialise_jobs(plan, {res: clip.sha256}, encoder.version)
            seeds = {
                200_000: (250_000, 34.0),
                500_000: (600_000, 32.0),
                1_000_000: (1_200_000, 31.0),
            }
            with Store(db) as store:
                store.init_schema()
                store.sync_plan(jobs)
                store.set_meta("plan_signature", Scheduler.signature_for(jobs))
                for _ in range(len(jobs)):
                    row = store.claim("seed", limit=1, lease_seconds=60)[0]
                    actual, psnr = seeds[int(row["target_bps"])]
                    store.complete(
                        task_key=row["task_key"],
                        worker_id="seed",
                        actual_bps=actual,
                        quality_psnr_db=psnr,
                        encoded_bytes=1000,
                        duration_s=1.0,
                        output_path=str(Path(tmp) / "seed.mp4"),
                    )
            code, out = run_cli(args + ["--content-seed", "9", "--resume"])
            self.assertEqual(code, 2)
            self.assertIn("ladder_error=", out)


if __name__ == "__main__":
    unittest.main()
