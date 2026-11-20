"""Interrupt/resume, idempotency and fingerprint-guard tests."""
from __future__ import annotations

import os
import tempfile
import unittest

from ladderlab import codec, fixture, scheduler, store


class ResumeTest(unittest.TestCase):
    def setUp(self) -> None:
        self.dir = tempfile.mkdtemp()
        self.db = os.path.join(self.dir, "scan.sqlite3")
        self.clips = fixture.generate_clips(41)
        self.params = codec.iter_params()
        self.fp = "test-fingerprint"
        conn = store.connect(self.db)
        store.init_schema(conn)
        store.check_fingerprint(conn, self.fp)
        store.seed_tasks(conn, [p.key for p in self.params])
        conn.close()

    def test_stop_then_resume_completes_all(self) -> None:
        stats = scheduler.run_scan(self.db, self.clips, self.params,
                                   workers=2, stop_after=5)
        self.assertFalse(stats.complete)
        self.assertEqual(stats.done, 5)
        self.assertEqual(stats.planned, 36)

        conn = store.connect(self.db)
        reclaimed = store.reclaim_running(conn)
        conn.close()
        self.assertEqual(reclaimed, 0)  # clean stop leaves nothing running

        stats2 = scheduler.run_scan(self.db, self.clips, self.params, workers=2)
        self.assertTrue(stats2.complete)
        self.assertEqual(stats2.done, 36)

    def test_interrupted_running_tasks_are_reclaimed(self) -> None:
        conn = store.connect(self.db)
        key = store.claim_task(conn)  # simulate crash: left 'running'
        conn.close()
        self.assertIsNotNone(key)
        conn = store.connect(self.db)
        self.assertEqual(store.reclaim_running(conn), 1)
        self.assertEqual(store.task_counts(conn)["pending"], 36)
        conn.close()

    def test_results_are_idempotent(self) -> None:
        scheduler.run_scan(self.db, self.clips, self.params, workers=2)
        conn = store.connect(self.db)
        first = store.load_results(conn)
        conn.close()
        # Re-running must not duplicate or alter results.
        scheduler.run_scan(self.db, self.clips, self.params, workers=2)
        conn = store.connect(self.db)
        second = store.load_results(conn)
        counts = store.task_counts(conn)
        conn.close()
        self.assertEqual(len(first), 36)
        self.assertEqual(first, second)
        self.assertEqual(counts["done"], 36)

    def test_fingerprint_mismatch_refused(self) -> None:
        conn = store.connect(self.db)
        with self.assertRaises(store.FingerprintMismatchError):
            store.check_fingerprint(conn, "other-fingerprint")
        conn.close()

    def test_corrupt_database_reported(self) -> None:
        bad = os.path.join(self.dir, "bad.sqlite3")
        with open(bad, "wb") as fh:
            fh.write(b"this is not a sqlite database" * 4)
        with self.assertRaises(store.CorruptDatabaseError):
            store.connect(bad)

    def test_workers_bound_validated(self) -> None:
        with self.assertRaises(ValueError):
            scheduler.run_scan(self.db, self.clips, self.params, workers=0)
        with self.assertRaises(ValueError):
            scheduler.run_scan(self.db, self.clips, self.params, workers=99)


if __name__ == "__main__":
    unittest.main()
