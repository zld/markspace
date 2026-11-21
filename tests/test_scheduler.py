"""Store and scheduler tests: atomic claims, leases, resume and failures."""

from __future__ import annotations

import tempfile
import threading
import unittest
from pathlib import Path

from abrscan.fixture import render_fixture
from abrscan.params import ParamError, parse_scan_spec
from abrscan.scheduler import execute_scan, make_task_key
from abrscan.store import DONE, Measurement, Store, StoreError, check_db_path

FIXTURE = "moving-texture"
WIDTH, HEIGHT, FRAMES, FPS = 32, 32, 6, 24
TARGETS = "120,200,120"
QUANTS = "20,60"
BLOCKS = "8"


class StoreAndSchedulerTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.root = Path(self.tmp.name)
        self.db = check_db_path(self.root / "runs" / "t.sqlite", self.root)
        self.store = Store(self.db)
        self.fixture = render_fixture(FIXTURE, WIDTH, HEIGHT, FRAMES, FPS)
        self.spec = parse_scan_spec(TARGETS, QUANTS, BLOCKS)
        self.run, _ = self.store.ensure_run(
            self.fixture.name,
            self.fixture.content_hash,
            self.fixture.width,
            self.fixture.height,
            self.fixture.frame_count,
            self.fixture.fps,
            self.spec.targets_kbps,
            self.spec.duplicate_targets,
        )
        self.store.sync_tasks(
            self.run.run_id,
            {
                (quant, block): make_task_key(self.fixture.content_hash, quant, block)
                for quant, block in self.spec.combos
            },
        )

    def tearDown(self):
        self.tmp.cleanup()

    def test_db_must_stay_inside_workspace(self):
        with self.assertRaises(ParamError):
            check_db_path("/tmp/outside.sqlite", self.root)
        with self.assertRaises(ParamError):
            check_db_path(self.root / "bad.txt", self.root)

    def test_unknown_measurement_rejected(self):
        conn = self.store.connect()
        try:
            with self.assertRaises(StoreError):
                self.store.measure_task(conn, "nope")
        finally:
            conn.close()

    def test_stop_after_interrupts_and_resume_reuses_work(self):
        first = execute_scan(
            self.store,
            self.run,
            self.fixture,
            self.spec.combos,
            workers=1,
            stop_after=1,
        )
        self.assertTrue(first.interrupted)
        self.assertEqual(first.counts[DONE], 1)
        self.assertEqual(first.counts["pending"] + first.counts["failed"], 1)

        second = execute_scan(
            self.store, self.run, self.fixture, self.spec.combos, workers=2
        )
        self.assertFalse(second.interrupted)
        self.assertTrue(second.resumed)
        self.assertEqual(second.inserted, 0)
        self.assertEqual(second.counts[DONE], 2)
        self.assertEqual(len(self.store.measurements(self.run.run_id)), 2)

    def test_unique_keys_avoid_duplicate_measurements_on_rerun(self):
        execute_scan(self.store, self.run, self.fixture, self.spec.combos, workers=2)
        again = execute_scan(
            self.store, self.run, self.fixture, self.spec.combos, workers=2
        )
        self.assertEqual(again.completed_now, 0)
        self.assertEqual(again.inserted, 0)
        self.assertEqual(again.counts[DONE], len(self.spec.combos))

    def test_expired_lease_is_recoverable(self):
        conn = self.store.connect()
        try:
            first = self.store.claim_next(conn, self.run.run_id, "w1", now=100.0)
            second = self.store.claim_next(conn, self.run.run_id, "w2", now=100.0)
            self.assertNotEqual(first.id, second.id)
            self.assertIsNone(
                self.store.claim_next(conn, self.run.run_id, "w3", now=101.0)
            )
            recovered = self.store.recover_expired_leases(self.run.run_id, now=999.0)
            self.assertEqual(recovered, 2)
            retried = self.store.claim_next(conn, self.run.run_id, "w3", now=1000.0)
            self.assertIn(retried.id, (first.id, second.id))
            self.assertEqual(retried.attempts, 2)
        finally:
            conn.close()

    def test_failed_task_is_retried_then_capped(self):
        conn = self.store.connect()
        try:
            task = self.store.claim_next(conn, self.run.run_id, "w", now=0.0)
            first_id = task.id
            for attempt in range(3):
                self.assertEqual(task.attempts, attempt + 1)
                self.store.fail_task(conn, task, f"boom {attempt}", now=float(attempt))
                task = self.store.claim_next(
                    conn, self.run.run_id, "w", now=float(attempt)
                )
            self.assertNotEqual(task.id, first_id)
            for attempt in range(3):
                self.store.fail_task(conn, task, "boom", now=float(attempt))
                task = self.store.claim_next(
                    conn, self.run.run_id, "w", now=float(attempt)
                )
            self.assertIsNone(task)
        finally:
            conn.close()

    def test_concurrent_claims_never_duplicate(self):
        claimed: list[int] = []
        lock = threading.Lock()
        barrier = threading.Barrier(4)

        def worker(name: str) -> None:
            conn = self.store.connect()
            try:
                barrier.wait()
                while True:
                    task = self.store.claim_next(conn, self.run.run_id, name)
                    if task is None:
                        return
                    with lock:
                        claimed.append(task.id)
            finally:
                conn.close()

        threads = [threading.Thread(target=worker, args=(f"w{i}",)) for i in range(4)]
        for thread in threads:
            thread.start()
        for thread in threads:
            thread.join(timeout=10)
        self.assertFalse(any(thread.is_alive() for thread in threads))
        self.assertEqual(len(claimed), len(self.spec.combos))
        self.assertEqual(len(set(claimed)), len(claimed))

    def test_worker_runs_full_scan_with_parallel_workers(self):
        outcome = execute_scan(
            self.store, self.run, self.fixture, self.spec.combos, workers=4
        )
        self.assertFalse(outcome.interrupted)
        self.assertEqual(outcome.counts[DONE], len(self.spec.combos))
        self.assertEqual(
            len(self.store.measurements(self.run.run_id)), len(self.spec.combos)
        )
        self.assertEqual(self.store.failed_tasks(self.run.run_id), [])

    def test_measurement_and_done_are_atomic(self):
        conn = self.store.connect()
        try:
            task = self.store.claim_next(conn, self.run.run_id, "w", now=10.0)
            wrong = Measurement(
                task_key="other",
                quant_step=task.quant_step,
                block_size=task.block_size,
                encoded_bytes=1,
                actual_kbps=1.0,
                quality_db=30.0,
                payload_sha256="x" * 64,
            )
            with self.assertRaises(StoreError):
                self.store.complete_task(conn, task, wrong, now=11.0)
            self.assertEqual(self.store.task_counts(self.run.run_id)[DONE], 0)

            good = Measurement(
                task.task_key, task.quant_step, task.block_size, 1000, 20.0, 30.0, "y"
            )
            self.store.complete_task(conn, task, good, now=12.0)
            self.assertEqual(self.store.task_counts(self.run.run_id)[DONE], 1)
            self.assertEqual(self.store.measurements(self.run.run_id), [good])
            with self.assertRaises(StoreError):
                self.store.complete_task(conn, task, good, now=13.0)
        finally:
            conn.close()

    def test_invalid_measurement_is_refused(self):
        conn = self.store.connect()
        try:
            task = self.store.claim_next(conn, self.run.run_id, "w", now=1.0)
            for bad in (
                Measurement(task.task_key, 20, 8, 0, 10.0, 30.0, "x"),
                Measurement(task.task_key, 20, 8, 10, float("nan"), 30.0, "x"),
                Measurement(task.task_key, 20, 8, 10, 10.0, float("inf"), "x"),
            ):
                with self.assertRaises(StoreError):
                    self.store.complete_task(conn, task, bad, now=2.0)
            self.assertEqual(self.store.task_counts(self.run.run_id)[DONE], 0)
        finally:
            conn.close()

    def test_task_keys_include_content_and_parameters(self):
        key = make_task_key(self.fixture.content_hash, 20, 8)
        self.assertIn(self.fixture.content_hash[:20], key)
        self.assertNotEqual(key, make_task_key(self.fixture.content_hash, 20, 16))

    def test_same_fixture_reuses_run_row(self):
        other, created = self.store.ensure_run(
            self.fixture.name,
            self.fixture.content_hash,
            self.fixture.width,
            self.fixture.height,
            self.fixture.frame_count,
            self.fixture.fps,
            (640,),
            (),
        )
        self.assertFalse(created)
        self.assertEqual(other.run_id, self.run.run_id)
        self.assertEqual(other.targets, (640,))


if __name__ == "__main__":
    unittest.main()
