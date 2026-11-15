"""Tests for the command line interface: arguments, output and exit codes."""

from __future__ import annotations

import io
import json
import os
import sys
import tempfile
import unittest
from contextlib import redirect_stderr, redirect_stdout
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "src"))
sys.path.insert(0, str(Path(__file__).resolve().parent))

import fixtures  # noqa: E402
from sidxcheck import cli  # noqa: E402


class CliTests(unittest.TestCase):
    def setUp(self) -> None:
        fd, self.sample_path = tempfile.mkstemp(suffix=".mp4")
        with os.fdopen(fd, "wb") as handle:
            handle.write(fixtures.build_container())
        fd, self.bad_path = tempfile.mkstemp(suffix=".mp4")
        with os.fdopen(fd, "wb") as handle:
            handle.write(fixtures.corrupt_sidx_size_mismatch())

    def tearDown(self) -> None:
        Path(self.sample_path).unlink(missing_ok=True)
        Path(self.bad_path).unlink(missing_ok=True)

    def test_valid_file_returns_zero(self):
        code = cli.run(["--input", self.sample_path])
        self.assertEqual(code, cli.EXIT_OK)

    def test_json_output_has_required_fields(self):
        buf = io.StringIO()
        with redirect_stdout(buf):
            code = cli.run(["--input", self.sample_path, "--json"])
        self.assertEqual(code, cli.EXIT_OK)
        payload = json.loads(buf.getvalue())
        self.assertEqual(payload["sample_entries"], 120)
        self.assertEqual(payload["index_refs"], 4)
        self.assertEqual(payload["offset_checks"], {"passed": 4, "total": 4})
        self.assertEqual(len(payload["rebuilt_segments"]), 4)
        segment = payload["rebuilt_segments"][0]
        self.assertIn("start", segment)
        self.assertIn("end", segment)
        self.assertEqual(payload["diagnostics"], [])

    def test_unreadable_file_returns_usage_code(self):
        err = io.StringIO()
        with redirect_stderr(err):
            code = cli.run(["--input", "/nonexistent/path/media.mp4"])
        self.assertEqual(code, cli.EXIT_USAGE)
        self.assertIn("sidxcheck", err.getvalue())
        self.assertNotIn("Traceback", err.getvalue())

    def test_corrupt_file_returns_corrupt_code_in_strict_mode(self):
        code = cli.run(["--input", self.bad_path, "--strict", "--json"])
        self.assertEqual(code, cli.EXIT_CORRUPT)

    def test_lenient_mode_reports_diagnostics_without_traceback(self):
        buf = io.StringIO()
        with redirect_stdout(buf):
            code = cli.run(["--input", self.bad_path, "--json"])
        payload = json.loads(buf.getvalue())
        self.assertTrue(payload["diagnostics"])
        self.assertTrue(
            any(d["code"] == "reference_size_mismatch"
                for d in payload["diagnostics"])
        )

    def test_no_valid_track_returns_corrupt_code(self):
        fd, empty_path = tempfile.mkstemp(suffix=".mp4")
        try:
            os.write(fd, b"not an iso bmff container at all")
            os.close(fd)
            err = io.StringIO()
            with redirect_stderr(err):
                code = cli.run(["--input", empty_path])
            self.assertEqual(code, cli.EXIT_CORRUPT)
            self.assertNotIn("Traceback", err.getvalue())
        finally:
            Path(empty_path).unlink(missing_ok=True)

    def test_make_sample_writes_and_verifies_round_trip(self):
        fd, target = tempfile.mkstemp(suffix=".mp4")
        os.close(fd)
        Path(target).unlink()
        try:
            code = cli.run(["--make-sample", target])
            self.assertEqual(code, cli.EXIT_OK)
            self.assertTrue(Path(target).is_file())
            verify = cli.run(["--input", target, "--json"])
            self.assertEqual(verify, cli.EXIT_OK)
        finally:
            Path(target).unlink(missing_ok=True)

    def test_make_sample_to_bad_path_returns_usage_code(self):
        err = io.StringIO()
        with redirect_stderr(err):
            code = cli.run(
                ["--make-sample", "/nonexistent/dir/sample.mp4"]
            )
        self.assertEqual(code, cli.EXIT_USAGE)


if __name__ == "__main__":
    unittest.main()
