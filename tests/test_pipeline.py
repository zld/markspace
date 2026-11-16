"""Integration tests for the audiomix pipeline."""
from __future__ import annotations

import contextlib
import io
import json
import tempfile
import unittest
from pathlib import Path

import numpy as np
import soundfile as sf

from audiomix.cli import main
from audiomix.curve import CurveError, validate_points
from audiomix.graph import build_graph
from audiomix.model import TrackSpec, load_manifest
from audiomix.offline import render_offline
from audiomix.resample import SincResampler, output_frames, resample
from audiomix.stream import render_stream

from scripts.generate_fixture import build_demo_manifest


def run_cli(argv):
    """Run the CLI, returning (exit_code, stdout, stderr)."""
    out, err = io.StringIO(), io.StringIO()
    with contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
        code = main(argv)
    return code, out.getvalue(), err.getvalue()


class DemoEndToEndTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls._tmp = tempfile.TemporaryDirectory()
        cls.workdir = Path(cls._tmp.name)
        cls.out_path = cls.workdir / "mix.wav"
        code, stdout, _ = run_cli([
            "demo", "--out", str(cls.out_path),
            "--chunk-frames", "257", "--curve-samples", "5", "--report", "-"])
        cls.exit_code = code
        cls.report = json.loads(stdout)

    @classmethod
    def tearDownClass(cls):
        cls._tmp.cleanup()

    def test_exit_code_and_output_file(self):
        self.assertEqual(self.exit_code, 0)
        data, rate = sf.read(str(self.out_path))
        self.assertEqual(rate, 48000)
        self.assertEqual(data.shape, (120000, 2))

    def test_report_shape(self):
        r = self.report
        self.assertEqual(r["sample_rate"], 48000)
        self.assertEqual(r["frames"], 120000)
        self.assertEqual(len(r["tracks"]), 3)
        for track in r["tracks"]:
            self.assertIn("peak", track)
            self.assertIn("rms", track)
        self.assertEqual(len(r["automation_samples"]), 15)
        self.assertIsInstance(r["clipping_count"], int)
        self.assertGreater(r["clipping_count"], 0)  # over-range kept, not clipped
        self.assertLessEqual(r["offline_stream_max_abs_error"], 2e-5)

    def test_over_range_values_survive_in_file(self):
        data, _ = sf.read(str(self.out_path), dtype="float64")
        self.assertGreater(np.max(np.abs(data)), 1.0)


class RenderParityTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls._tmp = tempfile.TemporaryDirectory()
        cls.manifest_path = build_demo_manifest(cls._tmp.name)
        cls.manifest = load_manifest(cls.manifest_path)

    @classmethod
    def tearDownClass(cls):
        cls._tmp.cleanup()

    def _loaded(self):
        from audiomix.model import load_track_audio
        return [(s, *load_track_audio(s)) for s in self.manifest.tracks]

    def test_offline_stream_parity_all_chunk_sizes(self):
        loaded = self._loaded()
        rate = self.manifest.sample_rate
        reference = render_offline(build_graph(loaded, rate))
        self.assertEqual(reference.shape[0], 120000)
        for chunk in (1, 257, 4096, 120000):
            mix = render_stream(build_graph(loaded, rate), chunk)
            self.assertEqual(mix.shape, reference.shape)
            err = float(np.max(np.abs(mix - reference)))
            self.assertLessEqual(err, 2e-5, f"chunk={chunk}")

    def test_offset_region_is_silent(self):
        only_offset_track = [self._loaded()[1]]  # ambience, offset 0.25 s
        mix = render_offline(build_graph(only_offset_track, 48000))
        head = mix[: int(0.25 * 48000) - 1]
        self.assertTrue(np.all(head == 0.0))
        self.assertGreater(np.max(np.abs(mix)), 0.0)

    def test_short_final_block(self):
        loaded = self._loaded()
        mix = render_stream(build_graph(loaded, 48000), 257)
        self.assertEqual(mix.shape[0], 120000)  # 120000 = 466*257 + 202


class ResamplerTest(unittest.TestCase):
    def test_chunk_boundaries_do_not_change_output(self):
        rng = np.random.default_rng(3)
        x = rng.standard_normal((5000, 1)) * 0.3
        whole = resample(x, 32000, 48000)
        engine = SincResampler(32000, 48000, 1)
        parts = [engine.process(x[i:i + 333]) for i in range(0, 5000, 333)]
        parts.append(engine.flush())
        chunked = np.concatenate(parts)
        self.assertEqual(chunked.shape, (output_frames(5000, 32000, 48000), 1))
        self.assertEqual(float(np.max(np.abs(whole - chunked))), 0.0)

    def test_output_length_is_exact(self):
        x = np.zeros((110250, 2))
        self.assertEqual(resample(x, 44100, 48000).shape[0], 120000)


class CurveValidationTest(unittest.TestCase):
    def test_duplicate_time_rejected(self):
        with self.assertRaises(CurveError):
            validate_points([[0.0, -3.0], [0.0, -6.0]], "vox")

    def test_non_finite_rejected(self):
        for bad in ([[float("nan"), 0.0]], [[0.0, float("inf")]],
                    [[-1.0, 0.0]]):
            with self.assertRaises(CurveError):
                validate_points(bad, "vox")

    def test_absurd_gain_rejected(self):
        with self.assertRaises(CurveError):
            validate_points([[0.0, 240.0]], "vox")

    def test_endpoints_held_outside_range(self):
        from audiomix.curve import gain_db_at
        pts = validate_points([[1.0, -6.0], [2.0, 0.0]], "vox")
        self.assertEqual(gain_db_at(pts, 0.0), -6.0)
        self.assertEqual(gain_db_at(pts, 5.0), 0.0)
        self.assertEqual(gain_db_at(pts, 1.5), -3.0)


class FailureHandlingTest(unittest.TestCase):
    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory()
        self.workdir = Path(self._tmp.name)

    def tearDown(self):
        self._tmp.cleanup()

    def _write_manifest(self, tracks):
        path = self.workdir / "manifest.json"
        path.write_text(json.dumps({"sample_rate": 48000, "tracks": tracks}))
        return path

    def _run_mix(self, manifest_path, out_name="out.wav"):
        return run_cli(["mix", "--manifest", str(manifest_path),
                        "--out", str(self.workdir / out_name)])

    def test_corrupt_file_reports_track_and_reason(self):
        (self.workdir / "broken.wav").write_bytes(b"not audio at all")
        manifest = self._write_manifest([
            {"name": "vox", "path": "broken.wav"}])
        code, _, stderr = self._run_mix(manifest)
        self.assertEqual(code, 2)
        self.assertIn("vox", stderr)
        self.assertIn("broken.wav", stderr)

    def test_unsupported_channel_count(self):
        sf.write(self.workdir / "quad.wav",
                 np.zeros((4800, 4)), 48000)
        manifest = self._write_manifest([
            {"name": "surround", "path": "quad.wav"}])
        code, _, stderr = self._run_mix(manifest)
        self.assertEqual(code, 2)
        self.assertIn("surround", stderr)
        self.assertIn("channel", stderr)

    def test_illegal_manifest_sample_rate(self):
        manifest = self._write_manifest([])
        data = json.loads(manifest.read_text())
        data["sample_rate"] = 123
        data["tracks"] = [{"name": "x", "path": "x.wav"}]
        manifest.write_text(json.dumps(data))
        code, _, stderr = self._run_mix(manifest)
        self.assertEqual(code, 2)
        self.assertIn("sample_rate", stderr)

    def test_write_failure_leaves_no_file(self):
        manifest_path = build_demo_manifest(self.workdir / "fixtures")
        out_dir = self.workdir / "blocked"
        out_dir.mkdir()
        code, _, stderr = run_cli([
            "mix", "--manifest", str(manifest_path), "--out", str(out_dir)])
        self.assertEqual(code, 3)
        self.assertIn("processing error", stderr)
        leftovers = [p for p in self.workdir.iterdir() if p.suffix == ".part"]
        self.assertEqual(leftovers, [])


if __name__ == "__main__":
    unittest.main()
