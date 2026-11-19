"""End-to-end tests that drive the same public CLI the demo uses."""

from __future__ import annotations

import json
import math
import re
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

import numpy as np
import soundfile as sf

ROOT = Path(__file__).resolve().parents[1]
FIXTURES = Path(__file__).resolve().parent / "fixtures"
sys.path.insert(0, str(ROOT / "scripts"))
import make_fixtures  # noqa: E402

REQUIRED_STATS = (
    "rms_before",
    "rms_after",
    "lufs_before",
    "lufs_after",
    "clipped_samples_before",
    "clipped_samples_after",
)


def run_cli(*args: str, cwd: Path | None = None) -> subprocess.CompletedProcess:
    return subprocess.run(
        [sys.executable, "-m", "audiopipe.cli", *args],
        cwd=str(cwd or ROOT),
        capture_output=True,
        text=True,
        env={"PYTHONPATH": str(ROOT / "src"), "PATH": "/usr/bin:/bin"},
    )


class FixtureCase(unittest.TestCase):
    """One generated input tree per test class; no external audio."""

    @classmethod
    def setUpClass(cls) -> None:
        cls._tmp = tempfile.TemporaryDirectory()
        cls.tmp = Path(cls._tmp.name)
        cls.input = cls.tmp / "input"
        cls.input.mkdir()
        make_fixtures.make_fixtures(cls.input)
        cls.invalid = cls.tmp / "invalid"
        make_fixtures.invalid_fixtures(cls.invalid)

    @classmethod
    def tearDownClass(cls) -> None:
        cls._tmp.cleanup()

    def batch(self, *extra: str, out: str = "out", input_dir: Path | None = None):
        output = self.tmp / out
        report = self.tmp / f"{out}-report.json"
        args = [
            "batch",
            "--input",
            str(input_dir or self.input),
            "--output",
            str(output),
            "--report",
            str(report),
            *extra,
        ]
        return run_cli(*args), output, report


class HappyPathTests(FixtureCase):
    def test_demo_command_shape(self) -> None:
        result, output, report = self.batch(
            "--rate",
            "48000",
            "--channels",
            "preserve",
            "--target-lufs",
            "-18",
            "--max-peak-dbfs",
            "-1",
            "--jobs",
            "2",
        )
        self.assertEqual(result.returncode, 0, result.stderr)
        document = json.loads(report.read_text())
        self.assertEqual(document["parameters"]["sample_rate"], 48000)
        self.assertEqual(document["parameters"]["channels"], "preserve")
        self.assertEqual(document["counts"]["failed"], 0)
        for entry in document["files"]:
            self.assertTrue(entry["ok"], entry)
            params, stats = entry["params"], entry["stats"]
            self.assertEqual(params["sample_rate"], 48000)
            self.assertEqual(params["channels"], params["channels_in"])
            self.assertIsInstance(params["frames_in"], int)
            self.assertIsInstance(params["frames_out"], int)
            self.assertEqual(params["frames_out"], params["frames_out_expected"])
            for key in REQUIRED_STATS:
                self.assertIn(key, stats)
            for value in stats.values():
                if isinstance(value, float):
                    self.assertTrue(math.isfinite(value))
            self.assertGreaterEqual(stats["clipped_samples_before"], 0)
            self.assertGreaterEqual(stats["clipped_samples_after"], 0)
        # Every output file exists and is readable at the target rate.
        for entry in document["files"]:
            target = output / entry["output"]
            info = sf.info(str(target))
            self.assertEqual(info.samplerate, 48000)
            self.assertEqual(info.frames, entry["params"]["frames_out"])
            self.assertEqual(info.channels, entry["params"]["channels"])

    def test_terminal_output_has_required_fields(self) -> None:
        result, _, _ = self.batch("--rate", "48000", "--target-lufs", "-18")
        self.assertEqual(result.returncode, 0, result.stderr)
        for token in (
            "sample_rate=48000",
            "channels=",
            "frames_in=",
            "frames_out=",
            "rms_before=",
            "rms_after=",
            "lufs_before=",
            "lufs_after=",
            "clipped_samples_before=",
            "clipped_samples_after=",
        ):
            self.assertIn(token, result.stdout)

    def test_report_has_no_absolute_paths(self) -> None:
        result, _, report = self.batch("--rate", "48000", "--target-lufs", "-18")
        self.assertEqual(result.returncode, 0, result.stderr)
        blob = report.read_text()
        self.assertNotIn(str(self.tmp), blob)
        self.assertNotIn("/Users/", blob)
        for entry in json.loads(blob)["files"]:
            self.assertFalse(Path(entry["input"]).is_absolute())
            if entry["output"] is not None:
                self.assertFalse(Path(entry["output"]).is_absolute())

    def test_report_order_is_stable_and_relative(self) -> None:
        _, _, report = self.batch("--rate", "48000")
        files = json.loads(report.read_text())["files"]
        names = [entry["input"] for entry in files]
        self.assertEqual(names, sorted(names))
        self.assertIn("nested/deep/tone_48000.wav", names)

    def test_rerun_is_idempotent(self) -> None:
        first, output, _ = self.batch("--rate", "48000", "--target-lufs", "-18", out="rerun")
        self.assertEqual(first.returncode, 0, first.stderr)
        snapshot = {p.name: p.read_bytes() for p in output.rglob("*.wav")}
        second = run_cli(
            "batch",
            "--input",
            str(self.input),
            "--output",
            str(output),
            "--rate",
            "48000",
            "--target-lufs",
            "-18",
        )
        self.assertEqual(second.returncode, 0, second.stderr)
        after = {p.name: p.read_bytes() for p in output.rglob("*.wav")}
        self.assertEqual(snapshot, after)
        # No temp files left behind by the atomic write path.
        self.assertEqual(list(output.rglob(".*tmp*")), [])

    def test_output_tree_is_not_rescanned(self) -> None:
        nested_out = self.input / "generated"
        result = run_cli(
            "batch",
            "--input",
            str(self.input),
            "--output",
            str(nested_out),
            "--rate",
            "48000",
            "--report",
            str(self.tmp / "nested-report.json"),
        )
        self.assertEqual(result.returncode, 0, result.stderr)
        first = json.loads((self.tmp / "nested-report.json").read_text())["counts"]["total"]
        result2 = run_cli(
            "batch",
            "--input",
            str(self.input),
            "--output",
            str(nested_out),
            "--rate",
            "48000",
            "--report",
            str(self.tmp / "nested-report.json"),
        )
        self.assertEqual(result2.returncode, 0, result2.stderr)
        second = json.loads((self.tmp / "nested-report.json").read_text())["counts"]["total"]
        self.assertEqual(first, second)


class LoudnessTests(FixtureCase):
    def test_target_lufs_is_reached(self) -> None:
        _, _, report = self.batch("--rate", "48000", "--target-lufs", "-18", out="lufs")
        for entry in json.loads(report.read_text())["files"]:
            stats = entry["stats"]
            if stats["lufs_after"] is None:
                continue
            self.assertAlmostEqual(stats["lufs_after"], -18.0, delta=0.05)

    def test_peak_ceiling_is_respected_and_recorded(self) -> None:
        _, _, report = self.batch(
            "--rate", "48000", "--target-lufs", "-3", "--max-peak-dbfs", "-1", out="peak"
        )
        files = json.loads(report.read_text())["files"]
        for entry in files:
            stats = entry["stats"]
            if stats["peak_dbfs_after"] is not None:
                self.assertLessEqual(stats["peak_dbfs_after"], -1.0 + 1e-6)
            self.assertEqual(stats["clipped_samples_after"], 0)
        limited = [e for e in files if e["params"]["limited_by_peak"]]
        self.assertTrue(limited, "expected at least one peak-limited file")
        self.assertTrue(all(e["params"]["gain_reason"] == "peak_limited" for e in limited))
        self.assertTrue(all(e["reason"] == "peak_limited" for e in limited))

    def test_silence_stays_silent_without_nan(self) -> None:
        _, output, report = self.batch("--rate", "48000", "--target-lufs", "-18", out="silence")
        files = {e["input"]: e for e in json.loads(report.read_text())["files"]}
        for name in ("silence_44100.wav", "empty.wav"):
            entry = files[name]
            self.assertTrue(entry["ok"])
            self.assertEqual(entry["reason"], "silent")
            self.assertEqual(entry["stats"]["lufs_before"], None)
            self.assertEqual(entry["stats"]["lufs_after"], None)
            self.assertEqual(entry["stats"]["rms_before"], 0.0)
            self.assertEqual(entry["stats"]["rms_after"], 0.0)
            self.assertEqual(entry["params"]["gain_db"], 0.0)
            data, _ = sf.read(str(output / entry["output"]), always_2d=True)
            self.assertEqual(data.size, 0 if name == "empty.wav" else data.size)
            if data.size:
                self.assertEqual(float(np.max(np.abs(data))), 0.0)
        self.assertNotIn("NaN", report.read_text())

    def test_full_scale_sine_is_attenuated(self) -> None:
        _, _, report = self.batch("--rate", "48000", "--target-lufs", "-18", out="full")
        files = {e["input"]: e for e in json.loads(report.read_text())["files"]}
        entry = files["full_scale_sine.wav"]
        self.assertLess(entry["params"]["gain_db"], 0.0)
        self.assertAlmostEqual(entry["stats"]["lufs_after"], -18.0, delta=0.05)
        self.assertLess(entry["stats"]["rms_after"], entry["stats"]["rms_before"])
        self.assertEqual(entry["stats"]["clipped_samples_before"], 0)
        self.assertEqual(entry["stats"]["clipped_samples_after"], 0)

    def test_clipping_is_counted_before_normalisation(self) -> None:
        # A full-scale square wave overshoots when band-limited, so the
        # pre-gain signal momentarily exceeds 0 dBFS.
        _, _, report = self.batch(
            "--rate", "44100", "--target-lufs", "-18", "--max-peak-dbfs", "-1", out="clip"
        )
        entry = {
            e["input"]: e for e in json.loads(report.read_text())["files"]
        }["square_48000.wav"]
        self.assertGreater(entry["stats"]["clipped_samples_before"], 0)
        self.assertEqual(entry["stats"]["clipped_samples_after"], 0)


class ScalingTests(FixtureCase):
    def test_non_integer_ratio_keeps_duration(self) -> None:
        _, _, report = self.batch("--rate", "48000", out="dur")
        for entry in json.loads(report.read_text())["files"]:
            params = entry["params"]
            # Frame counts are rounded, so duration differs by < 1 output frame.
            tolerance = 1.0 / params["sample_rate"]
            self.assertLessEqual(
                abs(params["duration_in_s"] - params["duration_out_s"]), tolerance
            )
            expected = int(
                math.floor(params["frames_in"] * params["sample_rate"] / params["sample_rate_in"] + 0.5)
            )
            self.assertLessEqual(abs(params["frames_out"] - expected), 0)

    def test_short_tail_frames_match(self) -> None:
        _, output, report = self.batch("--rate", "48000", out="tail")
        files = {e["input"]: e for e in json.loads(report.read_text())["files"]}
        entry = files["short_tail.wav"]
        self.assertEqual(entry["params"]["frames_in"], 333)
        self.assertEqual(entry["params"]["frames_out"], 500)
        info = sf.info(str(output / entry["output"]))
        self.assertEqual(info.frames, 500)

    def test_channels_preserved_in_order(self) -> None:
        _, output, _ = self.batch("--rate", "48000", out="order")
        target, _ = sf.read(str(output / "sweep_44100.wav"), always_2d=True)
        self.assertEqual(target.shape[1], 2)
        # Fixture left channel is 0.5 amplitude, right is 0.3: a swap is visible.
        self.assertAlmostEqual(float(np.abs(target[:, 0]).max()), 0.5, delta=0.02)
        self.assertAlmostEqual(float(np.abs(target[:, 1]).max()), 0.3, delta=0.02)

    def test_mono_mode_averages(self) -> None:
        _, output, report = self.batch("--rate", "48000", "--channels", "mono", out="mono")
        for entry in json.loads(report.read_text())["files"]:
            self.assertEqual(entry["params"]["channels"], 1)
        info = sf.info(str(output / "stereo_44100.wav"))
        self.assertEqual(info.channels, 1)
        info = sf.info(str(output / "multi_8ch_44100.wav"))
        self.assertEqual(info.channels, 1)

    def test_exact_channel_mismatch_fails_file_only(self) -> None:
        result, _, report = self.batch("--rate", "48000", "--channels", "2", out="exact")
        self.assertEqual(result.returncode, 1)
        files = json.loads(report.read_text())["files"]
        self.assertTrue(any(e["ok"] for e in files))
        failed = [e for e in files if not e["ok"]]
        self.assertTrue(failed)
        for entry in failed:
            self.assertEqual(entry["reason"], "channel_mismatch")

    def test_custom_block_size_gives_same_output(self) -> None:
        _, out_a, _ = self.batch("--rate", "48000", "--block-size", "97", out="blocka")
        _, out_b, _ = self.batch("--rate", "48000", "--block-size", "8192", out="blockb")
        for name in ("short_tail.wav", "stereo_44100.wav"):
            a, _ = sf.read(str(out_a / name), always_2d=True)
            b, _ = sf.read(str(out_b / name), always_2d=True)
            np.testing.assert_allclose(a, b, atol=0)


class FailureTests(FixtureCase):
    def test_corrupt_inputs_fail_without_stopping_batch(self) -> None:
        result, _, report = self.batch(
            "--rate", "48000", "--target-lufs", "-18", out="bad", input_dir=self.invalid
        )
        self.assertEqual(result.returncode, 1)
        document = json.loads(report.read_text())
        self.assertEqual(document["counts"]["failed"], 3)
        reasons = {entry["input"]: entry["reason"] for entry in document["files"]}
        self.assertEqual(reasons["corrupt.wav"], "corrupt")
        self.assertEqual(reasons["garbage.wav"], "corrupt")
        self.assertEqual(reasons["float_encoding.wav"], "unsupported_encoding")
        for entry in document["files"]:
            self.assertFalse(entry["ok"])
            self.assertIsNotNone(entry["error"])
            self.assertNotIn(str(self.tmp), json.dumps(entry))

    def test_failure_is_isolated_with_parallel_jobs(self) -> None:
        mixed = self.tmp / "mixed"
        mixed.mkdir()
        make_fixtures.make_fixtures(mixed)
        for path in self.invalid.iterdir():
            (mixed / path.name).write_bytes(path.read_bytes())
        result, output, report = self.batch(
            "--rate", "48000", "--jobs", "3", out="mixed", input_dir=mixed
        )
        self.assertEqual(result.returncode, 1)
        document = json.loads(report.read_text())
        self.assertGreater(document["counts"]["succeeded"], 0)
        self.assertEqual(document["counts"]["failed"], 3)
        self.assertEqual(len(document["failures"]), 3)
        self.assertTrue(output.exists())
        self.assertEqual(list(output.rglob(".*tmp*")), [])

    def test_usage_errors_exit_two(self) -> None:
        cases = [
            ("--rate", "0"),
            ("--rate", "48000", "--channels", "nope"),
            ("--rate", "48000", "--target-lufs", "12"),
            ("--rate", "48000", "--max-peak-dbfs", "3"),
            ("--rate", "48000", "--jobs", "0"),
            ("--rate", "48000", "--block-size", "0"),
            ("--rate", "48000", "--rolloff", "2"),
            ("--rate", "999999"),
        ]
        for extra in cases:
            with self.subTest(extra=extra):
                result, _, _ = self.batch(*extra, out="usage")
                self.assertEqual(result.returncode, 2, (extra, result.stderr))

    def test_missing_input_exits_two(self) -> None:
        result = run_cli(
            "batch",
            "--input",
            str(self.tmp / "does-not-exist"),
            "--output",
            str(self.tmp / "nope"),
            "--rate",
            "48000",
        )
        self.assertEqual(result.returncode, 2)
        self.assertIn("missing_input", result.stderr)

    def test_single_file_mode(self) -> None:
        source = self.input / "mono_22050.wav"
        result = run_cli(
            "batch",
            "--input",
            str(source),
            "--output",
            str(self.tmp / "single"),
            "--rate",
            "48000",
            "--report",
            str(self.tmp / "single.json"),
        )
        self.assertEqual(result.returncode, 0, result.stderr)
        document = json.loads((self.tmp / "single.json").read_text())
        self.assertEqual(document["counts"]["total"], 1)
        self.assertEqual(document["files"][0]["input"], "mono_22050.wav")
        self.assertTrue((self.tmp / "single" / "mono_22050.wav").exists())

    def test_rejects_oversized_rate_via_limit(self) -> None:
        # A file claiming 400 kHz is beyond the documented input limit.
        bogus = self.tmp / "highrate"
        bogus.mkdir()
        payload = bytearray(
            b"RIFF\x24\x00\x00\x00WAVEfmt \x10\x00\x00\x00\x01\x00\x01\x00"
            b"\x00\x00\x00\x00\x88\x58\x01\x00\x02\x00\x10\x00data\x00\x04\x00\x00"
        )
        # 400000 Hz little-endian
        payload[24:28] = (400000).to_bytes(4, "little")
        payload += b"\x00" * 1024
        # Patch RIFF size to match.
        payload[4:8] = (len(payload) - 8).to_bytes(4, "little")
        (bogus / "huge.wav").write_bytes(bytes(payload))
        result, _, report = self.batch("--rate", "48000", out="limit", input_dir=bogus)
        self.assertEqual(result.returncode, 1)
        entry = json.loads(report.read_text())["files"][0]
        self.assertIn(entry["reason"], {"sample_rate_limit", "corrupt", "unsupported_encoding"})


class UnitLevelTests(unittest.TestCase):
    def test_cli_exit_code_constants(self) -> None:
        from audiopipe.errors import EXIT_FILE_FAILED, EXIT_OK, EXIT_USAGE

        self.assertEqual((EXIT_OK, EXIT_FILE_FAILED, EXIT_USAGE), (0, 1, 2))

    def test_report_json_shapes(self) -> None:
        from audiopipe.pipeline import FileResult

        result = FileResult("a.wav", "a.wav", True, stats={"rms_before": 0.0})
        record = json.loads(json.dumps(result.as_dict()))
        self.assertEqual(record["status"], "ok")
        self.assertEqual(record["stats"]["rms_before"], 0.0)

    def test_fixture_files_are_deterministic(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            a = Path(tmp) / "a"
            b = Path(tmp) / "b"
            make_fixtures.make_fixtures(a)
            make_fixtures.make_fixtures(b)
            for path in sorted(a.rglob("*.wav")):
                twin = b / path.relative_to(a)
                self.assertEqual(path.read_bytes(), twin.read_bytes(), path.name)


if __name__ == "__main__":
    unittest.main()
