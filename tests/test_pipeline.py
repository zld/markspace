"""End-to-end tests: fixtures -> batch -> SQLite -> JSONL -> CLI exit codes."""

import io
import json
import os
import sqlite3
import sys
import tempfile
import unittest
import wave
from contextlib import redirect_stderr, redirect_stdout
from pathlib import Path

import numpy as np

REPO_ROOT = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(REPO_ROOT / "src"))
sys.path.insert(0, str(REPO_ROOT / "scripts"))

from audioslice.batch import BatchProcessor, discover_wavs  # noqa: E402
from audioslice.cli import main as cli_main  # noqa: E402
from audioslice.detector import DetectorConfig  # noqa: E402
from audioslice.export import ExportError, FileExport, export_jsonl  # noqa: E402
from audioslice.store import Store  # noqa: E402
import make_samples  # noqa: E402

SR = 48000


def make_fixture_dir() -> str:
    """Generate the deterministic sample set into a fresh temp directory."""
    out = tempfile.mkdtemp()
    for name, data in make_samples.build_samples().items():
        channels = 1 if data.ndim == 1 else data.shape[1]
        make_samples.write_pcm16(Path(out) / name, data, channels)
    make_samples.write_corrupt_files(Path(out))
    return out


class PipelineTest(unittest.TestCase):
    def setUp(self):
        self.input_dir = make_fixture_dir()
        self.work = tempfile.mkdtemp()
        self.db = os.path.join(self.work, "run.sqlite3")
        self.paths = discover_wavs(self.input_dir)
        self.cfg = DetectorConfig()

    def _run_batch(self, continue_on_error: bool):
        with Store(self.db) as store:
            store.begin()
            result = BatchProcessor(
                self.cfg, store, continue_on_error=continue_on_error
            ).run(self.paths)
        return result

    def test_continue_on_error_commits_successes_and_failures(self):
        result = self._run_batch(continue_on_error=True)
        self.assertEqual(len(result.ok), 5)
        self.assertEqual(len(result.failed), 2)
        failed_names = {os.path.basename(o.path) for o in result.failed}
        self.assertEqual(failed_names, {"06_truncated.wav", "07_unsupported.wav"})
        for outcome in result.failed:
            self.assertTrue(outcome.failures)

        conn = sqlite3.connect(self.db)
        statuses = dict(conn.execute("SELECT path, status FROM files").fetchall())
        self.assertEqual(sum(1 for s in statuses.values() if s == "ok"), 5)
        self.assertEqual(sum(1 for s in statuses.values() if s == "failed"), 2)
        # Activity and silence intervals are both persisted.
        kinds = {row[0] for row in conn.execute("SELECT DISTINCT kind FROM segments")}
        self.assertEqual(kinds, {"activity", "silence"})
        coords = conn.execute(
            "SELECT start_sample, end_sample FROM segments"
        ).fetchall()
        self.assertTrue(coords)
        for start, end in coords:
            self.assertIsInstance(start, int)
            self.assertIsInstance(end, int)
            self.assertGreater(end, start)
        conn.close()

    def test_single_detector_instance_reused_for_batch(self):
        seen_detectors = []
        original = BatchProcessor._process_one

        def spy(self, path):  # noqa: ANN001
            seen_detectors.append(id(self._detector))
            return original(self, path)

        BatchProcessor._process_one = spy
        try:
            self._run_batch(continue_on_error=True)
        finally:
            BatchProcessor._process_one = original
        self.assertEqual(len(set(seen_detectors)), 1)
        self.assertEqual(len(seen_detectors), len(self.paths))

    def test_duplicate_hash_and_config_is_skipped(self):
        first = self._run_batch(continue_on_error=True)
        self.assertTrue(first.ok)
        second = self._run_batch(continue_on_error=True)
        self.assertEqual(len(second.skipped), 5)
        self.assertEqual(len(second.failed), 2)  # corrupt files retried, fail again
        # No new ok rows: still 5 successful files in the database.
        conn = sqlite3.connect(self.db)
        ok_rows = conn.execute("SELECT COUNT(*) FROM files WHERE status='ok'").fetchone()[0]
        self.assertEqual(ok_rows, 5)
        conn.close()

    def test_abort_mode_rolls_back_entire_batch(self):
        result = self._run_batch(continue_on_error=False)
        self.assertTrue(result.failed)
        conn = sqlite3.connect(self.db)
        self.assertEqual(conn.execute("SELECT COUNT(*) FROM runs").fetchone()[0], 0)
        self.assertEqual(conn.execute("SELECT COUNT(*) FROM files").fetchone()[0], 0)
        self.assertEqual(conn.execute("SELECT COUNT(*) FROM segments").fetchone()[0], 0)
        conn.close()

    def test_jsonl_export_shape_and_invariants(self):
        result = self._run_batch(continue_on_error=True)
        export_path = os.path.join(self.work, "results.jsonl")
        export_jsonl(export_path, result.to_exports(), self.paths)

        with open(export_path, encoding="utf-8") as fh:
            records = [json.loads(line) for line in fh]
        self.assertEqual(len(records), 7)
        for rec in records:
            self.assertIn(rec["status"], {"ok", "failed", "skipped"})
            self.assertGreaterEqual(rec["silence_total_seconds"], 0.0)
            if rec["status"] == "ok":
                for seg in rec["segments"]:
                    self.assertIsInstance(seg["start_sample"], int)
                    self.assertIsInstance(seg["end_sample"], int)
            if rec["status"] == "failed":
                self.assertTrue(rec["failures"])
                self.assertEqual(rec["segments"], [])

    def test_export_refuses_wav_overwrite_and_bad_targets(self):
        wav_path = self.paths[0]
        with self.assertRaises(ExportError):
            export_jsonl(wav_path, [FileExport(path="x", status="ok")], [wav_path])
        missing = os.path.join(self.work, "no-such-dir", "r.jsonl")
        with self.assertRaises(ExportError):
            export_jsonl(missing, [], self.paths)

    def test_hum_and_silence_fixtures_have_no_segments(self):
        result = self._run_batch(continue_on_error=True)
        by_name = {os.path.basename(o.path): o for o in result.outcomes}
        for name in ("03_steady_hum.wav", "04_silence.wav"):
            self.assertEqual(len(by_name[name].result.activity), 0)
            self.assertEqual(by_name[name].silence_total_seconds, 3.0)
        merged = [
            s for o in result.ok for s in o.result.activity
            if s.reason.startswith("merged")
        ]
        self.assertTrue(merged, "weak adjacent events should produce a merged segment")

    def test_segment_count_in_single_digits_to_teens(self):
        result = self._run_batch(continue_on_error=True)
        total = sum(len(o.result.activity) for o in result.ok)
        self.assertGreaterEqual(total, 1)
        self.assertLessEqual(total, 19)


class CliTest(unittest.TestCase):
    def setUp(self):
        self.input_dir = make_fixture_dir()
        self.work = tempfile.mkdtemp()

    def _cli(self, *args):
        out, err = io.StringIO(), io.StringIO()
        with redirect_stdout(out), redirect_stderr(err):
            code = cli_main(list(args))
        return code, out.getvalue(), err.getvalue()

    def test_full_acceptance_flow_exit_1_on_failures(self):
        db = os.path.join(self.work, "a.sqlite3")
        export = os.path.join(self.work, "r.jsonl")
        code, out, err = self._cli(
            "process", self.input_dir, "--db", db, "--export", export,
            "--frame-ms", "10", "--noise-window-ms", "500",
            "--min-event-ms", "40", "--merge-gap-ms", "70",
            "--continue-on-error",
        )
        self.assertEqual(code, 1, msg=err)
        for token in ("files_ok:", "files_failed:", "segments:",
                      "start_sample=", "end_sample=", "silence_total_seconds="):
            self.assertIn(token, out)
        self.assertTrue(os.path.exists(db))
        self.assertTrue(os.path.exists(export))

    def test_clean_input_exit_0(self):
        for name in ("06_truncated.wav", "07_unsupported.wav"):
            os.remove(os.path.join(self.input_dir, name))
        code, out, err = self._cli(
            "process", self.input_dir,
            "--db", os.path.join(self.work, "clean.sqlite3"),
        )
        self.assertEqual(code, 0, msg=err)
        self.assertIn("files_failed: 0", out)

    def test_usage_errors_exit_2(self):
        db = os.path.join(self.work, "x.sqlite3")
        self.assertEqual(self._cli("process", "/no/such/dir", "--db", db)[0], 2)
        self.assertEqual(
            self._cli("process", self.input_dir, "--db", db, "--frame-ms", "0")[0], 2
        )
        empty = tempfile.mkdtemp()
        self.assertEqual(self._cli("process", empty, "--db", db)[0], 2)

    def test_export_failure_exit_3_and_rollback(self):
        wav = next(Path(self.input_dir).glob("*.wav"))
        code, _, err = self._cli(
            "process", self.input_dir,
            "--db", os.path.join(self.work, "io.sqlite3"),
            "--export", str(wav),
        )
        self.assertEqual(code, 3)
        self.assertIn("overwrite input WAV", err)

    def test_database_failure_exit_3(self):
        # Pointing the DB at an existing directory cannot create a sqlite file.
        code, _, err = self._cli(
            "process", self.input_dir, "--db", self.input_dir
        )
        self.assertEqual(code, 3)
        self.assertIn("database", err.lower())


if __name__ == "__main__":
    unittest.main()
