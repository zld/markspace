"""Validation, limiter, metrics and CLI contract tests."""

from __future__ import annotations

import json
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

import numpy as np
import soundfile as sf

from mixforge.cli import main as cli_main
from mixforge.metrics import MetricsAccumulator, compute
from mixforge.model import ModelError, load_manifest
from mixforge.render import render_offline

SR = 48000
REPO_ROOT = Path(__file__).resolve().parents[1]


def write_wav(directory: Path, name: str, data: np.ndarray, rate: int = SR) -> Path:
    path = directory / name
    sf.write(str(path), data, rate, format="WAV", subtype="FLOAT")
    return path


def write_manifest(directory: Path, payload: dict) -> Path:
    path = directory / "session.json"
    path.write_text(json.dumps(payload), encoding="utf-8")
    return path


def manifest(tracks: list[dict], *, channels: int = 2, limiter: bool = False) -> dict:
    return {
        "name": "t", "sample_rate": SR, "channels": channels,
        "block_size": 128, "limiter": limiter, "tracks": tracks,
    }


class ValidationTests(unittest.TestCase):
    def setUp(self) -> None:
        self._tmp = tempfile.TemporaryDirectory()
        self.tmp = Path(self._tmp.name)

    def tearDown(self) -> None:
        self._tmp.cleanup()

    def test_rejects_non_increasing_and_duplicate_node_times(self) -> None:
        write_wav(self.tmp, "a.wav", np.zeros((16, 1)))
        for nodes in (
            [{"time": 0.5, "gain": 1.0}, {"time": 0.5, "gain": 0.5}],
            [{"time": 0.8, "gain": 1.0}, {"time": 0.4, "gain": 0.5}],
        ):
            path = write_manifest(self.tmp, manifest([{
                "name": "a", "path": "a.wav",
                "channel_map": [[0, 0]],
                "automation": {"interpolation": "linear", "nodes": nodes},
            }], channels=1))
            with self.assertRaises(ModelError):
                load_manifest(path)

    def test_rejects_non_finite_and_negative_gain(self) -> None:
        write_wav(self.tmp, "a.wav", np.zeros((16, 1)))
        bad_nodes = [
            [{"time": 0.0, "gain": float("nan")}],
            [{"time": 0.0, "gain": float("inf")}],
            [{"time": 0.0, "gain": -0.1}],
        ]
        for nodes in bad_nodes:
            path = write_manifest(self.tmp, manifest([{
                "name": "a", "path": "a.wav", "channel_map": [[0, 0]],
                "automation": {"interpolation": "linear", "nodes": nodes},
            }], channels=1))
            with self.assertRaises(ModelError):
                load_manifest(path)

    def test_missing_file_corrupt_wav_and_bad_fields_raise(self) -> None:
        good = write_wav(self.tmp, "a.wav", np.zeros((16, 1)))
        missing = write_manifest(self.tmp, manifest([
            {"name": "a", "path": "nope.wav", "channel_map": [[0, 0]]},
        ], channels=1))
        with self.assertRaises(ModelError):
            load_manifest(missing)
        corrupt = self.tmp / "bad.wav"
        corrupt.write_bytes(b"RIFFxxxxnot a real wav")
        bad_wav = write_manifest(self.tmp, manifest([
            {"name": "b", "path": "bad.wav", "channel_map": [[0, 0]]},
        ], channels=1))
        with self.assertRaises(ModelError):
            load_manifest(bad_wav)
        bad_field = write_manifest(self.tmp, manifest([
            {"name": "c", "path": good.name, "gain": -1,
             "channel_map": [[0, 0]]},
        ], channels=1))
        with self.assertRaises(ModelError):
            load_manifest(bad_field)


class LimiterAndMetricsTests(unittest.TestCase):
    def setUp(self) -> None:
        self._tmp = tempfile.TemporaryDirectory()
        self.tmp = Path(self._tmp.name)

    def tearDown(self) -> None:
        self._tmp.cleanup()

    def test_limiter_reports_pre_and_post_clip_counts(self) -> None:
        # Gain 2 pushes a unit signal to +/- 2.
        write_wav(self.tmp, "a.wav", np.ones((256, 1)))
        payload = manifest([{
            "name": "a", "path": "a.wav", "gain": 2.0,
            "channel_map": [[0, 0]],
        }], channels=1, limiter=True)
        result = render_offline(load_manifest(write_manifest(self.tmp, payload)))
        self.assertGreater(result.bus_pre_limiter.clips, 0)
        self.assertEqual(result.bus_post_limiter.clips, 0)
        self.assertLessEqual(float(np.max(np.abs(result.output))), 1.0)

    def test_limiter_off_preserves_overrange_and_counts_clips(self) -> None:
        write_wav(self.tmp, "a.wav", np.ones((128, 1)))
        payload = manifest([{
            "name": "a", "path": "a.wav", "gain": 2.0,
            "channel_map": [[0, 0]],
        }], channels=1, limiter=False)
        result = render_offline(load_manifest(write_manifest(self.tmp, payload)))
        self.assertEqual(result.bus_pre_limiter.clips,
                         result.bus_post_limiter.clips)
        self.assertGreater(result.bus_post_limiter.clips, 0)
        self.assertAlmostEqual(float(np.max(result.output)), 2.0, places=5)

    def test_metrics_peak_rms_clip_and_silence(self) -> None:
        self.assertEqual(compute(np.zeros((10, 2))).rms, 0.0)
        signal = np.array([[0.5, -0.5], [0.0, 0.0]])
        metrics = compute(signal)
        self.assertAlmostEqual(metrics.peak, 0.5)
        self.assertAlmostEqual(metrics.rms, np.sqrt(0.5 / 4))
        self.assertEqual(metrics.frames, 2)
        self.assertEqual(metrics.clips, 0)
        over = np.array([[1.5], [-2.0], [0.1]])
        self.assertEqual(compute(over).clips, 2)
        # Streaming accumulation matches the one-shot computation.
        acc = MetricsAccumulator()
        acc.add(over[:2]); acc.add(over[2:])
        self.assertEqual(acc.result().clips, 2)
        self.assertAlmostEqual(acc.result().peak, 2.0)


class CliContractTests(unittest.TestCase):
    def setUp(self) -> None:
        self._tmp = tempfile.TemporaryDirectory()
        self.tmp = Path(self._tmp.name)

    def tearDown(self) -> None:
        self._tmp.cleanup()

    def test_cli_success_exit_0_and_summary_fields(self) -> None:
        write_wav(self.tmp, "a.wav", np.ones((256, 1)))
        write_manifest(self.tmp, manifest([{
            "name": "a", "path": "a.wav",
            "channel_map": [[0, 0], [0, 1]],
            "automation": {"interpolation": "linear", "nodes": [
                {"time": 0.0, "gain": 1.0},
                {"time": 0.005, "gain": 0.5},
            ]},
        }]))
        out_wav = self.tmp / "out.wav"
        out_json = self.tmp / "summary.json"
        code = cli_main([
            "render", "--manifest", str(self.tmp / "session.json"),
            "--mode", "stream", "--block-size", "64",
            "--out", str(out_wav), "--summary", str(out_json),
        ])
        self.assertEqual(code, 0)
        summary = json.loads(out_json.read_text())
        for field in ("sample_rate", "frames", "mode", "automation_samples",
                      "tracks", "bus", "channels", "block_size"):
            self.assertIn(field, summary)
        self.assertGreaterEqual(len(summary["automation_samples"]["a"]), 4)
        self.assertTrue(out_wav.exists())

    def test_cli_input_error_exit_2(self) -> None:
        code = cli_main([
            "render", "--manifest", str(self.tmp / "missing.json"),
            "--out", str(self.tmp / "o.wav"),
            "--summary", str(self.tmp / "s.json"),
        ])
        self.assertEqual(code, 2)

    def test_write_failure_leaves_no_summary(self) -> None:
        write_wav(self.tmp, "a.wav", np.ones((16, 1)))
        write_manifest(self.tmp, manifest([
            {"name": "a", "path": "a.wav", "channel_map": [[0, 0], [0, 1]]},
        ]))
        # Output directory is a file: WAV write must fail first.
        blocker = self.tmp / "blocked"
        blocker.write_text("x")
        summary = self.tmp / "summary.json"
        code = cli_main([
            "render", "--manifest", str(self.tmp / "session.json"),
            "--out", str(blocker / "o.wav"),
            "--summary", str(summary),
        ])
        self.assertEqual(code, 1)
        self.assertFalse(summary.exists())

    def test_acceptance_fixture_runs_as_subprocess(self) -> None:
        fixtures = REPO_ROOT / "fixtures"
        if not fixtures.exists():
            self.skipTest("run scripts/make_fixture.py first")
        result = subprocess.run(
            [sys.executable, "-m", "mixforge.cli", "render",
             "--manifest", str(fixtures / "session.json"),
             "--mode", "stream", "--block-size", "257",
             "--out", str(self.tmp / "mix.wav"),
             "--summary", str(self.tmp / "summary.json")],
            cwd=REPO_ROOT, capture_output=True, text=True, check=False,
        )
        self.assertEqual(result.returncode, 0, result.stderr)
        summary = json.loads(result.stdout)
        self.assertEqual(summary["sample_rate"], 48000)
        self.assertAlmostEqual(summary["frames"], 96000, delta=100)
        self.assertGreaterEqual(
            sum(len(v) for v in summary["automation_samples"].values()), 4
        )
        for name, metrics in summary["tracks"].items():
            self.assertGreaterEqual(metrics["peak"], 0.0)
            self.assertLessEqual(metrics["peak"], 1.5)
            self.assertGreaterEqual(metrics["rms"], 0.0)
            self.assertLessEqual(metrics["rms"], 1.5)
            self.assertIsInstance(metrics["clips"], int)
            self.assertGreaterEqual(metrics["clips"], 0)


if __name__ == "__main__":
    unittest.main()
