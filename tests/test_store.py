"""Unit tests for SQLite state transitions, leases and concurrent claims."""

from __future__ import annotations

import tempfile
import threading
import unittest
from pathlib import Path

from ladderforge.plan import Resolution, build_plan, materialise_jobs, source_id
from ladderforge.store import COMPLETED, FAILED, PENDING, RUNNING, Store


def make_jobs(count: int = 4):
    plan = build_plan(
        frames=8,
        resolutions="320x180",
        bitrates=",".join(str(100 * (i + 1)) for i in range(count)),
        presets="fast",
    )
    hashes = {Resolution(320, 180): source_id(Resolution(320, 180), 8, 24, 1)}
    return materialise_jobs(plan, hashes, "9.0.2")


class StoreTestCase(unittest.TestCase):
    def setUp(self) -> None:
        self._tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self._tmp.cleanup)
        self.store = Store(Path(self._tmp.name) / "test.sqlite", busy_timeout=2.0)
        self.store.init_schema()
        self.addCleanup(self.store.close)
        self.jobs = make_jobs()
        self.store.sync_plan(self.jobs)
        self.keys = tuple(job.key for job in self.jobs)


class SchemaTests(StoreTestCase):
    def test_wal_mode_enabled(self) -> None:
        mode = self.store.connect().execute("PRAGMA journal_mode").fetchone()[0]
        self.assertEqual(mode.lower(), "wal")

    def test_sync_plan_is_idempotent(self) -> None:
        result = self.store.sync_plan(self.jobs)
        self.assertEqual(result["inserted"], 0)
        self.assertEqual(result["existing"], len(self.jobs))

    def test_initial_counts(self) -> None:
        counts = self.store.state_counts(self.keys)
        self.assertEqual(counts[PENDING], len(self.keys))
        self.assertEqual(counts[COMPLETED], 0)


class ClaimTests(StoreTestCase):
    def test_claim_marks_running_and_increments_attempts(self) -> None:
        rows = self.store.claim("w1", limit=2, lease_seconds=30)
        self.assertEqual(len(rows), 2)
        self.assertTrue(all(r["state"] == RUNNING for r in rows))
        self.assertTrue(all(r["attempts"] == 1 for r in rows))

    def test_claim_does_not_reissue_running_jobs(self) -> None:
        first = {r["task_key"] for r in self.store.claim("w1", limit=2, lease_seconds=30)}
        second = {r["task_key"] for r in self.store.claim("w2", limit=2, lease_seconds=30)}
        self.assertFalse(first & second)

    def test_expired_lease_is_reclaimed(self) -> None:
        self.store.claim("w1", limit=1, lease_seconds=0.01)
        reclaimed = self.store.claim("w2", limit=1, lease_seconds=30, now=10**12)
        self.assertEqual(len(reclaimed), 1)
        self.assertEqual(reclaimed[0]["lease_owner"], "w2")

    def test_recover_expired_resets_to_pending(self) -> None:
        self.store.claim("w1", limit=3, lease_seconds=1)
        moved = self.store.recover_expired(now=10**12)
        self.assertEqual(moved, 3)
        self.assertEqual(self.store.state_counts(self.keys)[PENDING], len(self.keys))

    def test_release_returns_job_to_queue(self) -> None:
        row = self.store.claim("w1", limit=1, lease_seconds=30)[0]
        self.assertTrue(self.store.release(row["task_key"], "w1", "interrupted"))
        self.assertEqual(self.store.state_counts(self.keys)[PENDING], len(self.keys))

    def test_renew_extends_only_owned_lease(self) -> None:
        row = self.store.claim("w1", limit=1, lease_seconds=30)[0]
        self.assertTrue(self.store.renew(row["task_key"], "w1", 60))
        self.assertFalse(self.store.renew(row["task_key"], "w2", 60))


class ConcurrencyTests(StoreTestCase):
    def test_many_threads_never_double_claim(self) -> None:
        claimed: list[str] = []
        lock = threading.Lock()
        barrier = threading.Barrier(8)

        def worker(name: str) -> None:
            barrier.wait()
            while True:
                rows = self.store.claim(name, limit=1, lease_seconds=30)
                if not rows:
                    return
                with lock:
                    claimed.append(rows[0]["task_key"])

        threads = [threading.Thread(target=worker, args=(f"w{i}",)) for i in range(8)]
        for thread in threads:
            thread.start()
        for thread in threads:
            thread.join(timeout=30)
        self.assertEqual(len(claimed), len(self.jobs))
        self.assertEqual(len(set(claimed)), len(self.jobs))


class CompletionTests(StoreTestCase):
    def _complete(self, key: str, worker: str = "w1") -> bool:
        return self.store.complete(
            task_key=key,
            worker_id=worker,
            actual_bps=500_000,
            quality_psnr_db=30.5,
            encoded_bytes=100_000,
            duration_s=2.0,
            output_path="/tmp/out.mp4",
        )

    def test_complete_records_result_and_state(self) -> None:
        row = self.store.claim("w1", limit=1, lease_seconds=30)[0]
        self.assertTrue(self._complete(row["task_key"]))
        self.assertEqual(self.store.state_counts(self.keys)[COMPLETED], 1)
        results = self.store.results((row["task_key"],))
        self.assertEqual(results[0]["actual_bps"], 500_000)
        self.assertAlmostEqual(results[0]["quality_psnr_db"], 30.5)

    def test_complete_twice_is_idempotent(self) -> None:
        row = self.store.claim("w1", limit=1, lease_seconds=30)[0]
        self.assertTrue(self._complete(row["task_key"]))
        self.assertFalse(self._complete(row["task_key"]))
        self.assertEqual(self.store.state_counts(self.keys)[COMPLETED], 1)

    def test_completion_requires_matching_lease_owner(self) -> None:
        row = self.store.claim("w1", limit=1, lease_seconds=30)[0]
        self.assertFalse(self._complete(row["task_key"], worker="w2"))
        self.assertEqual(self.store.state_counts(self.keys)[COMPLETED], 0)

    def test_fail_and_explicit_retry(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            store = Store(Path(tmp) / "one.sqlite", busy_timeout=2.0)
            store.init_schema()
            try:
                jobs = make_jobs(count=1)
                keys = tuple(job.key for job in jobs)
                store.sync_plan(jobs)
                row = store.claim("w1", limit=1, lease_seconds=30)[0]
                self.assertTrue(store.fail(row["task_key"], "w1", "boom"))
                self.assertEqual(store.state_counts(keys)[FAILED], 1)
                # A failed job is not reissued implicitly.
                self.assertFalse(store.claim("w2", limit=1, lease_seconds=30))
                self.assertEqual(store.requeue_failed(), 1)
                self.assertTrue(store.claim("w2", limit=1, lease_seconds=30))
            finally:
                store.close()

    def test_completed_results_survive_requeue_of_failures(self) -> None:
        first = self.store.claim("w1", limit=1, lease_seconds=30)[0]
        self.store.complete(
            task_key=first["task_key"],
            worker_id="w1",
            actual_bps=1,
            quality_psnr_db=1.0,
            encoded_bytes=1,
            duration_s=1.0,
            output_path="/tmp/x.mp4",
        )
        second = self.store.claim("w1", limit=1, lease_seconds=30)[0]
        self.store.fail(second["task_key"], "w1", "boom")
        self.store.requeue_failed()
        self.assertIn(first["task_key"], self.store.completed_keys(self.keys))


if __name__ == "__main__":
    unittest.main()
