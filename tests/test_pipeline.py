"""Pipeline tests: offline/stream consistency, stats and report shape."""

from __future__ import annotations

import io
import json
import tempfile
import unittest
from contextlib import redirect_stdout
from pathlib import Path

import numpy as np
import soundfile as sf

from mixforge import cli
from mixforge.manifest import load_manifest
from mixforge.render import render

import sys
sys.path.insert(0, str(Path(__file__).resolve().parent.parent / "tools"))
import make_fixture  # noqa: E402


class PipelineTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls) -> None:
        cls._tmp = tempfile.TemporaryDirectory()
        cls.root = Path(cls._tmp.name)
        cls.fixture = cls.root / "fixture"
        make_fixture.build_fixture(cls.fixture, duration=3.0)
        cls.manifest = cls.fixture / "manifest.json"
        cls.project = load_manifest(cls.manifest)

    @classmethod
    def tearDownClass(cls) -> None:
        cls._tmp.cleanup()

    def _render(self, mode: str, block_size: int, name: str):
        out = self.root / name
        report = render(
            self.project, out, sample_rate=48000, duration=3.0,
            mode=mode, block_size=block_size,
        )
        data, rate = sf.read(out, dtype="float64", always_2d=True)
        return report, data, rate

    def test_offline_stream_identical_across_block_sizes(self) -> None:
        reference = None
        for mode in ("offline", "stream"):
            for block in (1024, 512, 1000, 4096, 1):
                _, data, rate = self._render(mode, block, f"{mode}-{block}.wav")
                self.assertEqual(rate, 48000)
                self.assertEqual(data.shape, (144000, 2))
                if reference is None:
                    reference = data
                else:
                    np.testing.assert_array_equal(data, reference)

    def test_report_shape_and_clip_counts(self) -> None:
        report, _, _ = self._render("offline", 1024, "report.wav")
        self.assertEqual(report["sample_rate"], 48000)
        self.assertEqual(report["frames"], 144000)
        self.assertEqual(report["channels"], 2)
        self.assertEqual(len(report["tracks"]), 3)
        for track in report["tracks"]:
            self.assertEqual(len(track["curve"]), 31)
            for point in track["curve"]:
                self.assertIn("frame", point)
                self.assertIn("db", point)
                self.assertIn("gain", point)
            self.assertGreaterEqual(track["peak"], 0.0)
            self.assertGreaterEqual(track["rms"], 0.0)
        impact = next(t for t in report["tracks"] if t["id"] == "impact")
        self.assertGreater(impact["peak"], 1.0)
        self.assertGreater(impact["clip_samples"], 0)
        self.assertGreater(report["mix"]["clip_samples"], 0)
        self.assertGreater(report["mix"]["peak"], 1.0)

    def test_curve_keeps_keyframes(self) -> None:
        report, _, _ = self._render("offline", 1024, "curve.wav")
        impact = next(t for t in report["tracks"] if t["id"] == "impact")
        dbs = {round(p["frame"]): p["db"] for p in impact["curve"]}
        # Keyframes at absolute 1.0s (+4 dB) and 2.5s (-2 dB).
        self.assertAlmostEqual(dbs[48000], 4.0)
        self.assertAlmostEqual(dbs[120000], -2.0)
        gains = {round(p["frame"]): p["gain"] for p in impact["curve"]}
        self.assertAlmostEqual(gains[48000], 10.0 ** (4.0 / 20.0), places=9)

    def test_stats_match_signal(self) -> None:
        report, data, _ = self._render("stream", 1024, "stats.wav")
        # The WAV stores FLOAT (32-bit); stats accumulate in float64, so
        # compare against the read-back signal within float32 precision.
        self.assertAlmostEqual(
            report["mix"]["peak"], float(np.abs(data).max()), delta=1e-6
        )
        expected_rms = float(np.sqrt(np.square(data).mean()))
        self.assertAlmostEqual(report["mix"]["rms"], expected_rms, delta=1e-6)
        self.assertEqual(
            report["mix"]["clip_samples"], int((np.abs(data) > 1.0).sum())
        )

    def test_cli_render_and_compare_exit_codes(self) -> None:
        offline = self.root / "cli-offline.wav"
        stream = self.root / "cli-stream.wav"
        base = ["--manifest", str(self.manifest), "--rate", "48000",
                "--duration", "3", "--block-size", "1024"]
        buf = io.StringIO()
        with redirect_stdout(buf):
            code = cli.main(["render", *base, "--out", str(offline),
                             "--mode", "offline"])
        self.assertEqual(code, 0)
        report = json.loads(buf.getvalue())
        self.assertEqual(report["frames"], 144000)
        with redirect_stdout(io.StringIO()):
            code = cli.main(["render", *base, "--out", str(stream),
                             "--mode", "stream"])
        self.assertEqual(code, 0)
        buf = io.StringIO()
        with redirect_stdout(buf):
            code = cli.main(["compare", "--left", str(offline),
                             "--right", str(stream), "--tolerance", "1e-6"])
        self.assertEqual(code, 0)
        self.assertLessEqual(json.loads(buf.getvalue())["max_abs_delta"], 1e-6)

    def test_cli_compare_mismatch_exit_code(self) -> None:
        _, data, _ = self._render("offline", 1024, "mm-a.wav")
        shifted = data + 0.01
        sf.write(str(self.root / "mm-b.wav"), shifted, 48000,
                 format="WAV", subtype="FLOAT")
        with redirect_stdout(io.StringIO()):
            code = cli.main(["compare", "--left", str(self.root / "mm-a.wav"),
                             "--right", str(self.root / "mm-b.wav"),
                             "--tolerance", "1e-6"])
        self.assertEqual(code, 4)


if __name__ == "__main__":
    unittest.main()
