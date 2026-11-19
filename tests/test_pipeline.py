"""End-to-end batch tests: scanning, processing, reporting and recovery."""

from __future__ import annotations

import json
import os
import shutil
import unittest
from unittest import mock

import numpy as np
import soundfile as sf

from audiopipe import batch
from audiopipe.batch import (
    BatchConfig,
    ConfigError,
    scan_inputs,
    validate_paths,
    verify_report,
)
from audiopipe.cli import main as cli_main
from tests.support import TempDirMixin, demo_fixtures, edge_fixtures, sine, write_wav


def _load(path: str) -> dict:
    with open(path, "r", encoding="utf-8") as fh:
        return json.load(fh)


def _stable(report: dict) -> list[dict]:
    """Records without wall-clock durations (the only non-deterministic field)."""
    return [
        {key: value for key, value in record.items() if key != "duration_seconds"}
        for record in report["files"]
    ]


class PathTests(TempDirMixin, unittest.TestCase):
    def test_recursive_scan_is_sorted_and_posix(self) -> None:
        root = self.path("in")
        write_wav(os.path.join(root, "b.wav"), np.zeros((10, 1)), 48000)
        write_wav(os.path.join(root, "a.WAV"), np.zeros((10, 1)), 48000)
        write_wav(os.path.join(root, "sub", "c.wav"), np.zeros((10, 1)), 48000)
        with open(os.path.join(root, "notes.txt"), "w", encoding="utf-8") as fh:
            fh.write("ignore me")
        self.assertEqual(scan_inputs(root), ["a.WAV", "b.wav", "sub/c.wav"])

    def test_scan_rejects_missing_directory(self) -> None:
        with self.assertRaises(ConfigError):
            scan_inputs(self.path("missing"))

    def test_overlapping_paths_rejected(self) -> None:
        src = self.path("src")
        os.makedirs(src)
        with self.assertRaises(ConfigError):
            validate_paths(src, src)
        with self.assertRaises(ConfigError):
            validate_paths(src, os.path.join(src, "out"))
        with self.assertRaises(ConfigError):
            validate_paths(os.path.join(src, "in"), src)
        validate_paths(src, self.path("out"))

    def test_cli_rejects_nested_paths(self) -> None:
        root = self.path("in")
        write_wav(os.path.join(root, "a.wav"), np.zeros((10, 1)), 48000)
        code = cli_main(
            [
                "normalize", "--input", root, "--output", os.path.join(root, "out"),
                "--report", self.path("r.json"), "--rate", "48000",
            ]
        )
        self.assertEqual(code, 2)
        self.assertFalse(os.path.exists(self.path("r.json")))


class BatchEndToEndTests(TempDirMixin, unittest.TestCase):
    def _config(self, input_dir: str, **overrides) -> BatchConfig:
        options = dict(
            input_dir=input_dir,
            output_dir=self.path("out"),
            report_path=self.path("report.json"),
            output_rate=48000,
            output_subtype="PCM_24",
            target_lufs=-18.0,
            max_gain_db=12.0,
            jobs=1,
        )
        options.update(overrides)
        return BatchConfig(**options)

    def test_demo_batch_contract(self) -> None:
        input_dir = self.path("in")
        demo_fixtures(input_dir)
        report = batch.run_batch(self._config(input_dir))
        self.assertEqual(report["summary"], {"files": 3, "success": 3, "failed": 0})
        for record in report["files"]:
            output = record["output"]
            self.assertEqual(record["status"], "ok")
            self.assertEqual(output["sample_rate"], 48000)
            self.assertGreaterEqual(output["channels"], 1)
            self.assertLessEqual(output["channels"], 8)
            self.assertEqual(output["frames"], record["resample"]["output_frames"])
            self.assertIsInstance(record["padding_frames"], int)
            self.assertGreaterEqual(record["padding_frames"], 0)
            for section in ("before", "after"):
                self.assertGreaterEqual(record[section]["rms"], 0.0)
                self.assertLessEqual(record[section]["rms"], 1.0)
                self.assertIsInstance(record[section]["clipped_samples"], int)
            self.assertLess(abs(record["after"]["lufs"] - (-18.0)), 0.1)
        written = scan_inputs(self.path("out"))
        self.assertEqual(len(written), 3)

    def test_report_is_strict_json_and_round_trips(self) -> None:
        input_dir = self.path("in")
        demo_fixtures(input_dir)
        config = self._config(input_dir)
        report = batch.run_batch(config)
        from audiopipe.report import write_json

        write_json(config.report_path, report)
        with open(config.report_path, "r", encoding="utf-8") as fh:
            text = fh.read()
        for token in ("NaN", "Infinity"):
            self.assertNotIn(token, text)
        json.loads(text)  # standard parser accepts it

    def test_verification_matches_outputs(self) -> None:
        input_dir = self.path("in")
        demo_fixtures(input_dir)
        config = self._config(input_dir)
        report = batch.run_batch(config)
        from audiopipe.report import write_json

        write_json(config.report_path, report)
        document = verify_report(config.report_path)
        self.assertEqual(document["summary"]["mismatched"], 0)
        self.assertEqual(document["summary"]["checked"], 3)

    def test_verification_detects_tampering(self) -> None:
        input_dir = self.path("in")
        demo_fixtures(input_dir)
        config = self._config(input_dir)
        report = batch.run_batch(config)
        report["files"][0]["after"]["rms"] = 0.123456
        from audiopipe.report import write_json

        write_json(config.report_path, report)
        document = verify_report(config.report_path)
        self.assertEqual(document["summary"]["mismatched"], 1)

    def test_output_is_deterministic(self) -> None:
        input_dir = self.path("in")
        demo_fixtures(input_dir)
        first = batch.run_batch(self._config(input_dir, output_dir=self.path("out1")))
        second = batch.run_batch(self._config(input_dir, output_dir=self.path("out2")))
        self.assertEqual(_stable(first), _stable(second))
        for rel in scan_inputs(self.path("out1")):
            with open(os.path.join(self.path("out1"), rel), "rb") as a, open(
                os.path.join(self.path("out2"), rel), "rb"
            ) as b:
                self.assertEqual(a.read(), b.read(), rel)

    def test_jobs_produce_identical_records(self) -> None:
        input_dir = self.path("in")
        demo_fixtures(input_dir)
        serial = batch.run_batch(self._config(input_dir, output_dir=self.path("o1"), jobs=1))
        parallel = batch.run_batch(self._config(input_dir, output_dir=self.path("o2"), jobs=2))
        self.assertEqual(_stable(serial), _stable(parallel))
        self.assertEqual(serial["summary"], parallel["summary"])

    def test_clipping_quantities_are_distinct(self) -> None:
        input_dir = self.path("in")
        edge_fixtures(input_dir)
        config = self._config(input_dir, output_subtype="PCM_16")
        report = batch.run_batch(config)
        records = {r["relative_path"]: r for r in report["files"]}
        hot = records["edge/hot_square.wav"]
        self.assertEqual(hot["status"], "ok")
        # The source sits on both PCM rails.
        self.assertGreater(hot["input"]["rail_clipped_samples"], 4000)
        # Band-limited interpolation overshoots the rails before any gain.
        self.assertGreater(hot["before"]["clipped_samples"], 0)
        self.assertEqual(
            hot["before"]["clipped_samples"], hot["resample"]["overshoot_samples"]
        )
        # Gain planning keeps post-gain samples inside full scale.
        self.assertEqual(hot["output"]["out_of_range_samples"], 0)
        self.assertEqual(hot["after"]["clipped_samples"], 0)
        self.assertLessEqual(hot["after"]["peak"], 1.0)

    def test_many_rate_and_channel_combinations(self) -> None:
        input_dir = self.path("in")
        rng = np.random.default_rng(21)
        combos = [
            (8000, 1), (16000, 2), (22050, 3), (32000, 4),
            (44100, 5), (48000, 8), (96000, 1), (192000, 2),
        ]
        for index, (rate, channels) in enumerate(combos):
            data = rng.standard_normal((997, channels)) * 0.1
            write_wav(os.path.join(input_dir, f"c{index}.wav"), data, rate, "PCM_24")
        config = self._config(input_dir)
        report = batch.run_batch(config)
        self.assertEqual(report["summary"], {"files": len(combos), "success": len(combos), "failed": 0})
        for record in report["files"]:
            info = record["resample"]
            expected_frames = (info["input_frames"] * info["numerator"]) // info["denominator"]
            self.assertEqual(record["output"]["frames"], expected_frames)
            self.assertTrue(np.isfinite(record["after"]["rms"]))
            self.assertGreaterEqual(record["output"]["channels"], 1)
            self.assertLessEqual(record["output"]["channels"], 8)
        from audiopipe.report import write_json

        write_json(config.report_path, report)
        self.assertEqual(verify_report(config.report_path)["summary"]["mismatched"], 0)

    def test_zero_peak_ceiling_still_avoids_rails(self) -> None:
        input_dir = self.path("in")
        write_wav(os.path.join(input_dir, "a.wav"), sine(440, 48000, 4800, 0.3)[:, None], 48000)
        config = BatchConfig(
            input_dir=input_dir,
            output_dir=self.path("out"),
            report_path=self.path("r.json"),
            output_rate=48000,
            target_lufs=3.0,
            max_gain_db=60.0,
            max_peak_db=0.0,
        )
        record = batch.run_batch(config)["files"][0]
        self.assertEqual(record["status"], "ok")
        self.assertEqual(record["gain"]["reason"], "peak_limit")
        self.assertEqual(record["output"]["out_of_range_samples"], 0)
        self.assertEqual(record["after"]["clipped_samples"], 0)
        self.assertLess(record["after"]["peak"], 1.0)

    def test_pcm16_output(self) -> None:
        input_dir = self.path("in")
        demo_fixtures(input_dir)
        config = self._config(input_dir, output_subtype="PCM_16")
        report = batch.run_batch(config)
        self.assertEqual(report["summary"]["failed"], 0)
        for rel in scan_inputs(self.path("out")):
            info = sf.info(os.path.join(self.path("out"), rel))
            self.assertEqual(info.subtype, "PCM_16")


class FailureRecoveryTests(TempDirMixin, unittest.TestCase):
    def test_corrupt_file_does_not_stop_batch(self) -> None:
        input_dir = self.path("in")
        write_wav(os.path.join(input_dir, "good.wav"), sine(440, 48000, 4800)[:, None], 48000)
        os.makedirs(os.path.join(input_dir, "bad"))
        with open(os.path.join(input_dir, "bad", "broken.wav"), "wb") as fh:
            fh.write(b"RIFF\x00\x00\x00\x00WAVEjunk")
        config = BatchConfig(
            input_dir=input_dir,
            output_dir=self.path("out"),
            report_path=self.path("report.json"),
            output_rate=48000,
        )
        report = batch.run_batch(config)
        self.assertEqual(report["summary"], {"files": 2, "success": 1, "failed": 1})
        failed = [r for r in report["files"] if r["status"] == "error"][0]
        self.assertEqual(failed["error"]["code"], "unreadable")
        self.assertEqual(failed["relative_path"], "bad/broken.wav")
        self.assertTrue(os.path.exists(os.path.join(self.path("out"), "good.wav")))
        self.assertFalse(os.path.exists(os.path.join(self.path("out"), "bad", "broken.wav")))

    def test_write_failure_is_isolated_and_cleans_up(self) -> None:
        input_dir = self.path("in")
        write_wav(os.path.join(input_dir, "a.wav"), sine(440, 48000, 4800)[:, None], 48000)
        write_wav(os.path.join(input_dir, "b.wav"), sine(440, 48000, 4800)[:, None], 48000)
        real_write = batch.WavWriter.write

        def failing_write(writer, block):
            if writer.path.endswith("a.wav"):
                raise batch.PipelineError("write_failed", "simulated disk failure")
            return real_write(writer, block)

        config = BatchConfig(
            input_dir=input_dir,
            output_dir=self.path("out"),
            report_path=self.path("report.json"),
            output_rate=48000,
        )
        with mock.patch.object(batch.WavWriter, "write", failing_write):
            report = batch.run_batch(config)
        self.assertEqual(report["summary"], {"files": 2, "success": 1, "failed": 1})
        failed = [r for r in report["files"] if r["status"] == "error"][0]
        self.assertEqual(failed["error"]["code"], "write_failed")
        self.assertFalse(os.path.exists(os.path.join(self.path("out"), "a.wav")))
        self.assertTrue(os.path.exists(os.path.join(self.path("out"), "b.wav")))
        leftovers = [
            name
            for _root, _dirs, names in os.walk(self.path("out"))
            for name in names
            if name.endswith(".part.wav")
        ]
        self.assertEqual(leftovers, [])

    def test_invalid_inputs_are_rejected_individually(self) -> None:
        input_dir = self.path("in")
        edge_fixtures(input_dir)
        config = BatchConfig(
            input_dir=input_dir,
            output_dir=self.path("out"),
            report_path=self.path("report.json"),
            output_rate=48000,
            output_subtype="PCM_24",
        )
        report = batch.run_batch(config)
        codes = {
            record["relative_path"]: (record["error"] or {}).get("code")
            for record in report["files"]
        }
        self.assertEqual(codes["invalid/nine_channels.wav"], "unsupported_channels")
        self.assertEqual(codes["invalid/float_encoding.wav"], "unsupported_format")
        self.assertEqual(codes["invalid/low_rate.wav"], "invalid_sample_rate")
        self.assertEqual(codes["invalid/corrupt.wav"], "unreadable")
        ok = [r for r in report["files"] if r["status"] == "ok"]
        self.assertGreaterEqual(len(ok), 6)
        for record in ok:
            self.assertIsNone(record["error"])

    def test_empty_and_single_frame_files(self) -> None:
        input_dir = self.path("in")
        write_wav(os.path.join(input_dir, "empty.wav"), np.zeros((0, 2)), 44100)
        write_wav(os.path.join(input_dir, "one.wav"), np.array([[0.3]]), 44100)
        config = BatchConfig(
            input_dir=input_dir,
            output_dir=self.path("out"),
            report_path=self.path("report.json"),
            output_rate=48000,
        )
        report = batch.run_batch(config)
        self.assertEqual(report["summary"]["success"], 2)
        records = {r["relative_path"]: r for r in report["files"]}
        self.assertEqual(records["empty.wav"]["output"]["frames"], 0)
        self.assertEqual(records["empty.wav"]["after"]["rms"], 0.0)
        self.assertEqual(records["one.wav"]["output"]["frames"], 1)
        self.assertTrue(np.isfinite(records["one.wav"]["after"]["rms"]))


class CliExitCodeTests(TempDirMixin, unittest.TestCase):
    def _run(self, *args: str) -> int:
        return cli_main(list(args))

    def test_success_exit_zero(self) -> None:
        input_dir = self.path("in")
        demo_fixtures(input_dir)
        code = self._run(
            "normalize", "--input", input_dir, "--output", self.path("out"),
            "--report", self.path("r.json"), "--rate", "48000", "--jobs", "2",
        )
        self.assertEqual(code, 0)
        self.assertTrue(os.path.exists(self.path("r.json")))

    def test_partial_failure_exit_one(self) -> None:
        input_dir = self.path("in")
        write_wav(os.path.join(input_dir, "good.wav"), sine(440, 48000, 4800)[:, None], 48000)
        with open(os.path.join(input_dir, "bad.wav"), "wb") as fh:
            fh.write(b"not a wav at all")
        code = self._run(
            "normalize", "--input", input_dir, "--output", self.path("out"),
            "--report", self.path("r.json"), "--rate", "48000",
        )
        self.assertEqual(code, 1)
        self.assertTrue(os.path.exists(self.path("r.json")))

    def test_config_error_exit_two(self) -> None:
        self.assertEqual(
            self._run(
                "normalize", "--input", self.path("missing"), "--output", self.path("o"),
                "--report", self.path("r.json"), "--rate", "48000",
            ),
            2,
        )
        self.assertEqual(
            self._run(
                "normalize", "--input", self.path("in"), "--output", self.path("o"),
                "--report", self.path("r.json"), "--rate", "12345",
            ),
            2,
        )

    def test_report_failure_exit_three(self) -> None:
        input_dir = self.path("in")
        write_wav(os.path.join(input_dir, "a.wav"), sine(440, 48000, 4800)[:, None], 48000)
        os.makedirs(self.path("blocked"))
        config_dir = self.path("blocked", "r.json")
        os.makedirs(config_dir)  # a directory where the report file must go
        code = self._run(
            "normalize", "--input", input_dir, "--output", self.path("out"),
            "--report", config_dir, "--rate", "48000",
        )
        self.assertEqual(code, 3)

    def test_verify_command_exit_codes(self) -> None:
        input_dir = self.path("in")
        demo_fixtures(input_dir)
        report_path = self.path("r.json")
        self.assertEqual(
            self._run(
                "normalize", "--input", input_dir, "--output", self.path("out"),
                "--report", report_path, "--rate", "48000",
            ),
            0,
        )
        self.assertEqual(self._run("verify", "--report", report_path), 0)
        self.assertEqual(self._run("verify", "--report", self.path("nope.json")), 2)


if __name__ == "__main__":
    unittest.main()
