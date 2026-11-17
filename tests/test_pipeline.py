"""End-to-end pipeline tests: offline/streaming parity and boundaries."""

from __future__ import annotations

import json
import tempfile
import unittest
from pathlib import Path

import numpy as np
import soundfile as sf

from trackmix.cli import main
from trackmix.graph import build_graph
from trackmix.manifest import load_manifest
from trackmix.streaming import run_streaming, verify_against_offline
from trackmix.offline import run_offline


def _write_wav(directory: Path, name: str, data: np.ndarray, rate: int) -> Path:
    path = directory / name
    sf.write(str(path), data, rate, subtype="FLOAT", format="WAV")
    return path


def _manifest(directory: Path, tracks: list[dict], **overrides) -> Path:
    payload = {
        "target_rate": 48000,
        "total_frames": 4000,
        "channels": 2,
        "tracks": tracks,
    }
    payload.update(overrides)
    path = directory / "manifest.json"
    path.write_text(json.dumps(payload), encoding="utf-8")
    return path


class PipelineTest(unittest.TestCase):
    def setUp(self) -> None:
        self._tmp = tempfile.TemporaryDirectory()
        self.dir = Path(self._tmp.name)
        rng = np.random.default_rng(7)
        self.stereo = rng.normal(size=(2000, 2)) * 0.3
        self.mono = rng.normal(size=(3000, 1)) * 0.3
        _write_wav(self.dir, "s48.wav", self.stereo, 48000)
        _write_wav(self.dir, "m44.wav", self.mono, 44100)

    def tearDown(self) -> None:
        self._tmp.cleanup()

    def _load(self, **overrides):
        tracks = [
            {"id": "a", "file": "s48.wav", "start_frame": 0},
            {
                "id": "b",
                "file": "m44.wav",
                "start_frame": 128,
                "channel_matrix": [[0.6, 0.4]],
                "automation": [
                    {"frame": 0, "gain_db": -6.0},
                    {"frame": 2000, "gain_db": 0.0},
                    {"frame": 4000, "gain_db": -12.0},
                ],
            },
        ]
        return load_manifest(_manifest(self.dir, tracks, **overrides))

    def test_offline_shape_and_finite(self) -> None:
        manifest = self._load()
        graph = build_graph(manifest)
        master, report = run_offline(
            graph, manifest.total_frames, manifest.target_rate,
            self.dir / "render", "master", force=True,
        )
        self.assertEqual(master.shape, (4000, 2))
        self.assertTrue(np.all(np.isfinite(master)))
        self.assertEqual(len(report["tracks"]), 2)
        for row in report["tracks"]:
            self.assertIn("peak_dbfs", row)
            self.assertIn("rms_dbfs", row)
        self.assertTrue((self.dir / "render" / "master.wav").exists())
        self.assertTrue((self.dir / "render" / "master.json").exists())

    def test_streaming_matches_offline_block_sizes(self) -> None:
        manifest = self._load()
        for block in (1, 2, 3, 7, 257, 4001):
            graph = build_graph(manifest)
            master, _ = run_offline(
                graph, manifest.total_frames, manifest.target_rate,
                self.dir / f"r{block}", "master", force=True,
            )
            check = verify_against_offline(
                master, graph, manifest.total_frames, block
            )
            self.assertEqual(check["frames"], 4000)
            self.assertLessEqual(check["max_abs_diff"], 1e-6)

    def test_streaming_concat_frame_count_exact(self) -> None:
        manifest = self._load()
        graph = build_graph(manifest)
        streamed = run_streaming(graph, manifest.total_frames, 257)
        self.assertEqual(streamed.shape, (4000, 2))
        graph.reset()
        offline = graph.evaluate_block(0, 4000)
        np.testing.assert_array_equal(streamed, offline)

    def test_block_straddles_track_end_boundary(self) -> None:
        # Track 'a' ends well before the timeline; blocks straddle it.
        manifest = self._load()
        graph = build_graph(manifest)
        offline = graph.evaluate_block(0, 4000)
        graph.reset()
        chunks = []
        for start in range(0, 4000, 997):  # odd, non-dividing stride
            chunks.append(graph.evaluate_block(start, min(997, 4000 - start)))
        np.testing.assert_array_equal(offline, np.concatenate(chunks))

    def test_empty_timeline_region_is_silent_not_nan(self) -> None:
        # One short track at the very start; the tail region must be zero.
        tracks = [{"id": "a", "file": "s48.wav", "start_frame": 0}]
        manifest = load_manifest(
            _manifest(self.dir, tracks, total_frames=10_000)
        )
        graph = build_graph(manifest)
        master = graph.evaluate_block(0, 10_000)
        self.assertEqual(master.shape, (10_000, 2))
        self.assertTrue(np.all(master[3000:] == 0.0))
        self.assertTrue(np.all(np.isfinite(master)))

    def test_cli_demo_acceptance_fields(self) -> None:
        out = self.dir / "demo"
        argv = [
            "demo", "--seed", "23", "--tracks", "4", "--frames", "9600",
            "--rate", "48000", "--block-frames", "257",
            "--automation-step", "1200", "--stress-clip",
            "--verify-stream", "--output-dir", str(out),
        ]
        code = main(argv)
        self.assertEqual(code, 0)
        report = json.loads((out / "master.json").read_text())
        self.assertEqual(len(report["tracks"]), 4)
        self.assertGreater(report["clipping"]["sample_count"], 0)
        self.assertEqual(report["stream_check"]["frames"], 9600)
        self.assertLessEqual(report["stream_check"]["max_abs_diff"], 1e-6)
        # automation_samples: 9 points per track at step 1200 over 9600.
        self.assertEqual(len(report["automation_samples"]), 36)

    def test_output_refuses_overwrite_without_force(self) -> None:
        from trackmix.errors import WriteError

        manifest = self._load()
        graph = build_graph(manifest)
        run_offline(
            graph, manifest.total_frames, manifest.target_rate,
            self.dir / "render", "master", force=False,
        )
        graph.reset()
        with self.assertRaises(WriteError):
            run_offline(
                graph, manifest.total_frames, manifest.target_rate,
                self.dir / "render", "master", force=False,
            )
        # force=True overwrites cleanly
        run_offline(
            graph, manifest.total_frames, manifest.target_rate,
            self.dir / "render", "master", force=True,
        )


if __name__ == "__main__":
    unittest.main()
