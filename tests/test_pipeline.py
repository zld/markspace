"""End-to-end pipeline, report and CLI boundary tests."""

import json
import tempfile
import unittest
from pathlib import Path

import numpy as np
import soundfile as sf

from audiopipe.cli import main
from audiopipe.discovery import DiscoveryError, discover_sources
from audiopipe.fixtures import generate_fixtures
from audiopipe.pipeline import BatchParams, run_batch
from audiopipe.report import verify_report


class PipelineEndToEndTests(unittest.TestCase):
    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory()
        self.root = Path(self._tmp.name)
        self.inp = self.root / "in"
        self.out = self.root / "out"
        generate_fixtures(self.inp)
        self.sources, self.errors = discover_sources(self.inp, self.out)

    def tearDown(self):
        self._tmp.cleanup()

    def _run(self, **overrides):
        params = BatchParams(
            target_rate=overrides.get("rate", 48000),
            target_lufs=overrides.get("lufs", -18.0),
            chunk_frames=overrides.get("chunk", 4096),
            taps=overrides.get("taps", 32),
            workers=overrides.get("workers", 2))
        return run_batch(self.sources, list(self.errors), params)

    def test_all_files_succeed_with_required_fields(self):
        records = self._run()
        self.assertEqual(len(records), len(self.sources))
        for record in records:
            self.assertEqual(record["status"], "ok")
            self.assertEqual(record["output_sample_rate"], 48000)
            self.assertIn(record["output_channels"], (1, 2, 6))
            self.assertGreaterEqual(record["rms_before"], 0.0)
            self.assertGreaterEqual(record["rms_after"], 0.0)
            self.assertTrue(np.isfinite(record["rms_before"]))
            self.assertIsInstance(record["clipped_samples_before"], int)
            self.assertIsInstance(record["clipped_samples_after"], int)
            self.assertTrue(Path(record["output"]).is_file())

    def test_outputs_are_pcm24_wav_at_target_rate(self):
        self._run()
        for source in self.sources:
            info = sf.info(str(source.output_path))
            self.assertEqual(info.format, "WAV")
            self.assertEqual(info.subtype, "PCM_24")
            self.assertEqual(info.samplerate, 48000)
            self.assertEqual(info.channels, source.channels)

    def test_non_limited_files_hit_target_loudness(self):
        records = self._run()
        for record in records:
            if record["gain"]["peak_limited"]:
                continue
            if record["lufs_after"] is None:
                continue
            self.assertLessEqual(abs(record["lufs_after"] + 18.0), 0.3)

    def test_silence_is_not_amplified_and_reports_null(self):
        record = next(r for r in self._run() if "silence" in r["source"])
        self.assertIsNone(record["lufs_before"])
        self.assertIsNone(record["lufs_after"])
        self.assertTrue(record["gain"]["silence_skipped"])
        self.assertEqual(record["gain"]["linear"], 1.0)

    def test_peak_limited_file_is_flagged(self):
        record = next(r for r in self._run()
                      if "peak_limited" in r["source"])
        self.assertTrue(record["gain"]["peak_limited"])
        data, _ = sf.read(record["output"], always_2d=True)
        self.assertLessEqual(np.max(np.abs(data)), 10 ** (-1 / 20) + 1e-9)

    def test_peak_ceiling_never_exceeded(self):
        for record in self._run():
            data, _ = sf.read(record["output"], always_2d=True)
            self.assertLessEqual(np.max(np.abs(data)),
                                 10 ** (-1 / 20) + 1e-9)

    def test_frame_count_contract_and_zero_pad_recorded(self):
        for record in self._run():
            self.assertEqual(record["output_frames"],
                             record["expected_output_frames"])
            self.assertGreaterEqual(record["zero_pad_frames"], 0)

    def test_chunk_size_does_not_change_output(self):
        out_small = self.root / "out_small"
        out_large = self.root / "out_large"
        small_sources, _ = discover_sources(self.inp, out_small)
        large_sources, _ = discover_sources(self.inp, out_large)
        small = run_batch(small_sources, [], BatchParams(
            48000, -18.0, 257, 32, 2))
        large = run_batch(large_sources, [], BatchParams(
            48000, -18.0, 16384, 32, 2))
        small = [r for r in small if r["status"] == "ok"]
        large = [r for r in large if r["status"] == "ok"]
        small_map = {r["source"]: r for r in small}
        for record in large:
            counterpart = small_map[record["source"]]
            self.assertEqual(counterpart["output_frames"],
                             record["output_frames"])
            a, _ = sf.read(counterpart["output"], always_2d=True)
            b, _ = sf.read(record["output"], always_2d=True)
            np.testing.assert_allclose(a, b, atol=1e-9)


class ErrorHandlingTests(unittest.TestCase):
    def test_corrupt_file_is_recorded_and_others_continue(self):
        with tempfile.TemporaryDirectory() as td:
            inp, out = Path(td) / "in", Path(td) / "out"
            generate_fixtures(inp)
            (inp / "mono_44k.wav").write_bytes(b"not a real wav")
            sources, errors = discover_sources(inp, out)
            records = run_batch(sources, errors, BatchParams(
                48000, -18.0, 4096, 32, 2))
            failed = [r for r in records if r["status"] == "error"]
            ok = [r for r in records if r["status"] == "ok"]
            self.assertTrue(failed)
            self.assertIn("mono_44k", failed[0]["source"])
            self.assertGreaterEqual(len(ok), 5)

    def test_unsupported_layout_rejected_before_processing(self):
        with tempfile.TemporaryDirectory() as td:
            inp, out = Path(td) / "in", Path(td) / "out"
            inp.mkdir()
            sf.write(str(inp / "three.wav"), np.zeros((16, 3)), 48000)
            sources, errors = discover_sources(inp, out)
            self.assertEqual(sources, [])
            self.assertEqual(errors[0]["status"], "error")
            self.assertIn("channel layout", errors[0]["error"])

    def test_nested_output_directory_rejected(self):
        with tempfile.TemporaryDirectory() as td:
            inp = Path(td) / "in"
            inp.mkdir()
            with self.assertRaises(DiscoveryError):
                discover_sources(inp, inp / "nested")


class ReportAndCliTests(unittest.TestCase):
    def test_verify_roundtrip_and_tamper_detection(self):
        with tempfile.TemporaryDirectory() as td:
            inp, out = Path(td) / "in", Path(td) / "out"
            report = Path(td) / "report.json"
            generate_fixtures(inp)
            code = main(["batch", "--input", str(inp), "--output", str(out),
                         "--rate", "48000", "--target-lufs", "-18",
                         "--chunk-frames", "4096", "--workers", "2",
                         "--report", str(report)])
            self.assertEqual(code, 0)
            payload = json.loads(report.read_text())
            self.assertEqual(payload["summary"]["failed"], 0)
            ok, discrepancies = verify_report(report)
            self.assertTrue(ok)
            self.assertEqual(discrepancies, [])
            target = next(f for f in payload["files"] if f["status"] == "ok")
            data, sr = sf.read(target["output"], always_2d=True)
            sf.write(target["output"], data * 0.25, sr, subtype="PCM_24")
            ok, discrepancies = verify_report(report)
            self.assertFalse(ok)
            self.assertTrue(discrepancies)
            self.assertEqual(main(["verify", "--report", str(report)]), 3)

    def test_usage_error_exit_code(self):
        with tempfile.TemporaryDirectory() as td:
            code = main(["batch", "--input", td,
                         "--output", str(Path(td) / "o"),
                         "--rate", "0", "--report", str(Path(td) / "r.json")])
            self.assertEqual(code, 2)

    def test_fixtures_command(self):
        with tempfile.TemporaryDirectory() as td:
            target = Path(td) / "fx"
            code = main(["fixtures", "--dir", str(target)])
            self.assertEqual(code, 0)
            self.assertTrue(any(target.glob("*.wav")))


if __name__ == "__main__":
    unittest.main()
