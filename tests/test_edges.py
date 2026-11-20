"""Edge-case tests: manifest/graph validation and boundary handling."""

from __future__ import annotations

import json
import tempfile
import unittest
from pathlib import Path

import numpy as np
import soundfile as sf

from mixforge.automation import GainCurve
from mixforge.graph import MixGraph, Node, build_graph
from mixforge.manifest import load_manifest
from mixforge.model import (
    AudioIOError,
    AutomationError,
    GraphError,
    Keyframe,
    ManifestError,
    Project,
    Track,
)
from mixforge.render import render


def _write_wav(path: Path, data: np.ndarray, rate: int) -> None:
    sf.write(str(path), data, rate, format="WAV", subtype="FLOAT")


class EdgeCaseTest(unittest.TestCase):
    def setUp(self) -> None:
        self._tmp = tempfile.TemporaryDirectory()
        self.root = Path(self._tmp.name)
        _write_wav(self.root / "mono.wav",
                   np.sin(np.linspace(0, 1, 4800)) * 0.5, 48000)
        _write_wav(self.root / "stereo.wav",
                   np.zeros((4800, 2)) + 0.1, 48000)

    def tearDown(self) -> None:
        self._tmp.cleanup()

    def _manifest(self, tracks: list[dict]) -> Path:
        path = self.root / "manifest.json"
        path.write_text(json.dumps({"tracks": tracks}), encoding="utf-8")
        return path

    def test_duplicate_track_id_rejected(self) -> None:
        path = self._manifest([
            {"id": "a", "path": "mono.wav"},
            {"id": "a", "path": "stereo.wav"},
        ])
        with self.assertRaises(ManifestError):
            load_manifest(path)

    def test_missing_file_rejected(self) -> None:
        path = self._manifest([{"id": "a", "path": "nope.wav"}])
        with self.assertRaises(ManifestError):
            load_manifest(path)

    def test_escaping_path_rejected(self) -> None:
        path = self._manifest([{"id": "a", "path": "../outside.wav"}])
        with self.assertRaises(ManifestError):
            load_manifest(path)

    def test_invalid_values_rejected(self) -> None:
        bad_manifests = [
            [{"id": "a", "path": "mono.wav", "start": -1.0}],
            [{"id": "a", "path": "mono.wav", "start": "soon"}],
            [{"id": "a", "path": "mono.wav",
              "gain": [{"time": 0.0, "db": "loud"}]}],
            [{"id": "a", "path": "mono.wav",
              "gain": [{"time": -0.5, "db": 0.0}]}],
            [{"id": "a", "path": "mono.wav",
              "gain": [{"time": 0.0, "db": 1.0}, {"time": 0.0, "db": 2.0}]}],
        ]
        for tracks in bad_manifests:
            with self.subTest(tracks=tracks):
                with self.assertRaises(ManifestError):
                    load_manifest(self._manifest(tracks))

    def test_malformed_json_rejected(self) -> None:
        path = self.root / "manifest.json"
        path.write_text("{not json", encoding="utf-8")
        with self.assertRaises(ManifestError):
            load_manifest(path)

    def test_duplicate_keyframe_times_rejected(self) -> None:
        with self.assertRaises(AutomationError):
            GainCurve([Keyframe(0.0, 0.0), Keyframe(0.0, 3.0)])

    def test_curve_endpoint_hold_and_interpolation(self) -> None:
        curve = GainCurve([Keyframe(1.0, 0.0), Keyframe(2.0, 6.0)])
        self.assertAlmostEqual(float(curve.db_at(0.5)), 0.0)   # before: hold
        self.assertAlmostEqual(float(curve.db_at(1.5)), 3.0)   # linear in dB
        self.assertAlmostEqual(float(curve.db_at(9.0)), 6.0)   # after: hold

    def test_unsupported_channel_layout_rejected(self) -> None:
        _write_wav(self.root / "quad.wav", np.zeros((480, 4)), 48000)
        path = self._manifest([{"id": "q", "path": "quad.wav"}])
        project = load_manifest(path)
        with self.assertRaises(AudioIOError):
            build_graph(project, 48000, 4800)

    def test_graph_cycle_and_dangling_rejected(self) -> None:
        graph = MixGraph(48000, 4800)
        graph.add_node(Node("a", "master", inputs=("b",)))
        graph.add_node(Node("b", "master", inputs=("a",)))
        with self.assertRaises(GraphError):
            graph.validate()
        graph = MixGraph(48000, 4800)
        graph.add_node(Node("a", "master", inputs=("ghost",)))
        with self.assertRaises(GraphError):
            graph.validate()

    def _project(self, track: Track) -> Project:
        return Project(tracks=(track,))

    def test_non_integer_keyframe_frame_time(self) -> None:
        # 0.0111s at 48kHz -> frame 532.8; results must not depend on blocks.
        track = Track("t", self.root / "mono.wav", 0.0,
                      (Keyframe(0.0, 0.0), Keyframe(0.0111, -6.0)))
        outs = []
        for block in (1024, 333):
            out = self.root / f"ni-{block}.wav"
            render(self._project(track), out, 48000, 0.2,
                   mode="stream", block_size=block)
            data, _ = sf.read(out, dtype="float64", always_2d=True)
            outs.append(data)
        np.testing.assert_array_equal(outs[0], outs[1])

    def test_keyframe_on_block_boundary(self) -> None:
        boundary = 1024 / 48000.0  # exactly one block
        track = Track("t", self.root / "mono.wav", 0.0,
                      (Keyframe(0.0, 0.0), Keyframe(boundary, -12.0)))
        outs = []
        for block in (1024, 512, 2048):
            out = self.root / f"bb-{block}.wav"
            render(self._project(track), out, 48000, 0.2,
                   mode="offline", block_size=block)
            data, _ = sf.read(out, dtype="float64", always_2d=True)
            outs.append(data)
        np.testing.assert_array_equal(outs[0], outs[1])
        np.testing.assert_array_equal(outs[0], outs[2])

    def test_partial_tail_block_and_frame_count(self) -> None:
        track = Track("t", self.root / "mono.wav", 0.0, ())
        out = self.root / "tail.wav"
        report = render(self._project(track), out, 48000, 0.333,
                        mode="stream", block_size=1024)
        self.assertEqual(report["frames"], 15984)  # 0.333 * 48000, not block-aligned
        data, _ = sf.read(out, dtype="float64", always_2d=True)
        self.assertEqual(data.shape, (15984, 2))

    def test_track_truncated_at_timeline_end(self) -> None:
        track = Track("t", self.root / "mono.wav", 0.09, ())  # 0.09 + 0.1 > 0.15
        out = self.root / "trunc.wav"
        report = render(self._project(track), out, 48000, 0.15,
                        mode="offline", block_size=1024)
        self.assertEqual(report["frames"], 7200)
        data, _ = sf.read(out, dtype="float64", always_2d=True)
        self.assertEqual(data.shape, (7200, 2))
        self.assertGreater(np.abs(data[-1]).max(), 0.0)  # cut mid-content

    def test_track_beyond_timeline_is_silent(self) -> None:
        track = Track("t", self.root / "mono.wav", 5.0, ())
        out = self.root / "silent.wav"
        report = render(self._project(track), out, 48000, 0.1,
                        mode="offline", block_size=1024)
        self.assertEqual(report["mix"]["peak"], 0.0)

    def test_mono_duplicated_to_stereo(self) -> None:
        track = Track("t", self.root / "mono.wav", 0.0, ())
        out = self.root / "mono2st.wav"
        render(self._project(track), out, 48000, 0.1,
               mode="offline", block_size=1024)
        data, _ = sf.read(out, dtype="float64", always_2d=True)
        np.testing.assert_array_equal(data[:, 0], data[:, 1])

    def test_leading_silence_before_track_start(self) -> None:
        track = Track("t", self.root / "mono.wav", 0.05, ())
        out = self.root / "lead.wav"
        render(self._project(track), out, 48000, 0.1,
               mode="offline", block_size=1024)
        data, _ = sf.read(out, dtype="float64", always_2d=True)
        self.assertEqual(np.abs(data[:2400]).max(), 0.0)
        self.assertGreater(np.abs(data[2400:]).max(), 0.0)


if __name__ == "__main__":
    unittest.main()
