"""End-to-end and unit coverage for parsing, comparison and storage."""

from __future__ import annotations

import contextlib
import io
import json
import sqlite3
import sys
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / "src"))
sys.path.insert(0, str(ROOT / "scripts"))

import make_samples  # noqa: E402
from mediacheck import cli  # noqa: E402
from mediacheck.boxes import parse_box_tree  # noqa: E402
from mediacheck.compare import compare_containers  # noqa: E402
from mediacheck.model import SniffResult, Track  # noqa: E402
from mediacheck.sniff import sniff_file  # noqa: E402
from mediacheck.store import Store, StoreError  # noqa: E402


def run_cli(*argv: str) -> tuple[int, str]:
    stdout, stderr = io.StringIO(), io.StringIO()
    with contextlib.redirect_stdout(stdout), contextlib.redirect_stderr(stderr):
        try:
            code = cli.main(list(argv))
        except SystemExit as exc:  # argparse usage errors
            code = int(exc.code or 0)
    return code, stdout.getvalue() + stderr.getvalue()


class SampleTree(unittest.TestCase):
    def setUp(self) -> None:
        self._tmp = tempfile.TemporaryDirectory()
        self.tmp = Path(self._tmp.name)
        self.samples = self.tmp / "samples"
        make_samples.create_samples(self.samples)

    def tearDown(self) -> None:
        self._tmp.cleanup()


class MatchingTests(SampleTree):
    def test_reordering_is_not_a_mismatch(self) -> None:
        baseline = sniff_file(self.samples / "reordered.mp4")
        actual = sniff_file(self.samples / "reordered.mp4")
        report = compare_containers(baseline, actual, path="reordered.mp4")
        self.assertEqual(report.mismatches, [])

    def test_reorder_against_baseline_is_clean(self) -> None:
        manifest = json.loads((self.samples / "baseline.json").read_text())
        expected = next(
            SniffResult.from_dict(e["record"])
            for e in manifest["files"]
            if e["path"] == "reordered.mp4"
        )
        actual = sniff_file(self.samples / "reordered.mp4")
        report = compare_containers(expected, actual, path="reordered.mp4")
        self.assertEqual(report.mismatches, [])

    def test_ambiguity_is_not_silently_resolved(self) -> None:
        track = Track(1, "audio", "eng", "mp4a", 1000.0, 48000, 2, 16, 4)
        expected = SniffResult("mp4", 100, [track])
        actual = SniffResult("mp4", 100, [track, Track.from_dict(track.to_dict())])
        report = compare_containers(expected, actual, path="dup.mp4")
        codes = {m.code for m in report.mismatches}
        self.assertIn("ambiguous", codes)

    def test_tolerance_boundary_is_consistent(self) -> None:
        expected = SniffResult(
            "wav", 10, [Track(1, "audio", None, "pcm_s16le", 1000.0, 48000, 2, 16, 4)]
        )
        actual = SniffResult(
            "wav", 10, [Track(1, "audio", None, "pcm_s16le", 1000.0 + 80.0, 48000, 2, 16, 4)]
        )
        report = compare_containers(
            expected, actual, path="x.wav", duration_tolerance_ms=80.0
        )
        self.assertEqual([m for m in report.mismatches if m.code == "duration_deviation"], [])
        self.assertEqual(report.duration_deviations_ms, [80.0])

        just_over = compare_containers(
            expected,
            SniffResult("wav", 10, [Track(1, "audio", None, "pcm_s16le", 1080.001, 48000, 2, 16, 4)]),
            path="x.wav",
            duration_tolerance_ms=80.0,
        )
        self.assertTrue(
            any(m.code == "duration_deviation" for m in just_over.mismatches)
        )

    def test_unknown_field_is_not_defaulted(self) -> None:
        expected = SniffResult("wav", 10, [Track(1, "audio", None, "pcm_s16le", 10.0)])
        actual = SniffResult(
            "wav", 10, [Track(1, "audio", None, "pcm_s16le", 10.0, 48000, 2, 16, 4)]
        )
        report = compare_containers(expected, actual, path="x.wav")
        codes = {m.code for m in report.mismatches}
        self.assertIn("unknown_field", codes)

    def test_missing_track_detected(self) -> None:
        audio = Track(1, "audio", "eng", "mp4a", 1000.0, 48000, 2, 16, 4)
        video = Track(1, "video", None, "avc1", 1000.0)
        report = compare_containers(
            SniffResult("mp4", 10, [audio, video]),
            SniffResult("mp4", 10, [audio]),
            path="x.mp4",
        )
        codes = {m.code for m in report.mismatches}
        self.assertIn("track_missing", codes)
        self.assertIn("track_count", codes)

    def test_none_fields_do_not_break_ordering(self) -> None:
        expected = SniffResult("mp4", 10, [Track(1, "video", None, "avc1", 1000.0)])
        actual = SniffResult(
            "mp4",
            10,
            [
                Track(1, "audio", "eng", "mp4a", 1000.0, 48000, 2, 16, 4),
                Track(1, "video", None, "avc1", 1000.0),
            ],
        )
        report = compare_containers(expected, actual, path="x.mp4")
        self.assertEqual(report.path, "x.mp4")

    def test_track_limit_is_locatable(self) -> None:
        inner = b"".join(make_samples._trak_audio("eng", 1.0) for _ in range(65))
        moov = make_samples._box("moov", make_samples._mvhd(1.0) + inner)
        path = self.tmp / "toomany.mp4"
        path.write_bytes(make_samples._ftyp() + moov)
        result = sniff_file(path)
        self.assertIsNotNone(result.error)
        self.assertIn("exceeds limit 64", result.error)


class StoreTests(SampleTree):
    def test_idempotent_reuse_by_hash(self) -> None:
        wav = self.samples / "params.wav"
        result = sniff_file(wav)
        with Store(self.tmp / "db.sqlite") as store:
            first = store.record_scan("abc123", result)
            second = store.record_scan("abc123", result)
            self.assertFalse(first.reused)
            self.assertTrue(second.reused)
            self.assertEqual(first.scan_id, second.scan_id)
            rows = store.conn.execute("SELECT COUNT(*) FROM scan_tracks").fetchone()
            self.assertEqual(rows[0], 1)

    def test_failed_write_leaves_no_partial_scan(self) -> None:
        result = SniffResult(
            "wav",
            10,
            [Track(1, "audio", None, "pcm_s16le", 10.0, 48000, 2, 16, 4)],
        )
        store = Store(self.tmp / "db.sqlite")
        try:
            store.conn.execute("DROP TABLE scan_tracks")
            store.conn.commit()
            with self.assertRaises(StoreError):
                store.record_scan("digest", result)
            rows = store.conn.execute("SELECT COUNT(*) FROM scans").fetchone()
            self.assertEqual(rows[0], 0)
        finally:
            store.close()

    def test_path_baseline_round_trip(self) -> None:
        result = SniffResult("wav", 10, [Track(1, "audio")], declared_duration_ms=1.0)
        with Store(self.tmp / "db.sqlite") as store:
            stored = store.record_scan("hash1", result)
            store.remember_path("a.wav", stored.scan_id)
            self.assertEqual(store.find_scan_by_path("a.wav"), stored.scan_id)
            loaded = store.load_scan(stored.scan_id)
            self.assertEqual(loaded.declared_duration_ms, 1.0)


class CliTests(SampleTree):
    def test_acceptance_compare(self) -> None:
        report = self.tmp / "report.json"
        code, output = run_cli(
            "compare",
            str(self.samples),
            "--db",
            str(self.tmp / "state.sqlite"),
            "--json",
            str(report),
            "--duration-tolerance",
            "0.08",
        )
        self.assertEqual(code, 1)
        self.assertIn("files_scanned: 4", output)
        self.assertIn("tracks_expected: 6", output)
        self.assertIn("tracks_actual: 6", output)
        self.assertIn("duration_deviation_max_ms: 80.0", output)
        payload = json.loads(report.read_text(encoding="utf-8"))
        self.assertEqual(set(payload) & {"files", "tracks", "mismatches", "errors"},
                         {"files", "tracks", "mismatches", "errors"})
        codes = {m["code"] for m in payload["mismatches"]}
        self.assertIn("truncated", codes)
        self.assertIn("parameter_drift", codes)
        self.assertEqual(payload["summary"]["mismatches"], 4)

    def test_pack_rejects_truncated(self) -> None:
        out = self.tmp / "package.json"
        code, output = run_cli(
            "pack", str(self.samples), "--out", str(out)
        )
        self.assertEqual(code, 1)
        self.assertIn("packed_files: 3", output)
        self.assertIn("rejected_files: 1", output)
        payload = json.loads(out.read_text(encoding="utf-8"))
        self.assertEqual(payload["rejected"][0]["path"], "truncated.mp4")

    def test_bad_argument_exit_code(self) -> None:
        code, output = run_cli(
            "compare", str(self.samples), "--duration-tolerance", "-1"
        )
        self.assertEqual(code, 2)
        self.assertIn("argument error", output)

    def test_missing_input_exit_code(self) -> None:
        code, _output = run_cli("compare", str(self.tmp / "nope"))
        self.assertEqual(code, 2)

    def test_storage_failure_exit_code(self) -> None:
        bad_db = self.tmp / "not-a-dir" / "state.sqlite"
        bad_db.parent.mkdir()
        bad_db.write_text("not a database", encoding="utf-8")
        # A file where the directory should be makes sqlite fail to open.
        blocker = self.tmp / "blocker"
        blocker.write_text("x")
        code, output = run_cli(
            "compare",
            str(self.samples),
            "--db",
            str(blocker / "state.sqlite"),
        )
        self.assertEqual(code, 3)
        self.assertIn("storage error", output)

    def test_unwritable_json_exit_code(self) -> None:
        code, output = run_cli(
            "compare",
            str(self.samples),
            "--db",
            str(self.tmp / "state.sqlite"),
            "--json",
            "/System/mediacheck-report.json",
        )
        self.assertEqual(code, 3)
        self.assertIn("storage error", output)

    def test_json_is_utf8_and_atomic(self) -> None:
        report = self.tmp / "report.json"
        report.write_text("stale", encoding="utf-8")
        code, _ = run_cli(
            "compare",
            str(self.samples),
            "--db",
            str(self.tmp / "state.sqlite"),
            "--json",
            str(report),
            "--duration-tolerance",
            "0.08",
        )
        self.assertEqual(code, 1)
        text = report.read_text(encoding="utf-8")
        self.assertTrue(text.endswith("\n"))
        self.assertNotIn("stale", text)
        leftovers = [p for p in self.tmp.iterdir() if p.name.startswith(".report")]
        self.assertEqual(leftovers, [])

    def test_scan_continues_past_corrupt_file(self) -> None:
        broken = self.samples / "broken.mp4"
        broken.write_bytes(b"not really an mp4")
        code, output = run_cli(
            "scan", str(self.samples), "--db", str(self.tmp / "state.sqlite")
        )
        self.assertEqual(code, 1)
        self.assertIn("files_scanned: 5", output)
        self.assertIn("broken.mp4", output)


if __name__ == "__main__":
    unittest.main()
