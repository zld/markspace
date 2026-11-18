"""Consistency rules, database transactions, and CLI behaviour."""

from __future__ import annotations

import io
import json
import os
import sqlite3
import sys
import tempfile
import unittest
from contextlib import redirect_stderr, redirect_stdout
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / "src"))
sys.path.insert(0, str(ROOT / "scripts"))

import make_samples as samples  # noqa: E402
from mediacheck import cli  # noqa: E402
from mediacheck.db import Database, compute_batch_id  # noqa: E402
from mediacheck.models import Container, Track  # noqa: E402
from mediacheck.pack import build_pack  # noqa: E402
from mediacheck.probe import probe_path  # noqa: E402
from mediacheck.rules import evaluate  # noqa: E402


def write_batch(directory: Path) -> Path:
    return Path(samples.write_samples(directory)[0]).parent


class RuleTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls) -> None:
        cls.tmp = tempfile.TemporaryDirectory()
        cls.root = str(write_batch(Path(cls.tmp.name)))

    @classmethod
    def tearDownClass(cls) -> None:
        cls.tmp.cleanup()

    def containers(self) -> list[Container]:
        return [probe_path(str(Path(self.root) / name)) for name in
                ("clip-a.mkv", "clip-b.mkv", "clip-c.mp4")]

    def test_batch_counts(self) -> None:
        report = evaluate(self.containers(), self.root)
        self.assertEqual(len(report.files), 3)
        self.assertEqual(report.track_count, 5)
        self.assertEqual(report.codec_summary,
                         {"audio": ["aac"], "video": ["h264"]})

    def test_duration_tolerance_controls_deviation_issue(self) -> None:
        strict = evaluate(self.containers(), self.root, duration_tolerance_ms=0)
        self.assertTrue(any(i.code == "duration-deviation" for i in strict.issues))
        loose = evaluate(self.containers(), self.root, duration_tolerance_ms=40)
        self.assertTrue(any(i.code == "duration-deviation" for i in loose.issues))
        self.assertTrue(any(i.deviation_ms == 80 for i in loose.issues))
        forgiving = evaluate(self.containers(), self.root,
                             duration_tolerance_ms=100)
        self.assertFalse(any(i.code == "duration-deviation" for i in forgiving.issues))

    def test_structural_drift_detected(self) -> None:
        report = evaluate(self.containers(), self.root)
        codes = {issue.code for issue in report.issues}
        self.assertIn("track-count-drift", codes)
        self.assertIn("track-kind-drift", codes)
        self.assertEqual(report.duration_deviation_ms, 80)

    def test_rule_versions_are_reported(self) -> None:
        report = evaluate(self.containers(), self.root)
        for issue in report.issues:
            self.assertTrue(issue.rule.startswith("R"))

    def test_stable_ordering(self) -> None:
        first = evaluate(self.containers(), self.root).scan_dict("b")
        second = evaluate(list(reversed(self.containers())), self.root).scan_dict("b")
        self.assertEqual(first, second)

    def test_negative_tolerance_rejected(self) -> None:
        with self.assertRaises(ValueError):
            evaluate([], self.root, duration_tolerance_ms=-1)

    def test_unknown_metadata_is_strict_error(self) -> None:
        container = Container(path=os.path.join(self.root, "x.mkv"),
                              container="ebml:matroska")
        container.tracks = [Track(None, "video", "h264", 1000, 0, 10, {})]
        relaxed = evaluate([container], self.root)
        strict = evaluate([container], self.root, strict=True)
        self.assertTrue(any(i.code == "unknown-metadata" for i in relaxed.issues))
        severities = {i.severity for i in relaxed.issues if i.code == "unknown-metadata"}
        self.assertEqual(severities, {"warning"})
        self.assertEqual(
            {i.severity for i in strict.issues if i.code == "unknown-metadata"},
            {"error"},
        )

    def test_parse_error_is_located_issue(self) -> None:
        broken = Path(self.root) / "broken.mkv"
        broken.write_bytes(b"\x1a\x45\xdf\xa3")
        try:
            report = evaluate([probe_path(str(broken))], self.root)
        finally:
            broken.unlink()
        self.assertTrue(any(i.code == "container-error" for i in report.issues))
        self.assertIn("offset", report.issues[0].message)


class DatabaseTests(unittest.TestCase):
    def setUp(self) -> None:
        self.tmp = tempfile.TemporaryDirectory()
        self.base = Path(self.tmp.name)
        self.sample_root = write_batch(self.base / "media")
        self.db_path = self.base / "results.sqlite"

    def tearDown(self) -> None:
        self.tmp.cleanup()

    def probe_all(self) -> list[Container]:
        return [probe_path(str(self.sample_root / name))
                for name in ("clip-a.mkv", "clip-b.mkv", "clip-c.mp4")]

    def test_round_trip(self) -> None:
        containers = self.probe_all()
        report = evaluate(containers, str(self.sample_root))
        with Database(self.db_path) as database:
            batch_id = database.save_batch(containers, report, {"strict": False})
            loaded = database.load_batch(batch_id)
            self.assertIsNotNone(loaded)
            reloaded, issues = loaded
            self.assertEqual([c.sha256 for c in reloaded],
                             [c.sha256 for c in containers])
            self.assertEqual(len(issues), len(report.issues))
            self.assertEqual(database.check_integrity(), "ok")

    def test_repeat_run_is_idempotent(self) -> None:
        containers = self.probe_all()
        report = evaluate(containers, str(self.sample_root))
        with Database(self.db_path) as database:
            first = database.save_batch(containers, report, {"strict": False})
            second = database.save_batch(containers, report, {"strict": False})
            self.assertEqual(first, second)
            self.assertEqual(database.batch_count(), 1)
            self.assertEqual(database.issue_count(first), len(report.issues))

    def test_batch_id_is_deterministic_and_input_sensitive(self) -> None:
        containers = self.probe_all()
        self.assertEqual(compute_batch_id(containers, {"a": 1}),
                         compute_batch_id(list(reversed(containers)), {"a": 1}))
        self.assertNotEqual(compute_batch_id(containers, {"a": 1}),
                            compute_batch_id(containers, {"a": 2}))

    def test_transaction_rolls_back_without_partial_rows(self) -> None:
        containers = self.probe_all()
        report = evaluate(containers, str(self.sample_root))
        with Database(self.db_path) as database:
            batch_id = database.save_batch(containers, report, {"strict": False})
            before = database.issue_count(batch_id)
            database.conn.execute(
                "CREATE TRIGGER fail_issue BEFORE INSERT ON issues "
                "BEGIN SELECT RAISE(ABORT, 'forced failure'); END"
            )
            with self.assertRaises(sqlite3.DatabaseError):
                database.save_batch(containers, report, {"strict": False})
            self.assertEqual(database.batch_count(), 1)
            self.assertEqual(database.issue_count(batch_id), before)
            database.conn.execute("DROP TRIGGER fail_issue")
            self.assertEqual(database.save_batch(containers, report,
                                                 {"strict": False}), batch_id)

    def test_failed_first_insert_leaves_empty_database(self) -> None:
        containers = self.probe_all()
        report = evaluate(containers, str(self.sample_root))
        with Database(self.db_path) as database:
            database.conn.execute(
                "CREATE TRIGGER fail_file BEFORE INSERT ON files "
                "BEGIN SELECT RAISE(ABORT, 'forced failure'); END"
            )
            with self.assertRaises(sqlite3.DatabaseError):
                database.save_batch(containers, report, {"strict": False})
            self.assertEqual(database.batch_count(), 0)
            self.assertEqual(database.check_integrity(), "ok")


class CliTests(unittest.TestCase):
    def setUp(self) -> None:
        self.tmp = tempfile.TemporaryDirectory()
        self.base = Path(self.tmp.name)
        self.root = write_batch(self.base / "media")
        self.db = self.base / "db.sqlite"
        self.report = self.base / "report.json"

    def tearDown(self) -> None:
        self.tmp.cleanup()

    def run_cli(self, argv: list[str]) -> tuple[int, str, str]:
        out, err = io.StringIO(), io.StringIO()
        with redirect_stdout(out), redirect_stderr(err):
            code = cli.main(argv)
        return code, out.getvalue(), err.getvalue()

    def test_scan_writes_terminal_and_json(self) -> None:
        code, out, _ = self.run_cli([
            "scan", str(self.root), "--db", str(self.db),
            "--json", str(self.report), "--duration-tolerance-ms", "100",
        ])
        self.assertEqual(code, 1)  # structural drift remains
        payload = json.loads(self.report.read_text())
        self.assertEqual(payload["files"], 3)
        self.assertEqual(payload["tracks"], 5)
        self.assertIn(f"batch_id: {payload['batch_id']}", out)
        self.assertEqual(payload["duration_deviation_ms"], 80)
        self.assertIn("track-count-drift", {i["code"] for i in payload["issues"]})
        self.assertFalse(any(tmp.name.startswith(".mediacheck-")
                             for tmp in self.base.iterdir()))

    def test_success_exit_zero_for_consistent_inputs(self) -> None:
        single = self.base / "one"
        single.mkdir()
        (single / "only.mkv").write_bytes(
            (self.root / "clip-a.mkv").read_bytes()
        )
        code, out, _ = self.run_cli(["scan", str(single), "--db", str(self.db)])
        self.assertEqual(code, 0)
        self.assertIn("issues: 0", out)

    def test_unknown_file_does_not_block_others(self) -> None:
        (self.root / "a-unknown.bin").write_bytes(b"not media at all")
        code, out, _ = self.run_cli(["scan", str(self.root), "--db", str(self.db)])
        self.assertEqual(code, 1)
        self.assertIn("files: 4", out)
        self.assertIn("container-error", out)

    def test_compare_records_baseline(self) -> None:
        # Scan with the default tolerance, then compare with a different
        # tolerance: the option change produces a new batch, so the scan is
        # available as a baseline and the same content must not look changed.
        code, _, _ = self.run_cli(["scan", str(self.root), "--db", str(self.db)])
        self.assertEqual(code, 1)
        code, out, _ = self.run_cli([
            "compare", str(self.root), "--db", str(self.db),
            "--duration-tolerance-ms", "100", "--json", str(self.report),
        ])
        payload = json.loads(self.report.read_text())
        self.assertEqual(payload["files"], 3)
        self.assertIsNotNone(payload["baseline_batch_id"])
        self.assertNotIn("input-changed", {i["code"] for i in payload["issues"]})
        self.assertEqual(code, 1)

    def test_exclude_and_stable_paths(self) -> None:
        code, out, _ = self.run_cli([
            "scan", str(self.root), "--db", str(self.db),
            "--exclude", "*.mp4", "--duration-tolerance-ms", "100",
        ])
        self.assertEqual(code, 0)
        self.assertIn("files: 2", out)
        self.assertIn("tracks: 4", out)

    def test_scan_reports_are_byte_identical_across_runs(self) -> None:
        outputs = []
        for index in range(2):
            db = self.base / f"db{index}.sqlite"
            report = self.base / f"report{index}.json"
            code, stdout, _ = CliTests.run_cli(self, [
                "scan", str(self.root), "--db", str(db), "--json", str(report),
                "--duration-tolerance-ms", "100",
            ])
            self.assertEqual(code, 1)
            outputs.append((stdout, report.read_text()))
        self.assertEqual(outputs[0], outputs[1])

    def test_same_batch_rerun_updates_in_place(self) -> None:
        args = ["scan", str(self.root), "--db", str(self.db)]
        self.run_cli(args)
        self.run_cli(args)
        with Database(self.db) as database:
            self.assertEqual(database.batch_count(), 1)

    def test_max_depth_bound(self) -> None:
        code, _, err = self.run_cli([
            "scan", str(self.root), "--db", str(self.db), "--max-depth", "17",
        ])
        self.assertEqual(code, 2)
        self.assertIn("between 0 and 16", err)

    def test_missing_path_is_environment_error(self) -> None:
        code, _, err = self.run_cli(["scan", str(self.base / "nope"),
                                     "--db", str(self.db)])
        self.assertEqual(code, 2)
        self.assertIn("does not exist", err)

    def test_unwritable_report_keeps_terminal_diagnostics(self) -> None:
        code, out, err = self.run_cli([
            "scan", str(self.root), "--db", str(self.db),
            "--json", str(self.base / "missing-dir" / "r.json"),
        ])
        self.assertEqual(code, 2)
        self.assertIn("files: 3", out)
        self.assertIn("output directory does not exist", err)

    def test_json_is_deterministic(self) -> None:
        args = ["scan", str(self.root), "--db", str(self.db), "--json",
                str(self.report), "--duration-tolerance-ms", "100"]
        self.run_cli(args)
        first = self.report.read_text()
        self.run_cli(args)
        self.assertEqual(first, self.report.read_text())

    def test_negative_tolerance_is_command_error(self) -> None:
        code, _, err = self.run_cli(["scan", str(self.root), "--db", str(self.db),
                                     "--duration-tolerance-ms", "-5"])
        self.assertEqual(code, 2)
        self.assertIn("must be >= 0", err)

    def test_recursive_scan_and_exclusion_patterns(self) -> None:
        nested = self.root / "nested"
        nested.mkdir()
        (nested / "copy.mkv").write_bytes((self.root / "clip-a.mkv").read_bytes())
        code, out, _ = self.run_cli([
            "scan", str(self.root), "--db", str(self.db), "--exclude", "nested/*",
        ])
        self.assertEqual(code, 1)
        self.assertIn("files: 3", out)

    def test_symlink_is_not_followed(self) -> None:
        link = self.root / "link.mkv"
        os.symlink(self.root / "clip-a.mkv", link)
        try:
            code, out, _ = self.run_cli(["scan", str(self.root),
                                         "--db", str(self.db)])
        finally:
            link.unlink()
        self.assertEqual(code, 1)
        self.assertIn("files: 3", out)


class PackTests(unittest.TestCase):
    def setUp(self) -> None:
        self.tmp = tempfile.TemporaryDirectory()
        self.base = Path(self.tmp.name)
        self.root = write_batch(self.base / "media")

    def tearDown(self) -> None:
        self.tmp.cleanup()

    def test_pack_reuses_probe_results(self) -> None:
        path = str(self.root / "clip-a.mkv")
        result = build_pack([path])
        manifest = result["manifest"]
        probed = probe_path(path)
        self.assertEqual(result["exit_code"], 0)
        self.assertEqual(len(manifest["inputs"]), 1)
        self.assertEqual(manifest["inputs"][0]["sha256"], probed.sha256)
        self.assertEqual(len(manifest["tracks"]), 2)
        self.assertEqual(
            [row["codec"] for row in manifest["tracks"]], ["h264", "aac"]
        )
        self.assertEqual(manifest["tracks"][0]["params"],
                         {"height": 360, "width": 640})

    def test_pack_records_errors(self) -> None:
        bad = self.base / "bad.mkv"
        bad.write_bytes(b"\x1a\x45")
        result = build_pack([str(bad)])
        self.assertEqual(result["exit_code"], 1)
        self.assertEqual(result["manifest"]["errors"][0]["path"], str(bad))

    def test_pack_cli_writes_manifest_atomically(self) -> None:
        out = self.base / "pack.json"
        code, stdout, _ = CliTests.run_cli(self, [
            "pack", str(self.root / "clip-a.mkv"), "--out", str(out),
        ])
        self.assertEqual(code, 0)
        manifest = json.loads(out.read_text())
        self.assertEqual(manifest["format"], "mediacheck-pack/1")
        self.assertIn("manifest:", stdout)


if __name__ == "__main__":
    unittest.main()
