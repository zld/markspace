"""End-to-end pipeline, report, verify and CLI contract tests."""

from __future__ import annotations

import json
import unittest

import numpy as np
import soundfile as sf

from audiorepro.cli import main
from audiorepro.fixtures import generate
from audiorepro.params import PipelineParams
from audiorepro.pipeline import run_pipeline
from audiorepro.resample import ResamplerConfig
from audiorepro.verify import verify

from support import TempDirTest


def _params(tmp, **overrides) -> PipelineParams:
    base = dict(
        input_dir=tmp / "input",
        output_dir=tmp / "output",
        sample_rate=48000,
        target_lufs=-23.0,
        peak_dbfs=-1.0,
        jobs=1,
        resume=False,
        chunk_frames=4096,
        resampler=ResamplerConfig(half_width=64, phases=256),
    )
    base.update(overrides)
    return PipelineParams(**base)


class PipelineContractTests(TempDirTest):
    def test_fixture_batch_contract(self) -> None:
        generate(self.tmp / "input", seed=17)
        result = run_pipeline(_params(self.tmp, jobs=2), self.tmp / "report.json")
        self.assertEqual(result.exit_code, 0)
        report = json.loads((self.tmp / "report.json").read_text())
        self.assertEqual(report["schema"], "audiorepro.report/1")
        self.assertEqual(report["summary"]["files"], 6)
        self.assertEqual(report["summary"]["failed"], 0)
        self.assertEqual(report["summary"]["sample_rate"], 48000)
        self.assertEqual(report["summary"]["channels"], {"1": 3, "2": 2, "6": 1})
        for entry in report["files"]:
            for key in (
                "rms_before_dbfs",
                "lufs_before",
                "rms_after_dbfs",
                "lufs_after",
                "clip_samples_before",
                "clip_samples_after",
            ):
                self.assertIn(key, entry)
            self.assertEqual(entry["clip_samples_after"], 0)
            if entry["normalizable"]:
                self.assertLessEqual(entry["peak_after_dbfs"], -1.0 + 1e-3)

    def test_normalizable_files_reach_target(self) -> None:
        generate(self.tmp / "input", seed=17)
        run_pipeline(_params(self.tmp), self.tmp / "report.json")
        report = json.loads((self.tmp / "report.json").read_text())
        normalizable = [f for f in report["files"] if f["normalizable"]]
        self.assertGreaterEqual(len(normalizable), 4)
        for entry in normalizable:
            self.assertAlmostEqual(entry["lufs_after"], -23.0, delta=0.3)

    def test_short_and_silent_are_null(self) -> None:
        generate(self.tmp / "input", seed=17)
        run_pipeline(_params(self.tmp), self.tmp / "report.json")
        report = json.loads((self.tmp / "report.json").read_text())
        short = next(f for f in report["files"] if f["relative_path"].endswith("mono_short.aiff"))
        silent = next(f for f in report["files"] if f["relative_path"].endswith("mono_silent.wav"))
        self.assertIsNone(short["lufs_after"])
        self.assertIsNone(silent["lufs_after"])
        self.assertFalse(short["normalizable"])

    def test_peak_limited_file_is_clip_free(self) -> None:
        generate(self.tmp / "input", seed=17)
        run_pipeline(_params(self.tmp), self.tmp / "report.json")
        report = json.loads((self.tmp / "report.json").read_text())
        hot = next(f for f in report["files"] if f["relative_path"].endswith("stereo_hot.wav"))
        self.assertGreater(hot["clip_samples_before"], 0)
        self.assertEqual(hot["clip_samples_after"], 0)
        self.assertTrue(hot["peak_limited"])

    def test_outputs_are_float_wav_at_target_rate(self) -> None:
        generate(self.tmp / "input", seed=17)
        run_pipeline(_params(self.tmp), self.tmp / "report.json")
        products = sorted((self.tmp / "output").rglob("*.wav"))
        self.assertEqual(len(products), 6)
        for path in products:
            info = sf.info(str(path))
            self.assertEqual(info.samplerate, 48000)
            self.assertEqual(info.format, "WAV")
            self.assertEqual(info.subtype, "FLOAT")

    def test_channel_order_survives_the_pipeline(self) -> None:
        generate(self.tmp / "input", seed=17)
        run_pipeline(_params(self.tmp), self.tmp / "report.json")
        product = self.tmp / "output" / "surround" / "surround_51.wav"
        data, _ = sf.read(str(product), always_2d=True)
        self.assertEqual(data.shape[1], 6)
        # LFE was generated far quieter than the five full-band channels.
        rms = [float(np.sqrt(np.mean(data[:, ch] ** 2))) for ch in range(6)]
        self.assertEqual(rms.index(min(rms)), 3)

    def test_report_matches_the_written_product(self) -> None:
        from audiorepro.loudness import analyze_array

        generate(self.tmp / "input", seed=17)
        run_pipeline(_params(self.tmp), self.tmp / "report.json")
        report = json.loads((self.tmp / "report.json").read_text())
        for entry in report["files"]:
            product = self.tmp / "output" / entry["output_path"]
            data, rate = sf.read(str(product), always_2d=True)
            recomputed = analyze_array(data, rate)
            self.assertAlmostEqual(entry["rms_after_dbfs"], recomputed.rms_dbfs, places=5)
            self.assertEqual(entry["clip_samples_after"], recomputed.clip_samples)
            if entry["lufs_after"] is None:
                self.assertIsNone(recomputed.lufs)
            else:
                self.assertAlmostEqual(entry["lufs_after"], recomputed.lufs, places=5)

    def test_outputs_are_byte_identical_across_runs(self) -> None:
        generate(self.tmp / "input", seed=17)
        first = run_pipeline(_params(self.tmp, jobs=1), self.tmp / "r1.json")
        second = run_pipeline(
            _params(self.tmp, jobs=4, output_dir=self.tmp / "output2"), self.tmp / "r2.json"
        )
        hashes_a = {f["relative_path"]: f["output_sha256"] for f in first.report["files"]}
        hashes_b = {f["relative_path"]: f["output_sha256"] for f in second.report["files"]}
        self.assertEqual(hashes_a, hashes_b)
        raw = (self.tmp / "output" / "mono" / "mono_tone.wav").read_bytes()
        self.assertNotIn(b"PEAK", raw)
        self.assertNotIn(b"fact", raw)

    def test_failed_file_does_not_block_others(self) -> None:
        generate(self.tmp / "input", seed=17)
        (self.tmp / "input" / "mono" / "mono_tone.wav").write_bytes(b"RIFF....WAVEbroken")
        result = run_pipeline(_params(self.tmp), self.tmp / "report.json")
        self.assertEqual(result.exit_code, 3)
        self.assertEqual(result.report["summary"]["files"], 5)
        self.assertEqual(result.report["summary"]["failed"], 1)
        self.assertEqual(result.report["errors"][0]["path"], "mono/mono_tone.wav")
        self.assertIn("stage", result.report["errors"][0])


class VerifyTests(TempDirTest):
    def _run(self) -> tuple[dict, object]:
        generate(self.tmp / "input", seed=17)
        report_path = self.tmp / "report.json"
        run_pipeline(_params(self.tmp), report_path)
        return json.loads(report_path.read_text()), report_path

    def test_verify_roundtrip_has_no_differences(self) -> None:
        _, report_path = self._run()
        outcome = verify(self.tmp / "input", self.tmp / "output", report_path, 0.15)
        self.assertTrue(outcome["ok"])
        self.assertEqual(outcome["files_checked"], 6)
        self.assertEqual(outcome["files_with_differences"], 0)

    def test_verify_detects_tampered_product(self) -> None:
        _, report_path = self._run()
        product = self.tmp / "output" / "mono" / "mono_tone.wav"
        data, rate = sf.read(str(product), always_2d=True)
        sf.write(str(product), data * 0.25, rate, subtype="FLOAT")
        outcome = verify(self.tmp / "input", self.tmp / "output", report_path, 0.15)
        self.assertFalse(outcome["ok"])
        fields = {diff["field"] for item in outcome["files"] for diff in item["diffs"]}
        self.assertIn("lufs_after", fields)
        self.assertIn("integrity", fields)

    def test_verify_reports_missing_output_without_aborting(self) -> None:
        _, report_path = self._run()
        (self.tmp / "output" / "mono" / "mono_tone.wav").unlink()
        outcome = verify(self.tmp / "input", self.tmp / "output", report_path, 0.15)
        self.assertFalse(outcome["ok"])
        self.assertEqual(outcome["files_checked"], 6)
        self.assertEqual(outcome["files_scored"], 5)
        self.assertEqual(outcome["files_with_differences"], 1)
        self.assertEqual(outcome["files"][0]["relative_path"], "mono/mono_short.aiff")
        broken = next(
            item for item in outcome["files"] if item["relative_path"] == "mono/mono_tone.wav"
        )
        self.assertEqual(broken["diffs"][0]["field"], "integrity")


class CliTests(TempDirTest):
    def test_full_acceptance_flow(self) -> None:
        self.assertEqual(main(["fixture", "--out", str(self.tmp / "input"), "--seed", "17"]), 0)
        report = self.tmp / "report.json"
        self.assertEqual(
            main([
                "process", "--input", str(self.tmp / "input"), "--output", str(self.tmp / "out"),
                "--report", str(report), "--rate", "48000", "--target-lufs", "-23",
                "--peak-dbfs", "-1", "--jobs", "2", "--resume",
            ]),
            0,
        )
        self.assertEqual(
            main([
                "verify", "--input", str(self.tmp / "input"), "--output", str(self.tmp / "out"),
                "--report", str(report), "--tolerance", "0.15",
            ]),
            0,
        )

    def test_invalid_parameters_exit_2(self) -> None:
        self.assertEqual(main(["fixture", "--out", str(self.tmp / "in")]), 0)
        base = [
            "process", "--input", str(self.tmp / "in"), "--output", str(self.tmp / "out"),
            "--report", str(self.tmp / "r.json"),
        ]
        self.assertEqual(main(base + ["--rate", "1000"]), 2)
        self.assertEqual(main(base + ["--rate", "48000", "--jobs", "0"]), 2)
        self.assertEqual(main(base + ["--rate", "48000", "--target-lufs", "nan"]), 2)
        self.assertEqual(main(base + ["--rate", "48000", "--peak-dbfs", "3"]), 2)
        self.assertEqual(main(["process", "--input", str(self.tmp / "missing")] + base[5:]), 2)
        self.assertEqual(main(["nonsense"]), 2)
        self.assertEqual(main([]), 2)

    def test_task_failure_exit_3(self) -> None:
        generate(self.tmp / "input", seed=17)
        (self.tmp / "input" / "mono" / "mono_tone.wav").write_bytes(b"RIFF....WAVEbad")
        self.assertEqual(
            main([
                "process", "--input", str(self.tmp / "input"), "--output", str(self.tmp / "out"),
                "--report", str(self.tmp / "report.json"), "--rate", "48000",
            ]),
            3,
        )

    def test_unwritable_report_exit_4(self) -> None:
        generate(self.tmp / "input", seed=17)
        blocker = self.tmp / "blocker"
        blocker.write_text("not a directory")
        self.assertEqual(
            main([
                "process", "--input", str(self.tmp / "input"), "--output", str(self.tmp / "out"),
                "--report", str(blocker / "report.json"), "--rate", "48000",
            ]),
            4,
        )

    def test_verify_detects_tampered_output(self) -> None:
        generate(self.tmp / "input", seed=17)
        report = self.tmp / "report.json"
        main([
            "process", "--input", str(self.tmp / "input"), "--output", str(self.tmp / "out"),
            "--report", str(report), "--rate", "48000",
        ])
        product = self.tmp / "out" / "mono" / "mono_tone.wav"
        data, rate = sf.read(str(product), always_2d=True)
        sf.write(str(product), data * 0.1, rate, subtype="FLOAT")
        self.assertEqual(
            main([
                "verify", "--input", str(self.tmp / "input"), "--output", str(self.tmp / "out"),
                "--report", str(report),
            ]),
            3,
        )


if __name__ == "__main__":
    unittest.main()
