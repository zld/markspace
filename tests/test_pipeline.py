"""End-to-end pipeline tests (self-check mode, persistence, repeat runs)."""

from __future__ import annotations

import contextlib
import io
import sqlite3
import sys
import tempfile
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "src"))

from forensic_timeline import cli  # noqa: E402


class PipelineTest(unittest.TestCase):
    def setUp(self) -> None:
        self._tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self._tmp.cleanup)
        self.db = Path(self._tmp.name) / "runs.sqlite"

    def run_cli(self, *extra: str) -> tuple[int, str]:
        out = io.StringIO()
        argv = [
            "--self-check",
            "--timezone", "Asia/Shanghai",
            "--drift-limit-seconds", "180",
            "--gap-threshold-seconds", "900",
            "--db", str(self.db),
            *extra,
        ]
        with contextlib.redirect_stdout(out):
            rc = cli.main(argv)
        return rc, out.getvalue()

    def test_self_check_end_to_end(self) -> None:
        rc, out = self.run_cli()
        self.assertEqual(rc, 0, out)
        for token in (
            "events=12",
            "duplicates=1",
            "hash_mismatches=2",
            "missing_intervals=1",
            "clock_drift_seconds=+37",
            "rejected_records=1",
            "clock_correction=applied",
        ):
            self.assertIn(token, out)
        # gap boundaries are printed
        self.assertIn("missing_interval kind=interior", out)
        self.assertIn("start=2026-03-01T00:25:00Z", out)
        self.assertIn("end=2026-03-01T00:55:00Z", out)
        self.assertIn("duration_seconds=1800", out)
        # at least one mismatched block offset is printed
        self.assertIn("hash_mismatch block=", out)
        self.assertIn("offset=", out)
        # all steps completed
        for step in ("read", "clock", "integrity", "timeline", "store"):
            self.assertIn(f"{step}=ok", out)

    def test_batch_persisted_atomically(self) -> None:
        rc, _ = self.run_cli()
        self.assertEqual(rc, 0)
        conn = sqlite3.connect(self.db)
        try:
            self.assertEqual(conn.execute("SELECT COUNT(*) FROM batches").fetchone()[0], 1)
            self.assertEqual(conn.execute("SELECT COUNT(*) FROM events").fetchone()[0], 12)
            self.assertEqual(conn.execute("SELECT COUNT(*) FROM gaps").fetchone()[0], 1)
            mismatches = conn.execute(
                "SELECT COUNT(*) FROM integrity WHERE status='hash_mismatch'"
            ).fetchone()[0]
            self.assertEqual(mismatches, 2)
            rejected = conn.execute(
                "SELECT COUNT(*) FROM anomalies WHERE kind='rejected_record'"
            ).fetchone()[0]
            self.assertEqual(rejected, 1)
            # raw timestamps and timezones are preserved alongside UTC
            row = conn.execute(
                "SELECT raw_timestamp, raw_timezone, naive, utc_raw, utc_final, corrected "
                "FROM events WHERE event_id='evt-000'"
            ).fetchone()
            self.assertEqual(row[0], "2026-03-01T08:00:37+08:00")
            self.assertEqual(row[1], "8:00:00")
            self.assertEqual(row[2], 0)  # record carried an explicit offset
            self.assertEqual(row[3], "2026-03-01T00:00:37Z")
            self.assertEqual(row[4], "2026-03-01T00:00:00Z")
            self.assertEqual(row[5], 1)
            # duplicate is kept and flagged
            dup = conn.execute(
                "SELECT COUNT(*) FROM events WHERE duplicate_of='evt-003'"
            ).fetchone()[0]
            self.assertEqual(dup, 1)
        finally:
            conn.close()

    def test_repeat_runs_use_unique_batch_ids(self) -> None:
        rc1, _ = self.run_cli()
        rc2, _ = self.run_cli()
        self.assertEqual((rc1, rc2), (0, 0))
        conn = sqlite3.connect(self.db)
        try:
            ids = [r[0] for r in conn.execute("SELECT batch_id FROM batches")]
            self.assertEqual(len(ids), 2)
            self.assertEqual(len(set(ids)), 2)
            self.assertEqual(conn.execute("SELECT COUNT(*) FROM events").fetchone()[0], 24)
        finally:
            conn.close()

    def test_json_report_written(self) -> None:
        json_path = Path(self._tmp.name) / "report.json"
        rc, out = self.run_cli("--json", str(json_path))
        self.assertEqual(rc, 0, out)
        import json
        data = json.loads(json_path.read_text(encoding="utf-8"))
        self.assertEqual(data["events"], 12)
        self.assertEqual(data["hash_mismatches"], 2)
        self.assertEqual(data["missing_intervals"], 1)
        self.assertEqual(data["clock_drift_seconds"], 37)
        self.assertEqual(len(data["gaps"]), 1)
        self.assertEqual(len(data["hash_mismatch_details"]), 2)


class CustomImagePipelineTest(unittest.TestCase):
    """End-to-end analysis of a hand-built image (--image/--manifest mode)."""

    def setUp(self) -> None:
        self._tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self._tmp.cleanup)
        self.dir = Path(self._tmp.name)

    def test_ambiguous_local_time_isolated_end_to_end(self) -> None:
        import hashlib
        import json
        from forensic_timeline.samplegen import BLOCK_SIZE, FORMAT_TAG

        good = b'{"id":"e1","ts":"2026-06-01T12:00:00Z","type":"x","desc":"ok"}\n'
        ambiguous = (b'{"id":"e2","ts":"2026-11-01T01:30:00",'
                     b'"type":"x","desc":"ambiguous"}\n')
        block = good + ambiguous
        block += b"\x00" * (BLOCK_SIZE - len(block))
        digest = hashlib.sha256(block).hexdigest()
        image = self.dir / "i.bin"
        image.write_bytes(block)
        manifest = {
            "format": FORMAT_TAG,
            "block_size": BLOCK_SIZE,
            "block_count": 1,
            "image_sha256": hashlib.sha256(block).hexdigest(),
            "acquisition_start_utc": "2026-01-01T00:00:00Z",
            "acquisition_end_utc": "2026-12-31T00:00:00Z",
            "blocks": [{"index": 0, "offset": 0, "length": BLOCK_SIZE,
                        "sha256": digest}],
            "anchors": [],
        }
        manifest_path = self.dir / "m.json"
        manifest_path.write_text(json.dumps(manifest), encoding="utf-8")
        db = self.dir / "runs.sqlite"

        out = io.StringIO()
        with contextlib.redirect_stdout(out):
            rc = cli.main([
                "--image", str(image), "--manifest", str(manifest_path),
                "--timezone", "America/New_York",
                "--drift-limit-seconds", "180",
                "--gap-threshold-seconds", "900",
                "--db", str(db),
            ])
        text = out.getvalue()
        self.assertEqual(rc, 0, text)
        # the good event survives; the ambiguous one is isolated, not guessed
        self.assertIn("events=1", text)
        self.assertIn("time_anomalies=1", text)
        self.assertIn("whole_image=match", text)
        self.assertIn("clock_correction=skipped", text)


if __name__ == "__main__":
    unittest.main()
