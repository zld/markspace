"""End-to-end checks: WAV validation, CLI batches, SQLite and export."""
from __future__ import annotations

import contextlib
import io
import json
import shutil
import sqlite3
import struct
import tempfile
import unittest
from pathlib import Path

import numpy as np

from audiosplit.cli import main
from audiosplit.store import Store
from audiosplit.wav import WavError, WavReader
from scripts.make_fixtures import write_pcm16

RATE = 48000


def run_cli(*args: str) -> tuple[int, str, str]:
    stdout, stderr = io.StringIO(), io.StringIO()
    with contextlib.redirect_stdout(stdout), contextlib.redirect_stderr(stderr):
        code = main(list(args))
    return code, stdout.getvalue(), stderr.getvalue()


def summary(stdout: str) -> dict[str, str]:
    values = {}
    for line in stdout.splitlines():
        if "=" in line:
            key, _, value = line.partition("=")
            values[key] = value
    return values


class PipelineFixture(unittest.TestCase):
    def setUp(self) -> None:
        self._tmp = tempfile.TemporaryDirectory()
        self.root = Path(self._tmp.name)
        self.audio = self.root / "audio"
        self.audio.mkdir()
        self.db = self.root / "db" / "audio.db"
        self.export = self.root / "out" / "segments.jsonl"

    def tearDown(self) -> None:
        self._tmp.cleanup()

    def make_file(self, name: str, samples: np.ndarray) -> Path:
        path = self.audio / name
        write_pcm16(path, samples, RATE)
        return path

    def two_events(self) -> np.ndarray:
        rng = np.random.default_rng(23)
        samples = rng.normal(0, 0.004, RATE * 2).astype(np.float32)
        tone = 0.2 * np.sin(2 * np.pi * 440 * np.arange(RATE // 4) / RATE)
        samples[RATE // 4 : RATE // 4 + RATE // 4] += tone.astype(np.float32)
        samples[RATE : RATE + RATE // 4] += tone.astype(np.float32)
        return samples

    def scan(self, *extra: str) -> tuple[int, str, str]:
        return run_cli(
            "scan",
            str(self.audio),
            "--db",
            str(self.db),
            "--frame-ms",
            "10",
            "--on-db",
            "4",
            "--off-db",
            "2",
            "--min-silence-ms",
            "120",
            *extra,
        )


class WavValidationTests(PipelineFixture):
    def test_rejects_empty_and_non_pcm_with_path_and_reason(self) -> None:
        empty = self.audio / "empty.wav"
        empty.write_bytes(b"")
        good = self.make_file("good.wav", np.zeros(RATE, dtype=np.float32))
        bad = self.audio / "float.wav"
        data = bytearray(good.read_bytes())
        struct.pack_into("<H", data, 20, 3)
        bad.write_bytes(bytes(data))
        for path, needle in ((empty, "empty"), (bad, "format")):
            with self.assertRaises(WavError) as ctx:
                WavReader(path)
            self.assertIn(str(path), str(ctx.exception))
            self.assertIn(needle, str(ctx.exception).lower())
        with self.assertRaises(WavError) as empty_ctx:
            WavReader(empty)
        self.assertIn("0 bytes", str(empty_ctx.exception))

    def test_rejects_truncated_data_block(self) -> None:
        good = self.make_file("good.wav", np.zeros(RATE, dtype=np.float32))
        cut = self.audio / "cut.wav"
        cut.write_bytes(good.read_bytes()[:-100])
        with self.assertRaises(WavError) as ctx:
            WavReader(cut)
        self.assertIn("truncated", str(ctx.exception))
        self.assertIn(str(cut), str(ctx.exception))

    def test_iter_blocks_is_bounded(self) -> None:
        samples = np.zeros(RATE * 3, dtype=np.float32)
        path = self.make_file("long.wav", samples)
        reader = WavReader(path)
        blocks = list(reader.iter_blocks(1024))
        self.assertEqual(sum(b.shape[0] for b in blocks), reader.info.total_samples)
        self.assertTrue(all(b.shape[0] <= 1024 for b in blocks))


class BatchScanTests(PipelineFixture):
    def test_scan_reports_summary_and_exports_jsonl(self) -> None:
        self.make_file("a.wav", self.two_events())
        self.make_file("b.wav", np.zeros(RATE, dtype=np.float32))
        code, out, _err = self.scan("--export", str(self.export))
        self.assertEqual(code, 0)
        values = summary(out)
        self.assertEqual(values["files_ok"], "2")
        self.assertEqual(values["files_failed"], "0")
        self.assertGreaterEqual(int(values["segments"]), 2)
        self.assertGreater(int(values["silence_total_ms"]), 500)
        rows = [json.loads(line) for line in self.export.read_text().splitlines()]
        self.assertEqual(len(rows), int(values["segments"]))
        for row in rows:
            self.assertIsInstance(row["start_sample"], int)
            self.assertIsInstance(row["end_sample"], int)
            self.assertTrue(0 <= row["start_sample"] < row["end_sample"])
            self.assertLessEqual(row["end_sample"], row["total_samples"])

    def test_all_silent_input_yields_zero_segments(self) -> None:
        self.make_file("silent.wav", np.zeros(RATE, dtype=np.float32))
        code, out, _err = self.scan()
        self.assertEqual(code, 0)
        self.assertEqual(summary(out)["segments"], "0")

    def test_failed_file_is_recorded_and_batch_continues(self) -> None:
        self.make_file("good.wav", self.two_events())
        (self.audio / "broken.wav").write_bytes(b"RIFF")
        code, out, err = self.scan()
        self.assertEqual(code, 1)
        values = summary(out)
        self.assertEqual(values["files_ok"], "1")
        self.assertEqual(values["files_failed"], "1")
        self.assertIn("broken.wav", err)
        with sqlite3.connect(self.db) as conn:
            statuses = conn.execute(
                "SELECT status, COUNT(*) FROM jobs GROUP BY status"
            ).fetchall()
            self.assertIn(("error", 1), statuses)

    def test_repeat_scan_reuses_and_force_recomputes(self) -> None:
        self.make_file("a.wav", self.two_events())
        first = summary(self.scan()[1])
        code, out, err = self.scan()
        self.assertEqual(code, 0)
        self.assertIn("(reused)", err)
        second = summary(out)
        self.assertEqual(first["segments"], second["segments"])
        self.assertNotEqual(first["run_id"], second["run_id"])
        code, out, _err = self.scan("--force")
        self.assertEqual(code, 0)
        self.assertEqual(summary(out)["segments"], first["segments"])
        with sqlite3.connect(self.db) as conn:
            runs = conn.execute("SELECT COUNT(*) FROM runs").fetchone()[0]
            jobs = conn.execute("SELECT COUNT(*) FROM jobs").fetchone()[0]
        self.assertEqual(runs, 3)
        self.assertEqual(jobs, 3)

    def test_invalid_parameters_exit_2_without_jobs(self) -> None:
        self.make_file("a.wav", self.two_events())
        code, _out, err = self.scan("--on-db", "2", "--off-db", "3")
        self.assertEqual(code, 2)
        self.assertIn("off_db", err)
        # A guard longer than an individual file is a per-file failure.
        code, out, err = self.scan("--pre-roll", "99999999")
        self.assertEqual(code, 1)
        self.assertIn("pre_roll", err)
        self.assertEqual(summary(out)["files_failed"], "1")
        self.assertEqual(summary(out)["files_ok"], "0")

    def test_database_path_error_exits_2(self) -> None:
        target = self.root / "blocked"
        target.write_text("not a directory")
        code, _out, err = run_cli(
            "scan", str(self.audio), "--db", str(target / "x.db")
        )
        self.assertEqual(code, 2)
        self.assertIn("error", err)


class ExportTests(PipelineFixture):
    def test_export_is_stable_and_traceable(self) -> None:
        self.make_file("b.wav", self.two_events())
        self.make_file("a.wav", self.two_events())
        self.scan("--export", str(self.export))
        first = self.export.read_text()
        rows = [json.loads(line) for line in first.splitlines()]
        paths = [row["input_path"] for row in rows]
        self.assertEqual(paths, sorted(paths))
        self.assertTrue(all("file_hash" in row and "run_id" in row for row in rows))
        self.assertTrue(all(row["params"]["frame_ms"] == 10.0 for row in rows))
        self.assertTrue(all(row["segment_count"] == sum(
            1 for other in rows if other["input_path"] == row["input_path"]
        ) for row in rows))
        code, out, _err = run_cli(
            "export", "--db", str(self.db), "--out", str(self.export)
        )
        self.assertEqual(code, 0)
        self.assertEqual(self.export.read_text(), first)
        self.assertIn("params=", out)
        self.assertEqual(summary(out)["segments"], str(len(rows)))

    def test_export_unknown_run_exits_2(self) -> None:
        Store(self.db).close()
        code, _out, err = run_cli(
            "export", "--db", str(self.db), "--run-id", "42", "--out", str(self.export)
        )
        self.assertEqual(code, 2)
        self.assertIn("42", err)


class StoreTests(PipelineFixture):
    def test_segment_rows_are_ordered_and_check_constraints(self) -> None:
        with Store(self.db) as store:
            run = store.start_run({"frame_ms": 10.0})
            self.assertEqual(store.run_summary(run)["files_ok"], 0)
            self.assertIsNone(store.find_reusable("x.wav", "hash", "{}"))
        with sqlite3.connect(self.db) as conn:
            with self.assertRaises(sqlite3.IntegrityError):
                conn.execute(
                    "INSERT INTO jobs (run_id, input_path, file_hash, status) "
                    "VALUES (1, 'p', 'h', 'bogus')"
                )
            job = conn.execute(
                "INSERT INTO jobs (run_id, input_path, file_hash, status) "
                "VALUES (1, 'p', 'h', 'ok')"
            ).lastrowid
            with self.assertRaises(sqlite3.IntegrityError):
                conn.execute(
                    "INSERT INTO segments (job_id, ordinal, start_sample, "
                    "end_sample, peak, rms, noise_floor_db, duration_ms) "
                    "VALUES (?, 0, 10, 10, 0, 0, -60, 0)",
                    (job,),
                )

    def test_failed_segment_insert_rolls_back_the_job(self) -> None:
        from audiosplit.merge import Segment
        from audiosplit.store import JobMeta, StoreError

        with Store(self.db) as store:
            run = store.start_run({"frame_ms": 10.0})
            store.connection.execute(
                "CREATE TRIGGER reject_segments BEFORE INSERT ON segments "
                "BEGIN SELECT RAISE(ABORT, 'simulated disk failure'); END"
            )
            segment = Segment(0, 100, 0.5, 0.3, -50.0, 10.0)
            with self.assertRaises(StoreError):
                store.record_success(
                    run,
                    "clip.wav",
                    "hash",
                    JobMeta(48000, 1, 48000, -60.0),
                    [segment],
                )
            self.assertEqual(store.run_summary(run)["files_ok"], 0)
            self.assertIsNone(store.find_reusable("clip.wav", "hash", "{}"))

    def test_mixed_sample_rates_are_diagnosed(self) -> None:
        self.make_file("a.wav", np.zeros(48000, dtype=np.float32))
        other = self.audio / "b.wav"
        write_pcm16(other, np.zeros(44100, dtype=np.float32), 44100)
        code, out, err = self.scan()
        self.assertEqual(code, 0)
        self.assertIn("mixes sample rates", err)
        self.assertEqual(summary(out)["files_ok"], "2")
        with sqlite3.connect(self.db) as conn:
            rates = [row[0] for row in conn.execute("SELECT sample_rate FROM jobs")]
        self.assertEqual(sorted(rates), [44100, 48000])

    def test_missing_database_parent_is_created(self) -> None:
        nested = self.root / "a" / "b" / "c.db"
        with Store(nested) as store:
            self.assertEqual(store.latest_run_id(), None)
        self.assertTrue(nested.exists())


if __name__ == "__main__":
    unittest.main()
