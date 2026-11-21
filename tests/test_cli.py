"""CLI-level tests: exit codes and terminal output."""

from __future__ import annotations

import contextlib
import io
import os
import tempfile
import unittest
from pathlib import Path

from abrscan.cli import main

BASE = [
    "--db", ".runs/t.sqlite",
    "--fixture", "moving-texture",
    "--targets-kbps", "80,160,320,320,640",
]


def _invoke(args):
    out, err = io.StringIO(), io.StringIO()
    with contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
        code = main(args)
    return code, out.getvalue(), err.getvalue()


class CliTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.previous = Path.cwd()
        os.chdir(self.tmp.name)

    def tearDown(self):
        os.chdir(self.previous)
        self.tmp.cleanup()

    def test_interrupted_then_resumed_then_verified(self):
        code, out, _ = _invoke(
            ["run", *BASE, "--workers", "1", "--stop-after", "6"]
        )
        self.assertEqual(code, 75)
        self.assertIn("completed=6", out)
        self.assertIn("interrupted=true", out)

        code, out, _ = _invoke(["run", *BASE, "--workers", "2"])
        self.assertEqual(code, 0)
        self.assertIn("resumed=true", out)
        self.assertIn("completed=12", out)
        self.assertIn("duplicate_targets=320", out)
        self.assertIn("status=completed", out)

        code, out, _ = _invoke(["verify", "--db", ".runs/t.sqlite"])
        self.assertEqual(code, 0)
        self.assertIn("rung=1", out)
        self.assertIn("actual_kbps=", out)
        self.assertIn("quality_db=", out)
        self.assertIn("monotonicity=PASS", out)
        self.assertIn("duplicate_targets=320", out)

    def test_bad_parameters_exit_2(self):
        for extra in (
            ["--targets-kbps", "0"],
            ["--targets-kbps", "80", "--quant-steps", "0"],
            ["--targets-kbps", "80", "--block-sizes", "7"],
            ["--targets-kbps", "80", "--workers", "0"],
            ["--targets-kbps", "80", "--stop-after", "0"],
        ):
            code, _, err = _invoke(["run", "--db", "x.sqlite", *extra])
            self.assertEqual(code, 2, msg=str(extra))
            self.assertIn("invalid_argument", err)

    def test_db_outside_workspace_exit_2(self):
        code, _, err = _invoke(
            ["run", "--db", "/tmp/outside.sqlite", "--targets-kbps", "80"]
        )
        self.assertEqual(code, 2)
        self.assertIn("inside the workspace", err)

    def test_incomplete_scan_cannot_be_verified(self):
        code, _, _ = _invoke(
            ["run", *BASE, "--workers", "1", "--stop-after", "3"]
        )
        self.assertEqual(code, 75)
        code, _, err = _invoke(["verify", "--db", ".runs/t.sqlite"])
        self.assertEqual(code, 3)
        self.assertIn("incomplete_scan", err)

    def test_verify_missing_database_exit_3(self):
        code, _, err = _invoke(["verify", "--db", ".runs/missing.sqlite"])
        self.assertEqual(code, 3)
        self.assertIn("database_not_found", err)

    def test_infeasible_targets_are_reported(self):
        code, _, _ = _invoke(
            [
                "run", "--db", ".runs/low.sqlite",
                "--targets-kbps", "1,320",
                "--quant-steps", "200",
                "--block-sizes", "16",
            ]
        )
        self.assertEqual(code, 0)
        # One target still has a rung, so the ladder is valid but partial.
        code, out, _ = _invoke(["verify", "--db", ".runs/low.sqlite"])
        self.assertEqual(code, 0)
        self.assertIn("infeasible_targets=1", out)
        self.assertIn("monotonicity=PASS", out)

    def test_all_targets_infeasible_exit_3(self):
        code, out, _ = _invoke(
            [
                "run", "--db", ".runs/tiny.sqlite",
                "--targets-kbps", "1",
                "--quant-steps", "200",
                "--block-sizes", "16",
            ]
        )
        self.assertEqual(code, 3)
        self.assertIn("status=completed", out)
        code, out, err = _invoke(["verify", "--db", ".runs/tiny.sqlite"])
        self.assertEqual(code, 3)
        self.assertIn("infeasible_targets=1", out)
        self.assertIn("no_feasible_rungs", err)


if __name__ == "__main__":
    unittest.main()
