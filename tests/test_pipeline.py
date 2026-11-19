"""End-to-end and boundary tests for the render pipeline."""

from __future__ import annotations

import json
import tempfile
import unittest
from pathlib import Path

import numpy as np
import soundfile as sf

from mixforge.automation import evaluate
from mixforge.model import AutomationCurve, AutomationNode, load_manifest
from mixforge.render import render, render_offline
from mixforge.stream import render_stream

SR = 48000


def write_wav(directory: Path, name: str, data: np.ndarray, rate: int = SR) -> Path:
    path = directory / name
    sf.write(str(path), data, rate, format="WAV", subtype="FLOAT")
    return path


def write_manifest(directory: Path, payload: dict) -> Path:
    path = directory / "session.json"
    path.write_text(json.dumps(payload), encoding="utf-8")
    return path


def base_manifest(tracks: list[dict], *, rate: int = SR, channels: int = 2,
                  block_size: int = 1024, limiter: bool = False) -> dict:
    return {
        "name": "test",
        "sample_rate": rate,
        "channels": channels,
        "block_size": block_size,
        "limiter": limiter,
        "tracks": tracks,
    }


class PipelineTests(unittest.TestCase):
    def setUp(self) -> None:
        self._tmp = tempfile.TemporaryDirectory()
        self.tmp = Path(self._tmp.name)

    def tearDown(self) -> None:
        self._tmp.cleanup()

    def _tone(self, frequency: float, seconds: float, amplitude: float,
              rate: int = SR) -> np.ndarray:
        frames = int(round(seconds * rate))
        times = np.arange(frames, dtype=np.float64) / rate
        return (amplitude * np.sin(2 * np.pi * frequency * times))[:, np.newaxis]

    def test_offline_and_stream_are_bit_identical(self) -> None:
        write_wav(self.tmp, "a.wav", self._tone(220, 1.0, 0.5))
        manifest = write_manifest(
            self.tmp,
            base_manifest([
                {"name": "a", "path": "a.wav", "gain": 0.9,
                 "channel_map": [[0, 0], [0, 1]]},
            ]),
        )
        config = load_manifest(manifest)
        offline = render_offline(config)
        streaming = render_stream(config, 257)
        self.assertTrue(np.array_equal(offline.output, streaming.output))
        self._assert_metrics_match(
            offline.track_metrics["a"], streaming.track_metrics["a"]
        )
        self._assert_metrics_match(
            offline.bus_pre_limiter, streaming.bus_pre_limiter
        )

    def _assert_metrics_match(self, left, right) -> None:
        # Samples are bit-identical; only RMS can differ in the last ULP
        # because block partitions change the per-chunk sums.
        self.assertEqual(left.peak, right.peak)
        self.assertEqual(left.frames, right.frames)
        self.assertEqual(left.clips, right.clips)
        self.assertAlmostEqual(left.rms, right.rms, places=12)

    def test_automation_node_on_block_boundary_is_continuous(self) -> None:
        # Node at exactly frame 514: block size 257 -> two full blocks.
        node_frame = 514
        write_wav(self.tmp, "a.wav", np.ones((2048, 1)))
        manifest = write_manifest(
            self.tmp,
            base_manifest([{
                "name": "a", "path": "a.wav",
                "channel_map": [[0, 0]],
                "automation": {"interpolation": "linear", "nodes": [
                    {"time": 0.0, "gain": 0.0},
                    {"time": node_frame / SR, "gain": 1.0},
                ]},
            }], channels=1),
        )
        config = load_manifest(manifest)
        aligned = render_stream(config, 257).output[:, 0]
        shifted = render_stream(config, 251).output[:, 0]
        self.assertTrue(np.array_equal(aligned, shifted))
        self.assertEqual(aligned[node_frame], 1.0)
        self.assertEqual(aligned[node_frame - 1],
                         (node_frame - 1) / node_frame)
        # No level jump across the boundary.
        self.assertLess(abs(np.diff(aligned).max()), 1.0 / node_frame + 1e-12)

    def test_resample_to_project_rate_keeps_duration(self) -> None:
        low_rate = 24000
        write_wav(self.tmp, "a.wav", self._tone(330, 1.0, 0.5, low_rate), low_rate)
        manifest = write_manifest(
            self.tmp,
            base_manifest([
                {"name": "a", "path": "a.wav",
                 "channel_map": [[0, 0], [0, 1]]},
            ]),
        )
        result = render_stream(load_manifest(manifest), 512)
        self.assertEqual(result.frames, SR)
        self.assertEqual(result.output.shape, (SR, 2))
        self.assertGreater(result.bus_post_limiter.peak, 0.4)

    def test_mono_to_stereo_channel_mapping(self) -> None:
        write_wav(self.tmp, "a.wav", np.ones((128, 1)))
        manifest = write_manifest(
            self.tmp,
            base_manifest([
                {"name": "a", "path": "a.wav",
                 "channel_map": [[0, 0], [0, 1]]},
            ], channels=2),
        )
        result = render_offline(load_manifest(manifest))
        np.testing.assert_array_equal(result.output[:, 0], result.output[:, 1])
        self.assertEqual(result.bus_post_limiter.peak, 1.0)

    def test_tail_shorter_than_one_block_finishes_cleanly(self) -> None:
        # 100 frames with block size 257: only a partial final block.
        write_wav(self.tmp, "a.wav", np.full((100, 1), 0.7))
        manifest = write_manifest(
            self.tmp,
            base_manifest([
                {"name": "a", "path": "a.wav", "channel_map": [[0, 0]]},
            ], channels=1, block_size=257),
        )
        result = render_stream(load_manifest(manifest), 257)
        self.assertEqual(result.frames, 100)
        self.assertEqual(result.output.shape, (100, 1))
        # WAV fixtures are float32, so allow storage-level rounding only.
        np.testing.assert_allclose(result.output[:100, 0], 0.7, rtol=0, atol=1e-6)

    def test_empty_session_yields_stable_zero_frame_output(self) -> None:
        manifest = write_manifest(self.tmp, base_manifest([]))
        config = load_manifest(manifest)
        for block_size in (1, 257, 1024):
            result = render(config, block_size)
            self.assertEqual(result.frames, 0)
            self.assertEqual(result.output.shape, (0, 2))
            self.assertEqual(result.bus_post_limiter.peak, 0.0)
            self.assertEqual(result.bus_post_limiter.rms, 0.0)
            self.assertEqual(result.bus_post_limiter.clips, 0)

    def test_hold_and_linear_interpolation_with_endpoint_hold(self) -> None:
        nodes = (
            AutomationNode(0.0, 1.0),
            AutomationNode(1.0, 0.0),
        )
        times = np.array([-1.0, 0.0, 0.5, 1.0, 2.0])
        linear = evaluate(AutomationCurve(nodes, "linear"), times)
        np.testing.assert_allclose(
            linear, [1.0, 1.0, 0.5, 0.0, 0.0], atol=1e-12
        )
        hold = evaluate(AutomationCurve(nodes, "hold"), times)
        np.testing.assert_allclose(hold, [1.0, 1.0, 1.0, 0.0, 0.0])


if __name__ == "__main__":
    unittest.main()
