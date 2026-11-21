"""Edge-case tests: manifest/audio rejection and exit-code contract."""

from __future__ import annotations

import json
import tempfile
import unittest
from pathlib import Path
from unittest import mock

import numpy as np
import soundfile as sf

from audiomix.cli import EXIT_AUDIO, EXIT_DIVERGED, EXIT_OK, EXIT_USAGE, main


class EdgeCaseTest(unittest.TestCase):
    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory()
        self.root = Path(self._tmp.name)
        rng = np.random.default_rng(9)
        sf.write(self.root / "ok.wav", rng.standard_normal((2000, 1)) * 0.3, 44100)
        self.out = str(self.root / "out.wav")
        self.report = str(self.root / "report.json")

    def tearDown(self):
        self._tmp.cleanup()

    def _scene(self, tracks, buses=None):
        scene = {"tracks": tracks}
        if buses is not None:
            scene["buses"] = buses
        path = self.root / "scene.json"
        path.write_text(json.dumps(scene), encoding="utf-8")
        return str(path)

    def _run(self, scene, *extra):
        argv = ["render", "--manifest", scene, "--rate", "48000",
                "--channels", "2", "--out", self.out, "--report", self.report]
        return main(argv + list(extra))

    def test_ok_minimal(self):
        code = self._run(self._scene([{"file": "ok.wav"}]))
        self.assertEqual(code, EXIT_OK)

    def test_missing_manifest(self):
        self.assertEqual(self._run(str(self.root / "nope.json")), EXIT_USAGE)

    def test_bad_json(self):
        p = self.root / "bad.json"
        p.write_text("{not json")
        self.assertEqual(self._run(str(p)), EXIT_USAGE)

    def test_missing_wav_file(self):
        code = self._run(self._scene([{"file": "ghost.wav"}]))
        self.assertEqual(code, EXIT_AUDIO)

    def test_unknown_bus_names_track(self):
        scene = self._scene([{"file": "ok.wav", "bus": "nope"}])
        self.assertEqual(self._run(scene), EXIT_USAGE)

    def test_bus_cycle(self):
        scene = self._scene(
            [{"file": "ok.wav", "bus": "x"}],
            buses={"x": {"target": "y"}, "y": {"target": "x"}},
        )
        self.assertEqual(self._run(scene), EXIT_USAGE)

    def test_bus_missing_target(self):
        scene = self._scene(
            [{"file": "ok.wav", "bus": "x"}], buses={"x": {"target": "ghost"}}
        )
        self.assertEqual(self._run(scene), EXIT_USAGE)

    def test_curve_duplicate_times(self):
        scene = self._scene([{"file": "ok.wav", "curve": [[0, 0], [0, -3]]}])
        self.assertEqual(self._run(scene), EXIT_USAGE)

    def test_curve_unordered(self):
        scene = self._scene([{"file": "ok.wav", "curve": [[1, 0], [0.5, -3]]}])
        self.assertEqual(self._run(scene), EXIT_USAGE)

    def test_curve_non_finite(self):
        scene = self._scene([{"file": "ok.wav", "curve": [[0, float("nan")]]}])
        self.assertEqual(self._run(scene), EXIT_USAGE)

    def test_negative_offset(self):
        scene = self._scene([{"file": "ok.wav", "offset": -0.5}])
        self.assertEqual(self._run(scene), EXIT_USAGE)

    def test_too_many_tracks(self):
        scene = self._scene([{"file": "ok.wav"}] * 129)
        self.assertEqual(self._run(scene), EXIT_USAGE)

    def test_nan_in_wav(self):
        data = np.zeros((100, 1))
        data[50] = np.nan
        sf.write(self.root / "nan.wav", data, 48000, subtype="FLOAT")
        scene = self._scene([{"file": "nan.wav"}])
        self.assertEqual(self._run(scene), EXIT_AUDIO)

    def test_three_channel_wav_rejected(self):
        sf.write(self.root / "tri.wav", np.zeros((100, 3)), 48000)
        scene = self._scene([{"file": "tri.wav"}])
        self.assertEqual(self._run(scene), EXIT_AUDIO)

    def test_overlong_wav_rejected(self):
        rate = 8000
        sf.write(self.root / "long.wav",
                 np.zeros(rate * 601, dtype=np.float32), rate)
        scene = self._scene([{"file": "long.wav"}])
        self.assertEqual(self._run(scene), EXIT_AUDIO)

    def test_unwritable_output_cleans_up(self):
        scene = self._scene([{"file": "ok.wav"}])
        self.out = str(self.root / "no-such-dir" / "out.wav")
        code = self._run(scene)
        self.assertEqual(code, EXIT_AUDIO)
        self.assertFalse(Path(self.report).exists())

    def test_bad_block_size_exits_2(self):
        scene = self._scene([{"file": "ok.wav"}])
        with self.assertRaises(SystemExit) as ctx:
            self._run(scene, "--block-size", "0")
        self.assertEqual(ctx.exception.code, 2)

    def test_divergence_exits_4(self):
        scene = self._scene([{"file": "ok.wav"}])
        with mock.patch("audiomix.cli.render_stream") as fake:
            fake.return_value = np.ones((10, 2))
            self.assertEqual(self._run(scene), EXIT_DIVERGED)


if __name__ == "__main__":
    unittest.main()
