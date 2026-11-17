"""Unit tests for validation, DSP statefulness and error handling."""

from __future__ import annotations

import json
import os
import stat
import tempfile
import unittest
from pathlib import Path

import numpy as np
import soundfile as sf

from trackmix.automation import GainCurve
from trackmix.errors import (
    AudioError,
    ManifestError,
    StreamVerifyError,
    WriteError,
)
from trackmix.graph import Graph, Node, SumNode, TrackNode, build_graph
from trackmix.io_utils import write_wav
from trackmix.manifest import load_manifest
from trackmix.resample import SincResampler
from trackmix.streaming import verify_against_offline


class ResamplerTest(unittest.TestCase):
    def test_same_rate_identity(self) -> None:
        rng = np.random.default_rng(3)
        data = rng.normal(size=(512, 2))
        r = SincResampler(48000, 48000, 2)
        r.feed(data)
        np.testing.assert_allclose(r.read(512), data, atol=1e-12)

    def test_chunked_matches_whole_for_any_rate_pair(self) -> None:
        rng = np.random.default_rng(4)
        for in_rate, out_rate in ((44100, 48000), (48000, 22050),
                                  (32000, 48000)):
            src = rng.normal(size=(1500, 1))
            n = int(np.ceil(1500 * out_rate / in_rate))
            whole_r = SincResampler(in_rate, out_rate, 1)
            whole_r.feed(src)
            whole_r.feed(np.zeros((whole_r.required_input_for(n) - 1500, 1)))
            whole = whole_r.read(n)

            chunked_r = SincResampler(in_rate, out_rate, 1)
            outs, pos, made = [], 0, 0
            while made < n:
                b = min(7, n - made)
                need = chunked_r.required_input_for(b)
                take = min(need, 1500)
                if take > pos:
                    chunked_r.feed(src[pos:take])
                    pos = take
                if chunked_r.fed < need:
                    chunked_r.feed(np.zeros((need - chunked_r.fed, 1)))
                outs.append(chunked_r.read(b))
                made += b
            np.testing.assert_array_equal(whole, np.concatenate(outs))

    def test_input_shorter_than_filter_still_renders(self) -> None:
        r = SincResampler(48000, 48000, 1)
        r.feed(np.ones((2, 1)))
        n = r.flush_len(2)
        r.feed(np.zeros((r.required_input_for(n) - r.fed, 1)))
        out = r.read(n)
        self.assertEqual(out.shape, (2, 1))
        self.assertTrue(np.all(np.isfinite(out)))

    def test_rejects_non_finite_and_bad_shape(self) -> None:
        r = SincResampler(48000, 24000, 1)
        with self.assertRaises(AudioError):
            r.feed(np.array([[np.nan]]))
        with self.assertRaises(AudioError):
            r.feed(np.ones((3, 2)))


class AutomationTest(unittest.TestCase):
    def test_hold_extend_and_interpolation(self) -> None:
        curve = GainCurve([(10, -6.0), (20, 0.0)])
        db = curve.sample_db(np.array([0, 10, 15, 20, 40]))
        np.testing.assert_array_equal(db, [-6.0, -6.0, -3.0, 0.0, 0.0])
        linear = curve.sample_linear(np.array([10]))
        np.testing.assert_allclose(linear, [10 ** (-6 / 20)])

    def test_empty_curve_is_unity(self) -> None:
        curve = GainCurve.from_manifest(None, 100, "t")
        np.testing.assert_array_equal(
            curve.sample_linear(np.arange(5)), np.ones(5)
        )

    def test_validation_names_track_and_field(self) -> None:
        for bad in (
            [{"frame": 1, "gain_db": float("nan")}],
            [{"frame": 5, "gain_db": 0.0}, {"frame": 5, "gain_db": -1.0}],
            [{"frame": 101, "gain_db": 0.0}],
            [{"frame": -1, "gain_db": 0.0}],
        ):
            with self.assertRaises(ManifestError) as ctx:
                GainCurve.from_manifest(bad, 100, "voc")
            self.assertIn("voc", str(ctx.exception))


class ManifestValidationTest(unittest.TestCase):
    def setUp(self) -> None:
        self._tmp = tempfile.TemporaryDirectory()
        self.dir = Path(self._tmp.name)
        sf.write(str(self.dir / "a.wav"),
                 np.zeros((100, 2), dtype=np.float64), 48000)
        sf.write(str(self.dir / "m.wav"),
                 np.zeros((100, 1), dtype=np.float64), 22050)

    def tearDown(self) -> None:
        self._tmp.cleanup()

    def _load(self, tracks, **overrides):
        payload = {"target_rate": 48000, "total_frames": 1000,
                   "channels": 2, "tracks": tracks}
        payload.update(overrides)
        path = self.dir / "m.json"
        path.write_text(json.dumps(payload))
        return load_manifest(path)

    def test_duplicate_ids_missing_files_matrix_and_corrupt_wav(self) -> None:
        with self.assertRaises(ManifestError):
            self._load([{"id": "x", "file": "a.wav", "start_frame": 0},
                        {"id": "x", "file": "a.wav", "start_frame": 0}])
        with self.assertRaises(ManifestError):
            self._load([{"id": "x", "file": "ghost.wav", "start_frame": 0}])
        with self.assertRaises(ManifestError):  # mono->stereo needs matrix
            self._load([{"id": "x", "file": "m.wav", "start_frame": 0}])
        with self.assertRaises(ManifestError):  # wrong matrix shape
            self._load([{"id": "x", "file": "m.wav", "start_frame": 0,
                         "channel_matrix": [[1.0]]}])
        (self.dir / "bad.wav").write_bytes(b"RIFFnotawav")
        with self.assertRaises(AudioError):
            self._load([{"id": "x", "file": "bad.wav", "start_frame": 0,
                         "channel_matrix": [[1.0, 1.0]]}])

    def test_empty_wav_and_non_finite_guard(self) -> None:
        sf.write(str(self.dir / "empty.wav"),
                 np.zeros((0, 2), dtype=np.float64), 48000)
        with self.assertRaises(AudioError):
            self._load([{"id": "e", "file": "empty.wav",
                         "start_frame": 0}])


class GraphTest(unittest.TestCase):
    def test_cycle_and_unknown_node_detection(self) -> None:
        class Dummy(Node):
            def evaluate(self, start, length, *values):
                return np.zeros((length, 1))

        x, y = Dummy("x"), Dummy("y")
        x.inputs, y.inputs = [y], [x]
        with self.assertRaises(ManifestError):
            Graph([x, y], "x")
        with self.assertRaises(ManifestError):
            Graph([Dummy("z")], "missing")

    def test_track_node_absolute_automation_across_blocks(self) -> None:
        src = np.ones((100, 1))
        node = TrackNode(
            "track:t", src, 48000, 48000, 10, np.array([[1.0, 1.0]]),
            GainCurve([(0, 0.0), (110, -20.0)]), 2,
        )
        bus = SumNode("bus", 2)
        bus.inputs = [node]
        graph = Graph([node, bus], "bus")
        first = graph.evaluate_block(0, 60)
        second = graph.evaluate_block(60, 60)
        graph.reset()
        whole = graph.evaluate_block(0, 120)
        np.testing.assert_array_equal(np.concatenate([first, second]), whole)


class OutputCleanupTest(unittest.TestCase):
    def test_failed_write_leaves_no_temp_and_raises_exit4(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            target = Path(tmp) / "ro" / "x.wav"
            target.parent.mkdir()
            os.chmod(target.parent, stat.S_IRUSR | stat.S_IXUSR)
            try:
                with self.assertRaises(WriteError):
                    write_wav(target, np.zeros((4, 2)), 48000, force=False)
                leftovers = [
                    p.name for p in target.parent.glob("*.tmp")
                ]
                self.assertEqual(leftovers, [])
            finally:
                os.chmod(target.parent, stat.S_IRWXU)


class VerifyFailureTest(unittest.TestCase):
    def test_tolerance_breach_raises_stream_verify_error(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "a.wav"
            sf.write(str(path), np.ones((64, 1)) * 0.1, 48000)
            manifest = {
                "target_rate": 48000, "total_frames": 128, "channels": 1,
                "tracks": [{"id": "a", "file": "a.wav",
                            "start_frame": 0}],
            }
            (Path(tmp) / "m.json").write_text(json.dumps(manifest))
            loaded = load_manifest(Path(tmp) / "m.json")
            graph = build_graph(loaded)
            master = graph.evaluate_block(0, 128)
            corrupted = master.copy()
            corrupted[0, 0] += 1.0  # force a breach far above 1e-6
            with self.assertRaises(StreamVerifyError):
                verify_against_offline(corrupted, graph, 128, 7)


class CliExitCodeTest(unittest.TestCase):
    def test_parameter_errors_return_exit_2(self) -> None:
        from trackmix.cli import main

        self.assertEqual(main(["demo", "--tracks", "0"]), 2)
        self.assertEqual(main(["demo", "--block-frames", "-1"]), 2)
        self.assertEqual(main(["demo", "--frames", "abc"]), 2)
        self.assertEqual(main([]), 2)

    def test_write_failure_returns_exit_4(self) -> None:
        from trackmix.cli import main

        with tempfile.TemporaryDirectory() as tmp:
            locked = Path(tmp) / "locked"
            locked.mkdir()
            os.chmod(locked, stat.S_IRUSR | stat.S_IXUSR)
            try:
                code = main([
                    "demo", "--output-dir", str(locked / "out"),
                    "--seed", "1", "--frames", "1200", "--tracks", "2",
                ])
                self.assertEqual(code, 4)
            finally:
                os.chmod(locked, stat.S_IRWXU)

    def test_verification_failure_returns_exit_5(self) -> None:
        from trackmix import cli as cli_module
        from trackmix.cli import main

        def boom(*_args, **_kwargs):
            raise StreamVerifyError("forced divergence")

        original = cli_module.verify_against_offline
        cli_module.verify_against_offline = boom
        try:
            with tempfile.TemporaryDirectory() as tmp:
                wav = Path(tmp) / "a.wav"
                sf.write(str(wav), np.ones((32, 1)) * 0.1, 48000)
                manifest = {
                    "target_rate": 48000, "total_frames": 64,
                    "channels": 1,
                    "tracks": [{"id": "a", "file": "a.wav",
                                "start_frame": 0}],
                }
                manifest_path = Path(tmp) / "m.json"
                manifest_path.write_text(json.dumps(manifest))
                code = main([
                    "mix", str(manifest_path), "--verify-stream",
                    "--output-dir", str(Path(tmp) / "render"),
                ])
                self.assertEqual(code, 5)
        finally:
            cli_module.verify_against_offline = original


if __name__ == "__main__":
    unittest.main()
