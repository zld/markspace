"""End-to-end CLI, store and packer behaviour."""

from __future__ import annotations

import io
import json
import sqlite3
import sys
import tempfile
import unittest
from contextlib import redirect_stderr, redirect_stdout
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / "src"))
sys.path.insert(0, str(ROOT / "scripts"))

import make_samples  # noqa: E402
from mediacheck.cli import (  # noqa: E402
    EXIT_INPUT,
    EXIT_MISMATCH,
    EXIT_OK,
    EXIT_STORE,
    main,
)
from mediacheck.store import open_store  # noqa: E402


def run_cli(*argv: str) -> tuple[int, str, str]:
    out, err = io.StringIO(), io.StringIO()
    with redirect_stdout(out), redirect_stderr(err):
        code = main(list(argv))
    return code, out.getvalue(), err.getvalue()


class CliFixtureCase(unittest.TestCase):
    @classmethod
    def setUpClass(cls) -> None:
        cls._tmp = tempfile.TemporaryDirectory()
        cls.tmp = Path(cls._tmp.name)
        cls.samples = cls.tmp / "samples"
        make_samples.main(["--out", str(cls.samples)])

    @classmethod
    def tearDownClass(cls) -> None:
        cls._tmp.cleanup()

    def path(self, name: str) -> str:
        return str(self.samples / name)

    def out(self, name: str) -> Path:
        target = self.tmp / name
        if target.exists():
            target.unlink()
        return target


class CompareCliTest(CliFixtureCase):
    def test_acceptance_flow(self) -> None:
        db = self.tmp / "results.sqlite"
        report = self.tmp / "report.json"
        for target in (db, report):
            if target.exists():
                target.unlink()
        code, out, _err = run_cli(
            "compare",
            "--baseline", self.path("base.mp4"),
            "--candidate", self.path("candidate.mkv"),
            "--db", str(db), "--json", str(report),
            "--tolerance-ms", "40")
        self.assertEqual(code, EXIT_MISMATCH)
        self.assertIn("baseline_tracks: 2", out)
        self.assertIn("candidate_tracks: 2", out)
        self.assertIn("duration_delta_ms: 12.0", out)
        self.assertIn("status: issues", out)
        self.assertIn(str(report), out)

        payload = json.loads(report.read_text())
        self.assertEqual(payload["baseline_tracks"], 2)
        self.assertEqual(payload["candidate_tracks"], 2)
        self.assertAlmostEqual(payload["duration_delta_ms"], 12.0)
        self.assertGreaterEqual(len(payload["mismatches"]), 2)
        kinds = {m["kind"] for m in payload["mismatches"]}
        self.assertIn("codec_change", kinds)
        self.assertTrue(any("duration" in k for k in kinds))
        for mismatch in payload["mismatches"]:
            self.assertIn("offset", mismatch)
        for track in payload["baseline"]["tracks"]:
            for key in ("codec", "time_base", "duration_ms"):
                self.assertIn(key, track)
        for track in payload["candidate"]["tracks"]:
            for key in ("codec", "time_base", "duration_ms"):
                self.assertIn(key, track)

    def test_within_tolerance_exits_zero(self) -> None:
        code, out, _ = run_cli(
            "compare", "--baseline", self.path("base.mp4"),
            "--candidate", self.path("mov_variant.mov"),
            "--tolerance-ms", "40")
        self.assertEqual(code, EXIT_OK)
        self.assertIn("status: match", out)

    def test_ignore_field_changes_result(self) -> None:
        code, out, _ = run_cli(
            "compare", "--baseline", self.path("base.mp4"),
            "--candidate", self.path("candidate.mkv"),
            "--tolerance-ms", "40",
            "--ignore-field", "codec", "--ignore-field", "time_base",
            "--ignore-field", "duration_ms")
        self.assertEqual(code, EXIT_OK)
        self.assertIn("status: match", out)

    def test_unprobeable_candidate_exits_one(self) -> None:
        code, _out, err = run_cli(
            "compare", "--baseline", self.path("base.mp4"),
            "--candidate", self.path("truncated.mp4"))
        self.assertEqual(code, EXIT_INPUT)
        self.assertIn("mp4.box_overrun", err)

    def test_missing_file_exits_one(self) -> None:
        code, _out, err = run_cli(
            "compare", "--baseline", self.path("base.mp4"),
            "--candidate", str(self.tmp / "nope.mp4"))
        self.assertEqual(code, EXIT_INPUT)
        self.assertIn("file not found", err)

    def test_report_to_missing_directory_exits_three(self) -> None:
        code, _out, err = run_cli(
            "compare", "--baseline", self.path("base.mp4"),
            "--candidate", self.path("candidate.mkv"),
            "--json", str(self.tmp / "absent" / "report.json"))
        self.assertEqual(code, EXIT_STORE)
        self.assertIn("output directory does not exist", err)

    def test_print_json_emits_full_document(self) -> None:
        code, out, _ = run_cli(
            "--print-json", "compare",
            "--baseline", self.path("base.mp4"),
            "--candidate", self.path("candidate.mkv"))
        self.assertEqual(code, EXIT_MISMATCH)
        document = json.loads(out)
        self.assertIn("mismatches", document)
        self.assertIn("baseline", document)


class StoreCliTest(CliFixtureCase):
    def test_compare_writes_files_tracks_and_validation(self) -> None:
        db = self.tmp / "store.sqlite"
        if db.exists():
            db.unlink()
        code, _out, _ = run_cli(
            "compare", "--baseline", self.path("base.mp4"),
            "--candidate", self.path("candidate.mkv"), "--db", str(db))
        self.assertEqual(code, EXIT_MISMATCH)
        with open_store(db) as store:
            self.assertEqual(store.count_files(), 2)
            self.assertEqual(store.count_validations(), 1)
            store.integrity_check()
            sha = store.conn.execute(
                "SELECT sha256 FROM files WHERE path LIKE '%base.mp4'"
            ).fetchone()[0]
            record = store.get_file(sha)
            self.assertIsNotNone(record)
            self.assertEqual(len(record["tracks"]), 2)

    def test_repeat_run_updates_without_duplicating(self) -> None:
        db = self.tmp / "repeat.sqlite"
        if db.exists():
            db.unlink()
        for _ in range(2):
            run_cli("probe", "--input", self.path("base.mp4"),
                    "--db", str(db))
        with open_store(db) as store:
            self.assertEqual(store.count_files(), 1)
            rows = store.conn.execute("SELECT COUNT(*) FROM tracks").fetchone()
            self.assertEqual(rows[0], 2)

    def test_failed_probe_leaves_no_tracks(self) -> None:
        db = self.tmp / "failed.sqlite"
        if db.exists():
            db.unlink()
        code, _out, err = run_cli(
            "probe", "--input", self.path("truncated.mp4"), "--db", str(db))
        self.assertEqual(code, EXIT_INPUT)
        self.assertIn("error:", err)
        with open_store(db) as store:
            record = store.conn.execute(
                "SELECT fatal, media_json FROM files").fetchone()
            self.assertEqual(record["fatal"], 1)
            count = store.conn.execute("SELECT COUNT(*) FROM tracks").fetchone()
            self.assertEqual(count[0], 0)
            self.assertEqual(store.count_validations(), 0)

    def test_corrupt_database_exits_three(self) -> None:
        db = self.tmp / "corrupt.sqlite"
        db.write_bytes(b"this is definitely not a sqlite database" * 4)
        code, _out, err = run_cli(
            "compare", "--baseline", self.path("base.mp4"),
            "--candidate", self.path("candidate.mkv"), "--db", str(db))
        self.assertEqual(code, EXIT_STORE)
        self.assertIn("cannot open database", err)

    def test_missing_database_directory_exits_three(self) -> None:
        db = self.tmp / "no_such_dir" / "x.sqlite"
        code, _out, err = run_cli(
            "probe", "--input", self.path("base.mp4"), "--db", str(db))
        self.assertEqual(code, EXIT_STORE)
        self.assertIn("database directory does not exist", err)


class PackCliTest(CliFixtureCase):
    def test_pack_manifest(self) -> None:
        db = self.tmp / "pack.sqlite"
        pack = self.tmp / "pack.json"
        for target in (db, pack):
            if target.exists():
                target.unlink()
        code, out, _ = run_cli(
            "pack", "--input", self.path("base.mp4"),
            "--db", str(db), "--json", str(pack))
        self.assertEqual(code, EXIT_OK)
        self.assertIn("track_count: 2", out)
        manifest = json.loads(pack.read_text())
        self.assertEqual(manifest["status"], "ok")
        self.assertEqual(manifest["track_count"], 2)
        self.assertEqual(len(manifest["files"]), 1)
        file_entry = manifest["files"][0]
        self.assertEqual(len(file_entry["sha256"]), 64)
        self.assertEqual([t["codec"] for t in file_entry["tracks"]],
                         ["avc1", "mp4a"])
        self.assertEqual(manifest["blocked"], [])

    def test_pack_blocks_failed_probe(self) -> None:
        target = self.out("blocked.json")
        code, _out, err = run_cli(
            "pack", "--input", self.path("truncated.mp4"),
            "--json", str(target))
        self.assertEqual(code, EXIT_INPUT)
        self.assertIn("could not be probed", err)
        manifest = json.loads(target.read_text())
        self.assertEqual(manifest["status"], "blocked")
        self.assertTrue(manifest["blocked"])
        self.assertFalse(manifest["allow_issues"])

    def test_pack_allow_issues_emits_diagnostic_manifest(self) -> None:
        target = self.out("diagnostic.json")
        code, _out, _ = run_cli(
            "pack", "--input", self.path("truncated.mp4"),
            "--json", str(target), "--allow-issues")
        self.assertEqual(code, EXIT_INPUT)
        manifest = json.loads(target.read_text())
        self.assertTrue(manifest["allow_issues"])
        self.assertEqual(manifest["status"], "diagnostic")
        self.assertTrue(manifest["blocked"])
        self.assertTrue(manifest["files"][0]["blocking_problems"])

    def test_pack_refuses_baseline_mismatch(self) -> None:
        # candidate.mkv has a codec change and drift against base.mp4, so it
        # must not enter the manifest without --allow-issues.
        target = self.out("mismatch.json")
        code, _out, err = run_cli(
            "pack", "--input", self.path("candidate.mkv"),
            "--baseline", self.path("base.mp4"),
            "--json", str(target))
        self.assertEqual(code, EXIT_MISMATCH)
        self.assertIn("blocked", err)
        manifest = json.loads(target.read_text())
        self.assertEqual(manifest["status"], "blocked")
        self.assertIsNotNone(manifest["files"][0]["validation"])
        kinds = {m["kind"] for m in
                 manifest["files"][0]["validation"]["mismatches"]}
        self.assertIn("codec_change", kinds)

    def test_pack_accepts_matching_baseline(self) -> None:
        target = self.out("match.json")
        code, _out, _ = run_cli(
            "pack", "--input", self.path("base.mp4"),
            "--baseline", self.path("mov_variant.mov"),
            "--json", str(target))
        self.assertEqual(code, EXIT_OK)
        manifest = json.loads(target.read_text())
        self.assertEqual(manifest["status"], "ok")
        self.assertEqual(
            manifest["files"][0]["validation"]["status"], "match")

    def test_pack_unwritable_output_exits_three(self) -> None:
        code, _out, err = run_cli(
            "pack", "--input", self.path("base.mp4"),
            "--json", str(self.tmp / "missing" / "pack.json"))
        self.assertEqual(code, EXIT_STORE)
        self.assertIn("cannot write manifest", err)


class ProbeCliTest(CliFixtureCase):
    def test_probe_summary(self) -> None:
        code, out, _ = run_cli("probe", "--input", self.path("base.mp4"))
        self.assertEqual(code, EXIT_OK)
        self.assertIn("container: mp4", out)
        self.assertIn("track_count: 2", out)

    def test_probe_unknown_container(self) -> None:
        code, _out, err = run_cli("probe", "--input", self.path("unknown.bin"))
        self.assertEqual(code, EXIT_INPUT)
        self.assertIn("sniff.unknown_container", err)


if __name__ == "__main__":
    unittest.main()
