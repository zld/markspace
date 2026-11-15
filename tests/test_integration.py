"""Integration tests: graph parity, engine writes, error codes and demo."""

from __future__ import annotations

import json
import os
import tempfile
import unittest
from pathlib import Path

import numpy as np
import soundfile as sf

from audiomix import EXIT_CONFIG, AudioProcessError, WriteError
from audiomix.cli import main
from audiomix.demo import build_project, generate_sources
from audiomix.engine import write_float_wav
from audiomix.graph import CHANNELS, MixGraph
from audiomix.model import validate_project
from audiomix.stream import StreamRenderer, parity_error, render_stream

from tests._fixtures import TempTree, write_audio


def _render(project, block):
    with MixGraph(project) as graph:
        chunks = []
        while True:
            block_data = graph.read_block(block)
            if block_data.shape[0] == 0:
                break
            chunks.append(block_data)
    return np.concatenate(chunks)


class GraphAndStreamTests(unittest.TestCase):
    def test_block_size_invariance_is_exact(self):
        with TempTree() as fixture:
            project = fixture.project()
            reference = _render(project, 8192)
            for block in (1, 3, 257, 4096):
                data = _render(project, block)
                self.assertEqual(data.shape, (project.frames, CHANNELS))
                np.testing.assert_array_equal(reference, data)

    def test_stream_report_and_automation_samples(self):
        with TempTree() as fixture:
            project = fixture.project()
            data, report = render_stream(project, 257)
            with StreamRenderer(project, 1024) as renderer:
                other = renderer.render_to_array()
            self.assertLessEqual(parity_error(data, other), 1e-6)
            self.assertEqual(report["frames"], project.frames)
            self.assertEqual(len(report["tracks"]), 3)
            for track in report["tracks"]:
                self.assertIn("peak", track)
                self.assertIn("rms", track)
                self.assertEqual(len(track["automation_samples"]), 3)

    def test_tail_beyond_project_is_truncated(self):
        with TempTree() as fixture:
            long_path = fixture.tmp / "long.wav"
            write_audio(long_path, 48000, 1, fixture.frames * 2, seed=9)
            project = validate_project({
                "sample_rate": fixture.rate,
                "frames": fixture.frames,
                "tracks": [{"id": "long", "path": str(long_path)}],
            })
            data, _ = render_stream(project, 512)
            self.assertEqual(data.shape, (fixture.frames, CHANNELS))

    def test_mono_duplicated_to_stereo(self):
        with TempTree() as fixture:
            mono_path = fixture.tmp / "onlymono.wav"
            write_audio(mono_path, 48000, 1, 500, seed=4)
            project = validate_project({
                "sample_rate": 48000,
                "frames": 500,
                "tracks": [{"id": "m", "path": str(mono_path)}],
            })
            data, _ = render_stream(project, 128)
            np.testing.assert_array_equal(data[:, 0], data[:, 1])


class EngineTests(unittest.TestCase):
    def test_writes_float_wav_and_counts_clips_without_clamping(self):
        with TempTree() as fixture:
            raw = fixture.raw()
            for track in raw["tracks"]:
                track["gain"] = 4.0
            project = validate_project(raw)
            wav = fixture.tmp / "nested" / "mix.wav"
            with MixGraph(project) as graph:
                report = write_float_wav(
                    wav, graph, 257, project.automation_sample_frames
                )
            self.assertEqual(report["frames"], project.frames)
            self.assertGreater(report["clip_samples"], 0)
            disk, rate = sf.read(str(wav), always_2d=True)
            self.assertEqual(rate, project.sample_rate)
            self.assertEqual(disk.shape, (project.frames, CHANNELS))
            self.assertEqual(
                int(np.count_nonzero(np.abs(disk) > 1.0)),
                report["clip_samples"],
            )
            leftovers = [p for p in wav.parent.iterdir() if p.name.startswith(".")]
            self.assertEqual(leftovers, [])

    def test_write_failure_cleans_temp_and_raises_write_error(self):
        with TempTree() as fixture:
            target = fixture.tmp / "readonly" / "mix.wav"
            target.parent.mkdir()
            os.chmod(target.parent, 0o500)
            try:
                with MixGraph(fixture.project()) as graph:
                    with self.assertRaises(WriteError):
                        write_float_wav(target, graph, 257)
                leftovers = [
                    p for p in target.parent.iterdir() if p.name.startswith(".")
                ]
                self.assertEqual(leftovers, [])
            finally:
                os.chmod(target.parent, 0o700)


class ErrorReportingTests(unittest.TestCase):
    def test_corrupt_audio_reports_track_id(self):
        with TempTree() as fixture:
            bad = fixture.tmp / "broken.wav"
            bad.write_bytes(b"RIFFxxxxWAVEnot-really-audio")
            project = validate_project({
                "sample_rate": 48000,
                "frames": 1000,
                "tracks": [{"id": "broken", "path": str(bad)}],
            })
            with MixGraph(project) as graph:
                with self.assertRaisesRegex(AudioProcessError, "broken"):
                    graph.read_block(128)

    def test_unsupported_channel_count_reports_track_id(self):
        with TempTree() as fixture:
            three = fixture.tmp / "three.wav"
            sf.write(str(three), np.zeros((100, 3)), 48000)
            project = validate_project({
                "sample_rate": 48000,
                "frames": 1000,
                "tracks": [{"id": "tri", "path": str(three)}],
            })
            with MixGraph(project) as graph:
                with self.assertRaisesRegex(AudioProcessError, "tri"):
                    graph.read_block(64)

    def test_missing_source_reports_track_id(self):
        with TempTree() as fixture:
            project = validate_project({
                "sample_rate": 48000,
                "frames": 1000,
                "tracks": [{"id": "ghost", "path": str(fixture.tmp / "ghost.wav")}],
            })
            with MixGraph(project) as graph:
                with self.assertRaisesRegex(AudioProcessError, "ghost"):
                    graph.read_block(32)

    def test_cli_config_exit_code_and_demo_contract(self):
        with TempTree() as fixture:
            bad_project = fixture.tmp / "bad.json"
            bad_project.write_text(json.dumps({"sample_rate": 48000, "frames": -1}))
            code = main([
                "render", "--project", str(bad_project),
                "--wav", str(fixture.tmp / "x.wav"),
                "--report", str(fixture.tmp / "x.json"),
            ])
            self.assertEqual(code, EXIT_CONFIG)

            wav = fixture.tmp / "demo.wav"
            report_path = fixture.tmp / "demo.json"
            code = main([
                "demo", "--seed", "17", "--rate", "48000", "--seconds", "1",
                "--block-frames", "257", "--wav", str(wav),
                "--report", str(report_path),
            ])
            self.assertEqual(code, 0)
            report = json.loads(report_path.read_text())
            self.assertEqual(report["frames"], 48000)
            self.assertEqual(len(report["tracks"]), 3)
            self.assertLessEqual(report["parity_max_abs"], 1e-6)
            self.assertTrue(wav.exists())


class DemoFixtureTests(unittest.TestCase):
    def test_seeded_fixtures_are_reproducible(self):
        with tempfile.TemporaryDirectory(prefix="audiomix-demo-") as tmp:
            first = generate_sources(tmp, 17)
            voice_bytes = Path(first["voice"]).read_bytes()
            project = build_project(first, 48000, 3.0)
            self.assertEqual(project.frames, 144000)
            self.assertEqual(len(project.tracks), 3)
        with tempfile.TemporaryDirectory(prefix="audiomix-demo-") as tmp2:
            second = generate_sources(tmp2, 17)
            self.assertEqual(Path(second["voice"]).read_bytes(), voice_bytes)


if __name__ == "__main__":
    unittest.main()
