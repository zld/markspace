"""End-to-end tests: fixture generation, validation, rebuild, exit codes."""
import json
import os
import tempfile
import unittest
from contextlib import redirect_stderr, redirect_stdout
from io import StringIO

from fragcheck.cli import main
from fragcheck.fixture import build_fixture


def run_cli(argv):
    out, err = StringIO(), StringIO()
    with redirect_stdout(out), redirect_stderr(err):
        code = main(argv)
    return code, out.getvalue(), err.getvalue()


class Pipeline(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.dir = self.tmp.name

    def tearDown(self):
        self.tmp.cleanup()

    def make(self, name, corrupt=None):
        path = os.path.join(self.dir, name)
        with open(path, "wb") as fh:
            fh.write(build_fixture(corrupt))
        return path

    def inspect_json(self, path):
        code, out, _ = run_cli(["inspect", path, "--json"])
        return code, json.loads(out)

    def test_valid_fixture_inspect(self):
        path = self.make("valid.mp4")
        code, report = self.inspect_json(path)
        self.assertEqual(code, 0)
        self.assertTrue(report["sidx_offset_valid"])
        self.assertEqual(len(report["fragments"]), 3)
        by_id = {t["track_id"]: t for t in report["tracks"]}
        self.assertEqual(by_id[1]["sample_entries"], 120)
        self.assertEqual(by_id[1]["timescale"], 15360)
        self.assertEqual(by_id[2]["sample_entries"], 96)
        self.assertEqual(by_id[2]["timescale"], 48000)
        for f in report["fragments"]:
            self.assertEqual(f["end_offset"] - f["start_offset"],
                             f["byte_length"])

    def test_valid_fixture_rebuild_is_idempotent(self):
        path = self.make("valid.mp4")
        out_path = os.path.join(self.dir, "out.mp4")
        code, out, _ = run_cli(["rebuild", path, "--out", out_path, "--json"])
        self.assertEqual(code, 0)
        summary = json.loads(out)
        self.assertTrue(summary["rebuilt"])
        self.assertTrue(summary["sidx_offset_valid"])
        self.assertTrue(summary["output_sidx_offset_valid"])
        self.assertEqual(len(summary["fragments"]), 3)
        with open(path, "rb") as fh:
            original = fh.read()
        with open(out_path, "rb") as fh:
            self.assertEqual(fh.read(), original)

    def test_rebuild_refuses_to_overwrite_input(self):
        path = self.make("valid.mp4")
        code, _, err = run_cli(["rebuild", path, "--out", path])
        self.assertEqual(code, 2)
        self.assertIn("read-only", err)

    def test_missing_sidx_rebuilds_clean(self):
        path = self.make("no-sidx.mp4", "no-sidx")
        code, report = self.inspect_json(path)
        self.assertEqual(code, 3)
        self.assertFalse(report["sidx_offset_valid"])
        out_path = os.path.join(self.dir, "fixed.mp4")
        code, _, _ = run_cli(["rebuild", path, "--out", out_path])
        self.assertEqual(code, 0)
        code, report = self.inspect_json(out_path)
        self.assertEqual((code, report["sidx_offset_valid"]), (0, True))

    def test_bad_sidx_offset_rebuilds_clean(self):
        path = self.make("bad-offset.mp4", "sidx-offset")
        code, report = self.inspect_json(path)
        self.assertEqual((code, report["sidx_offset_valid"]), (3, False))
        out_path = os.path.join(self.dir, "fixed.mp4")
        code, out, _ = run_cli(["rebuild", path, "--out", out_path, "--json"])
        self.assertEqual(code, 0)
        summary = json.loads(out)
        self.assertFalse(summary["sidx_offset_valid"])   # original was bad
        self.assertTrue(summary["output_sidx_offset_valid"])

    def test_overlapping_references_stop_repair(self):
        path = self.make("dup.mp4", "sidx-dup")
        code, _, err = run_cli(["rebuild", path,
                                "--out", os.path.join(self.dir, "o.mp4")])
        self.assertEqual(code, 3)
        self.assertIn("overlap", err)

    def test_contradictory_sample_counts_stop_repair(self):
        path = self.make("counts.mp4", "stts-count")
        code, report = self.inspect_json(path)
        self.assertEqual(code, 3)
        self.assertIn("sample count mismatch",
                      " ".join(report["tracks"][0]["errors"]))
        code, _, err = run_cli(["rebuild", path,
                                "--out", os.path.join(self.dir, "o.mp4")])
        self.assertEqual(code, 3)
        self.assertIn("sample count mismatch", err)

    def test_structural_errors_exit_2(self):
        for corrupt in ("truncate", "bad-box-size"):
            path = self.make(f"{corrupt}.mp4", corrupt)
            code, _, err = run_cli(["inspect", path])
            self.assertEqual(code, 2, corrupt)
            self.assertIn("error:", err)

    def test_unclosed_fragment_reported_but_inspect_ok(self):
        path = self.make("unclosed.mp4", "unclosed")
        code, report = self.inspect_json(path)
        self.assertEqual(code, 0)
        self.assertTrue(any("unclosed" in w for w in report["warnings"]))
        self.assertFalse(report["fragments"][-1]["closed"])
        code, _, err = run_cli(["rebuild", path,
                                "--out", os.path.join(self.dir, "o.mp4")])
        self.assertEqual(code, 3)
        self.assertIn("unclosed", err)

    def test_orphan_mdat_stops_repair(self):
        path = self.make("orphan.mp4", "orphan-mdat")
        code, _, err = run_cli(["rebuild", path,
                                "--out", os.path.join(self.dir, "o.mp4")])
        self.assertEqual(code, 3)
        self.assertIn("uniquely attributed", err)

    def test_timestamp_rollback_located(self):
        path = self.make("rollback.mp4", "tfdt-rollback")
        code, report = self.inspect_json(path)
        self.assertEqual(code, 3)
        self.assertTrue(any("rollback" in e and "fragment 2" in e
                            for e in report["errors"]))

    def test_timestamp_overflow_located(self):
        path = self.make("overflow.mp4", "sidx-overflow")
        code, report = self.inspect_json(path)
        self.assertEqual(code, 3)
        self.assertTrue(any("overflow" in e and "offset" in e
                            for e in report["errors"]))

    def test_non_bmff_input_exit_2(self):
        path = os.path.join(self.dir, "junk.bin")
        with open(path, "wb") as fh:
            fh.write(b"\x00" * 64)
        code, _, _ = run_cli(["inspect", path])
        self.assertEqual(code, 2)

    def test_missing_file_exit_2(self):
        code, _, _ = run_cli(["inspect", os.path.join(self.dir, "nope.mp4")])
        self.assertEqual(code, 2)

    def test_fixture_subcommand(self):
        path = os.path.join(self.dir, "gen.mp4")
        code, out, _ = run_cli(["fixture", "--out", path])
        self.assertEqual(code, 0)
        self.assertTrue(os.path.getsize(path) > 0)
        code, report = self.inspect_json(path)
        self.assertEqual(code, 0)

    def test_text_summary_on_terminal(self):
        path = self.make("valid.mp4")
        code, out, _ = run_cli(["inspect", path])
        self.assertEqual(code, 0)
        self.assertIn("sample_entries=120", out)
        self.assertIn("offset_valid=true", out)


if __name__ == "__main__":
    unittest.main()
