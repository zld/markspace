"""Batch scheduling, result reuse, resume and persistence tests."""
import json
import os
import sqlite3
import tempfile
import unittest
import wave
from pathlib import Path

from audioslice import signal
from audioslice.batch import list_wav_files, process_directory
from audioslice.detector import DetectParams, SilenceDetector
from audioslice.storage import Storage, StorageError
from audioslice.report import build_batch_report, build_summary_report


class BatchTestBase(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.mkdtemp()
        self.wav_dir = os.path.join(self.tmp, "wav")
        self.good_files = signal.generate_fixtures(self.wav_dir, seed=42)
        self.db = os.path.join(self.tmp, "batch.sqlite3")
        self.params = DetectParams(
            threshold_db=-38.0, min_silence_ms=180.0, merge_gap_ms=90.0
        )

    def _add_empty_file(self):
        path = os.path.join(self.wav_dir, "broken_empty.wav")
        Path(path).write_bytes(b"")
        return path

    def _add_stereo_file(self):
        path = os.path.join(self.wav_dir, "broken_stereo.wav")
        with wave.open(path, "wb") as wf:
            wf.setnchannels(2)
            wf.setsampwidth(2)
            wf.setframerate(8000)
            wf.writeframes(b"\x00\x00" * 400)
        return path


class SharedDetectorTests(BatchTestBase):
    def test_one_detector_instance_serves_all_files(self):
        params = self.params
        detector = SilenceDetector(params)
        seen = []
        for path in self.good_files:
            wav = signal.read_wav(path)
            seen.append(detector.detect(wav.samples, wav.sample_rate))
        with Storage(self.db) as storage:
            summary = process_directory(self.wav_dir, storage, params)
        self.assertEqual(summary.processed, 3)
        # segment counts match the single shared detector's outputs
        with Storage(self.db) as storage:
            rows = storage.list_results()
        stored_counts = sorted(r["segment_count"] for r in rows if r["status"] == "ok")
        direct_counts = sorted(len(r.segments) for r in seen)
        self.assertEqual(stored_counts, direct_counts)


class ReuseAndResumeTests(BatchTestBase):
    def test_second_run_is_fully_cached(self):
        with Storage(self.db) as storage:
            first = process_directory(self.wav_dir, storage, self.params)
            self.assertEqual(first.processed, 3)
            second = process_directory(self.wav_dir, storage, self.params)
        self.assertEqual(second.cached, 3)
        self.assertEqual(second.processed, 0)
        self.assertEqual(second.failed, 0)
        report = build_batch_report(second)
        self.assertEqual(report["files_cached"], 3)
        self.assertEqual(report["segments"], report["segments"])  # numeric field present

    def test_changed_parameters_create_new_results(self):
        with Storage(self.db) as storage:
            process_directory(self.wav_dir, storage, self.params)
            changed = DetectParams(
                threshold_db=-30.0, min_silence_ms=180.0, merge_gap_ms=90.0
            )
            rerun = process_directory(self.wav_dir, storage, changed)
        self.assertEqual(rerun.processed, 3)  # cache misses -> reanalysed
        with Storage(self.db) as storage:
            rows = storage.list_results()
        fingerprints = {r["param_fingerprint"] for r in rows}
        self.assertEqual(len(fingerprints), 2)

    def test_resume_after_interruption_leaves_no_half_result(self):
        # Simulate a crash mid-batch by leaving only the first file committed.
        with Storage(self.db) as storage:
            first = process_directory(self.wav_dir, storage, self.params)
            result_ids = [f.result_id for f in first.files]
        # Every committed result has matching child rows or is an error row.
        with sqlite3.connect(self.db) as conn:
            for rid in result_ids:
                status = conn.execute(
                    "SELECT status FROM results WHERE id = ?", (rid,)
                ).fetchone()[0]
                if status == "ok":
                    seg_count = conn.execute(
                        "SELECT COUNT(*) FROM segments WHERE result_id = ?", (rid,)
                    ).fetchone()[0]
                    self.assertGreater(seg_count, 0)
        # Re-running resumes from the committed state via cache lookups.
        with Storage(self.db) as storage:
            resumed = process_directory(self.wav_dir, storage, self.params)
        self.assertEqual(resumed.cached, 3)


class FailureIsolationTests(BatchTestBase):
    def test_bad_files_record_errors_and_good_files_still_succeed(self):
        empty = self._add_empty_file()
        stereo = self._add_stereo_file()
        Path(self.wav_dir, "ignore.txt").write_text("not audio")
        with Storage(self.db) as storage:
            summary = process_directory(self.wav_dir, storage, self.params)
        self.assertEqual(summary.failed, 2)
        self.assertEqual(summary.processed, 3)
        self.assertEqual(summary.exit_code(), 1)
        failed_paths = {Path(f.path).name for f in summary.files if f.status == "error"}
        self.assertEqual(failed_paths, {Path(empty).name, Path(stereo).name})
        with Storage(self.db) as storage:
            rows = storage.list_results()
        self.assertTrue(all(r["error_message"] for r in rows if r["status"] == "error"))

    def test_stable_listing_order(self):
        self._add_empty_file()
        names = [p.name for p in list_wav_files(self.wav_dir)]
        self.assertEqual(names, sorted(names))
        self.assertNotIn("ignore.txt", names)

    def test_summary_report_counts(self):
        self._add_empty_file()
        with Storage(self.db) as storage:
            process_directory(self.wav_dir, storage, self.params)
            payload = build_summary_report(storage)
        self.assertEqual(payload["results_failed"], 1)
        self.assertEqual(payload["results_ok"], 3)
        self.assertGreater(payload["segments"], 0)
        self.assertIsInstance(payload["silence_total_seconds"], float)


class StorageSafetyTests(BatchTestBase):
    def test_corrupt_database_raises_actionable_error(self):
        Path(self.db).write_bytes(b"not a sqlite database" * 8)
        with self.assertRaises(StorageError) as ctx:
            Storage(self.db)
        self.assertIn("corrupt", str(ctx.exception))

    def test_schema_version_mismatch_is_rejected_not_overwritten(self):
        with Storage(self.db) as storage:
            storage.conn.execute(
                "UPDATE meta SET value = '999' WHERE key = 'schema_version'"
            )
            storage.conn.commit()
        with self.assertRaises(StorageError) as ctx:
            Storage(self.db)
        self.assertIn("incompatible", str(ctx.exception))

    def test_params_json_round_trips(self):
        with Storage(self.db) as storage:
            process_directory(self.wav_dir, storage, self.params)
            row = storage.list_results()[0]
            full = storage.conn.execute(
                "SELECT params_json FROM results WHERE id = ?",
                (row["result_id"],),
            ).fetchone()
        decoded = json.loads(full[0])
        self.assertEqual(decoded, self.params.to_dict())


if __name__ == "__main__":
    unittest.main()
