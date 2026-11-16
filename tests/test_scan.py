"""Tests for ladderforge: recovery, dedup, and error paths."""

from __future__ import annotations

import contextlib
import io
import tempfile
import unittest
from pathlib import Path

from ladderforge import cli, fixtures, metrics, planner, selector, store


def make_spec(**kw) -> fixtures.ClipSpec:
    base = dict(width=96, height=64, fps=10, duration_s=1.0, seed=7)
    base.update(kw)
    return fixtures.ClipSpec(**base)


class PlannerTest(unittest.TestCase):
    def test_duplicate_targets_collapse_with_alias_count(self):
        tasks = planner.expand_grid("64x64", "100,100,200", "ultrafast")
        self.assertEqual(len(tasks), 2)
        by_target = {t.target_kbps: t for t in tasks}
        self.assertEqual(by_target[100].alias_count, 2)
        self.assertEqual(by_target[200].alias_count, 1)
        keys = [t.params_key for t in tasks]
        self.assertEqual(len(keys), len(set(keys)))

    def test_grid_expands_resolutions_and_presets(self):
        tasks = planner.expand_grid("64x64,96x64", "100", "ultrafast,medium")
        self.assertEqual(len(tasks), 4)

    def test_invalid_grid_values_rejected(self):
        with self.assertRaises(ValueError):
            planner.expand_grid("63x64", "100", "ultrafast")  # odd width
        with self.assertRaises(ValueError):
            planner.expand_grid("3840x2160", "100", "ultrafast")  # over 1080p
        with self.assertRaises(ValueError):
            planner.expand_grid("64x64", "100", "ludicrous")  # bad preset
        with self.assertRaises(ValueError):
            planner.expand_grid("64x64", "0", "ultrafast")  # bad target


class StoreRecoveryTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.db = store.Store(Path(self.tmp.name) / "s.sqlite")
        self.addCleanup(self.db.close)
        self.tasks = planner.expand_grid("64x64", "100,200", "ultrafast")
        self.db.upsert_tasks(self.tasks, source="src.y4m")

    def test_stale_running_task_reclaimed(self):
        due = self.db.due_tasks(2)
        self.assertEqual(len(due), 2)
        self.assertTrue(self.db.claim(due[0]["id"], 2))
        self.assertEqual(self.db.counts()["running"], 1)
        self.assertEqual(self.db.reclaim_stale(), 1)
        self.assertEqual(self.db.counts()["pending"], 2)

    def test_claim_is_atomic_and_retry_budget_ends_failed(self):
        task_id = self.db.due_tasks(2)[0]["id"]
        self.assertTrue(self.db.claim(task_id, 2))
        self.assertFalse(self.db.claim(task_id, 2))  # already running
        self.db.finish_failure(task_id, "boom", 2)
        self.assertEqual(self.db.counts()["pending"], 2)  # retry queued
        self.assertTrue(self.db.claim(task_id, 2))
        self.db.finish_failure(task_id, "boom again", 2)
        self.assertEqual(self.db.counts()["failed"], 1)

    def test_corrupt_artifact_requeued(self):
        task_id = self.db.due_tasks(2)[0]["id"]
        artifact = Path(self.tmp.name) / "a.mp4"
        artifact.write_bytes(b"original-bytes")
        self.db.claim(task_id, 2)
        self.db.mark_done(task_id, {
            "actual_kbps": 100.0, "quality_score": 50.0, "psnr": 40.0,
            "frames": 10, "duration_s": 1.0,
            "file_hash": store.sha256_file(artifact), "file_path": str(artifact),
            "encoder_version": "ffmpeg test", "params": {"key": "k"},
        })
        self.assertEqual(self.db.verify_artifacts(), [])
        artifact.write_bytes(b"tampered-bytes")
        issues = self.db.verify_artifacts()
        self.assertEqual(len(issues), 1)
        self.assertIn("hash mismatch", issues[0][1])
        self.assertEqual(self.db.counts()["pending"], 2)


class SelectorTest(unittest.TestCase):
    def rows(self, specs):
        return [
            {"target_kbps": t, "alias_count": 1, "params_key": f"k{i}",
             "actual_kbps": a, "quality_score": q}
            for i, (t, a, q) in enumerate(specs)
        ]

    def test_monotonic_ladder_selected(self):
        rungs = selector.select_ladder(
            self.rows([(100, 95.0, 30.0), (200, 190.0, 55.0), (400, 380.0, 70.0)])
        )
        self.assertEqual([r.target_kbps for r in rungs], [100, 200, 400])

    def test_degrading_rung_dropped(self):
        rows = self.rows([(100, 95.0, 30.0), (200, 190.0, 25.0), (400, 380.0, 70.0)])
        rungs = selector.select_ladder(rows)
        self.assertEqual([r.target_kbps for r in rungs], [100, 400])

    def test_dominated_candidate_removed(self):
        cands = [
            selector.Candidate(0, 100, 100.0, 50.0, 1, "a"),
            selector.Candidate(0, 100, 120.0, 40.0, 1, "b"),  # dominated
        ]
        self.assertEqual([c.params_key for c in selector.non_dominated(cands)], ["a"])

    def test_too_few_rungs_fails(self):
        with self.assertRaises(selector.LadderError):
            selector.select_ladder(self.rows([(100, 95.0, 30.0)]))
        with self.assertRaises(selector.LadderError):
            selector.select_ladder([])


class MetricsTest(unittest.TestCase):
    def test_identical_clip_scores_100(self):
        with tempfile.TemporaryDirectory() as tmp:
            clip = Path(tmp) / "a.y4m"
            fixtures.write_y4m(clip, make_spec())
            score, psnr, frames = metrics.quality_score(clip, clip, 96, 64)
            self.assertEqual(score, 100.0)
            self.assertEqual(frames, 10)

    def test_frame_mismatch_fails_alignment(self):
        with tempfile.TemporaryDirectory() as tmp:
            full = Path(tmp) / "full.y4m"
            short = Path(tmp) / "short.y4m"
            fixtures.write_y4m(full, make_spec())
            fixtures.write_y4m(short, make_spec(duration_s=0.5))
            with self.assertRaises(metrics.AlignError):
                metrics.quality_score(full, short, 96, 64)


class EndToEndTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        root = Path(self.tmp.name)
        self.src = root / "src.y4m"
        self.db = root / "scan.sqlite"
        self.out = root / "enc"
        fixtures.write_y4m(self.src, make_spec())
        self.scan_args = [
            "scan", "--input", str(self.src), "--db", str(self.db),
            "--out", str(self.out), "--targets-kbps", "100,100,300",
            "--resolutions", "96x64", "--presets", "ultrafast",
            "--workers", "1",
        ]

    def test_interrupt_resume_report(self):
        rc = cli.main(self.scan_args + ["--stop-after", "1"])
        self.assertEqual(rc, 10)  # resumable interruption
        rc = cli.main(self.scan_args)  # no --resume on existing db
        self.assertEqual(rc, 10)
        rc = cli.main(self.scan_args + ["--resume"])
        self.assertEqual(rc, 0)
        buf = io.StringIO()
        with contextlib.redirect_stdout(buf):
            rc = cli.main(["report", "--db", str(self.db), "--format", "tsv"])
        self.assertEqual(rc, 0)
        text = buf.getvalue()
        self.assertIn("tier\ttarget_kbps\tactual_kbps\tquality_score\t"
                      "alias_count\tmonotonicity", text)
        self.assertIn("PASS", text)
        self.assertIn("\t2\t", text)  # merged duplicate target has alias_count 2

    def test_invalid_inputs_exit_2(self):
        rc = cli.main(self.scan_args[:1] + ["--input", str(self.src) + ".missing"]
                      + self.scan_args[2:])
        self.assertEqual(rc, 2)
        bogus = Path(self.tmp.name) / "bogus.mp4"
        bogus.write_text("not a video")
        rc = cli.main(["scan", "--input", str(bogus), "--db",
                       str(Path(self.tmp.name) / "b.sqlite"),
                       "--out", str(self.out), "--targets-kbps", "100",
                       "--resolutions", "96x64", "--presets", "ultrafast"])
        self.assertEqual(rc, 2)

    def test_report_without_results_exit_4(self):
        db = Path(self.tmp.name) / "empty.sqlite"
        conn = store.Store(db)
        conn.close()
        rc = cli.main(["report", "--db", str(db)])
        self.assertEqual(rc, 4)


if __name__ == "__main__":
    unittest.main()
