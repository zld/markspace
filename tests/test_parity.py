"""Parity tests: offline and streaming paths must agree exactly."""

from __future__ import annotations

import json
import tempfile
import unittest
from pathlib import Path

import numpy as np
import soundfile as sf

from audiomix.cli import EXIT_OK, main
from audiomix.graph import MixEngine
from audiomix.model import load_manifest
from audiomix.offline import render_offline
from audiomix.resample import Resampler, resample
from audiomix.stream import StreamRenderer, render_stream


def _write_scene(root: Path) -> Path:
    rng = np.random.default_rng(123)
    sf.write(root / "a.wav", rng.standard_normal((4000, 1)) * 0.4, 44100)
    sf.write(root / "b.wav", rng.standard_normal((2500, 2)) * 0.3, 32000)
    sf.write(root / "c.wav", rng.standard_normal((6000, 1)) * 0.2, 48000)
    manifest = {
        "buses": {"sub": {"target": "master"}},
        "tracks": [
            {"file": "a.wav", "offset": 0.01, "bus": "sub",
             "curve": [[0.0, 0.0], [0.1, -9.0], [0.3, -2.0]]},
            {"file": "b.wav", "offset": 0.05, "bus": "master",
             "curve": [[0.02, -3.0], [0.2, 0.0]]},
            {"file": "c.wav", "offset": 0.0, "bus": "sub"},
        ],
    }
    scene = root / "scene.json"
    scene.write_text(json.dumps(manifest), encoding="utf-8")
    return scene


class ResamplerParityTest(unittest.TestCase):
    def test_chunked_matches_one_shot(self):
        rng = np.random.default_rng(5)
        src = rng.standard_normal((9000, 2))
        whole = resample(src, 44100, 48000)
        for block in (1, 7, 257, 4096, 100000):
            r = Resampler(src, 44100, 48000)
            parts = []
            while not r.exhausted:
                parts.append(r.read(block))
            chunked = np.vstack(parts)
            self.assertEqual(chunked.shape, whole.shape)
            self.assertEqual(np.abs(chunked - whole).max(), 0.0)

    def test_output_length_ceil(self):
        src = np.zeros((1000, 1))
        self.assertEqual(resample(src, 44100, 48000).shape[0],
                         int(np.ceil(1000 * 48000 / 44100)))

    def test_identity_rate_is_exact(self):
        src = np.random.default_rng(1).standard_normal((500, 2))
        self.assertTrue(np.array_equal(resample(src, 48000, 48000), src))


class EngineParityTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls._tmp = tempfile.TemporaryDirectory()
        cls.scene = _write_scene(Path(cls._tmp.name))

    @classmethod
    def tearDownClass(cls):
        cls._tmp.cleanup()

    def _engine(self, rate=48000, channels=2):
        return MixEngine(load_manifest(self.scene), rate, channels)

    def test_offline_vs_stream_block_sizes(self):
        eng = self._engine()
        ref = render_offline(eng)
        for block in (1, 3, 257, 1024, eng.total_frames + 10):
            out = render_stream(eng, block)
            self.assertEqual(out.shape, ref.shape)
            self.assertEqual(np.abs(out - ref).max(), 0.0,
                             f"block_size={block} diverged")

    def test_stream_reset_repeatable(self):
        eng = self._engine()
        renderer = StreamRenderer(eng, 257)
        first = renderer.render()
        second = renderer.render()
        self.assertTrue(np.array_equal(first, second))

    def test_mono_output(self):
        eng = self._engine(channels=1)
        ref = render_offline(eng)
        out = render_stream(eng, 333)
        self.assertEqual(ref.shape[1], 1)
        self.assertEqual(np.abs(out - ref).max(), 0.0)

    def test_cli_mode_both_report(self):
        with tempfile.TemporaryDirectory() as d:
            out = str(Path(d) / "mix.wav")
            report = str(Path(d) / "report.json")
            code = main(["render", "--manifest", str(self.scene),
                         "--rate", "48000", "--channels", "2",
                         "--block-size", "257", "--mode", "both",
                         "--sample-times", "0,0.1,0.2",
                         "--out", out, "--report", report])
            self.assertEqual(code, EXIT_OK)
            data = json.loads(Path(report).read_text())
            self.assertEqual(data["tracks"], 3)
            self.assertEqual(len(data["automation_samples"]), 9)
            self.assertLessEqual(data["max_abs_diff"], 1e-6)
            self.assertIsInstance(data["clipped_samples"], int)
            self.assertEqual(len(data["track_metrics"]), 3)
            wav, rate = sf.read(out, always_2d=True)
            self.assertEqual(rate, 48000)
            self.assertEqual(wav.shape[0], data["frames"])
            self.assertLessEqual(np.abs(wav).max(), 1.0)


if __name__ == "__main__":
    unittest.main()
