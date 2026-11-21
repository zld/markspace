"""End-to-end and unit tests for the audiomix pipeline."""

from __future__ import annotations

import json
import math
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

import numpy as np
import soundfile as sf

from audiomix.automation import automation_samples, build_curve, gain_at
from audiomix.cli import main
from audiomix.engine import render_project
from audiomix.graph import AudioIOError
from audiomix.model import (
    MAX_DURATION_SECONDS,
    ManifestError,
    check_duration,
    parse_manifest,
    resampled_frames,
)
from audiomix.resample import StreamingResampler, resampled_length

SR = 48000


def _write_wav(path: Path, data: np.ndarray, rate: int) -> None:
    sf.write(str(path), data, rate, subtype="FLOAT", format="WAV")


def _tone(frames: int, rate: int, freq: float = 220.0, amp: float = 0.5,
          channels: int = 2) -> np.ndarray:
    t = np.arange(frames, dtype=np.float64) / rate
    wave = amp * np.sin(2.0 * np.pi * freq * t)
    if channels == 1:
        return wave
    return np.column_stack([wave, 0.8 * wave])


def _manifest(tracks: list[dict], **extra) -> dict:
    payload = {"sample_rate": 48000, "channels": 2, "tracks": tracks}
    payload.update(extra)
    return payload


class ManifestTests(unittest.TestCase):
    def test_rejects_missing_field_with_track_and_field(self) -> None:
        manifest = _manifest([{"name": "voice", "path": "a.wav", "gain_db": 0.0}])
        manifest["tracks"][0].pop("path")
        with self.assertRaises(ManifestError) as ctx:
            parse_manifest(manifest)
        self.assertIn("voice", str(ctx.exception))
        self.assertIn("path", str(ctx.exception))

    def test_rejects_duplicate_track_names(self) -> None:
        manifest = _manifest([
            {"name": "voice", "path": "a.wav"},
            {"name": "voice", "path": "b.wav"},
        ])
        with self.assertRaises(ManifestError) as ctx:
            parse_manifest(manifest)
        self.assertIn("duplicate", str(ctx.exception))

    def test_rejects_non_finite_numbers_and_bad_pan(self) -> None:
        for bad in (float("nan"), float("inf")):
            manifest = _manifest([{"name": "v", "path": "a.wav", "gain_db": bad}])
            with self.assertRaises(ManifestError):
                parse_manifest(manifest)
        with self.assertRaises(ManifestError):
            parse_manifest(_manifest([{"name": "v", "path": "a.wav", "pan": 1.5}]))

    def test_rejects_out_of_range_gain(self) -> None:
        for bad in (121.0, -121.0, 1e308):
            manifest = _manifest([{"name": "v", "path": "a.wav", "gain_db": bad}])
            with self.assertRaises(ManifestError) as ctx:
                parse_manifest(manifest)
            self.assertIn("v", str(ctx.exception))
        manifest = _manifest([{
            "name": "v", "path": "a.wav",
            "automation": [{"t": 0.0, "gain_db": -130.0}],
        }])
        with self.assertRaises(ManifestError):
            parse_manifest(manifest)

    def test_rejects_non_increasing_automation(self) -> None:
        manifest = _manifest([{
            "name": "v", "path": "a.wav",
            "automation": [{"t": 1.0, "gain_db": 0.0}, {"t": 0.5, "gain_db": -3.0}],
        }])
        with self.assertRaises(ManifestError) as ctx:
            parse_manifest(manifest)
        self.assertIn("v", str(ctx.exception))

    def test_rejects_too_many_tracks_and_bad_rates(self) -> None:
        tracks = [{"name": f"t{i}", "path": "a.wav"} for i in range(33)]
        with self.assertRaises(ManifestError):
            parse_manifest(_manifest(tracks))
        with self.assertRaises(ManifestError):
            parse_manifest(_manifest([{"name": "v", "path": "a.wav"}], sample_rate=44100))

    def test_frame_clock_rounds_half_up(self) -> None:
        self.assertEqual(resampled_frames(88200, 44100), 96000)
        self.assertEqual(resampled_frames(44100, 44100), 48000)
        self.assertEqual(resampled_frames(1, 44100), 2)  # ceil(1.088) = 2

    def test_duration_ceiling_is_enforced(self) -> None:
        check_duration(int(MAX_DURATION_SECONDS * 48000), 48000)
        with self.assertRaises(ManifestError) as ctx:
            check_duration(int(MAX_DURATION_SECONDS * 48000) + 1, 48000, "long")
        self.assertIn("long", str(ctx.exception))
        self.assertIn("path", str(ctx.exception))


class AutomationTests(unittest.TestCase):
    def test_piecewise_linear_and_endpoint_hold(self) -> None:
        curve = build_curve([(0.0, 0.0), (1.0, -12.0), (2.0, 0.0)], 96000)
        self.assertEqual(curve.frames.tolist(), [0, 48000, 96000])
        values = gain_at(curve, np.array([0, 24000, 48000, 72000, 95999, 96000, 120000]))
        np.testing.assert_allclose(
            values, [0.0, -6.0, -12.0, -6.0, -0.00025, 0.0, 0.0], atol=1e-4
        )
        # Endpoint hold outside the defined range.
        held = gain_at(curve, np.array([-100]))
        np.testing.assert_allclose(held, [0.0])

    def test_automation_samples_are_spread_and_monotonic_frames(self) -> None:
        curve = build_curve([(0.0, 0.0), (0.5, -6.0)], 48000)
        samples = automation_samples(curve, 6)
        self.assertGreaterEqual(len(samples), 6)
        frames = [s[0] for s in samples]
        self.assertEqual(frames, sorted(set(frames)))
        self.assertIn(-6.0, [round(s[1], 9) for s in samples])

    def test_rounding_collision_rejected(self) -> None:
        with self.assertRaises(ValueError):
            build_curve([(0.0, 0.0), (1e-9, -1.0)], 48000)


class ResampleTests(unittest.TestCase):
    def _run(self, frames: int, rate_in: int, chunk: int) -> np.ndarray:
        rng = np.random.default_rng(7)
        x = rng.standard_normal((2, frames))
        resampler = StreamingResampler(rate_in, SR, 2)
        chunks = []
        for start in range(0, frames, chunk):
            chunks.append(resampler.pull(x[:, start:start + chunk]))
        chunks.append(resampler.finalize())
        out = np.concatenate(chunks, axis=1)
        expected = resampled_length(frames, rate_in, SR)
        self.assertEqual(out.shape, (2, expected))
        return out

    def test_chunking_never_changes_output(self) -> None:
        for rate_in in (44100, 48000, 96000, 32000):
            reference = self._run(5000, rate_in, 5000)
            for chunk in (1, 7, 257, 999):
                np.testing.assert_allclose(self._run(5000, rate_in, chunk), reference,
                                           atol=1e-12, rtol=0)

    def test_length_is_ceil(self) -> None:
        for frames in (1, 2, 3, 1001):
            self.assertEqual(resampled_length(frames, 44100, SR),
                             math.ceil(frames * SR / 44100))

    def test_dc_gain_is_unity_in_steady_state(self) -> None:
        resampler = StreamingResampler(44100, SR, 1)
        out = [resampler.pull(np.ones((1, 8000))), resampler.finalize()]
        y = np.concatenate(out, axis=1)
        # Windowed-sinc ripple stays within a tiny band of unity at DC.
        self.assertAlmostEqual(float(np.mean(y[0])), 1.0, places=4)
        self.assertLess(float(np.max(np.abs(y[0] - 1.0))), 1e-4)

    def _resample_tone(self, freq: float, rate_in: int, rate_out: int,
                       seconds: float = 0.5) -> np.ndarray:
        frames = int(rate_in * seconds)
        t = np.arange(frames, dtype=np.float64) / rate_in
        x = (0.5 * np.sin(2.0 * np.pi * freq * t))[None, :]
        resampler = StreamingResampler(rate_in, rate_out, 1)
        chunks = [resampler.pull(x[:, i:i + 1024])
                  for i in range(0, frames, 1024)]
        chunks.append(resampler.finalize())
        return np.concatenate(chunks, axis=1)[0]

    @staticmethod
    def _core_rms(y: np.ndarray) -> float:
        core = y[int(len(y) * 0.2):int(len(y) * 0.8)]
        return float(np.sqrt(np.mean(core * core)))

    def test_passband_tone_keeps_its_level(self) -> None:
        for rate_in, freq in ((96000, 10000.0), (44100, 5000.0), (8000, 1000.0)):
            y = self._resample_tone(freq, rate_in, SR)
            self.assertAlmostEqual(self._core_rms(y), 0.5 / math.sqrt(2), delta=0.002)

    def test_stopband_tone_is_suppressed(self) -> None:
        # 30 kHz cannot survive a 96k -> 48k conversion (Nyquist 24 kHz).
        y = self._resample_tone(30000.0, 96000, SR)
        self.assertLess(self._core_rms(y), 1e-3)


class GraphTests(unittest.TestCase):
    def setUp(self) -> None:
        self.tmp = tempfile.TemporaryDirectory()
        self.dir = Path(self.tmp.name)

    def tearDown(self) -> None:
        self.tmp.cleanup()

    def test_offline_and_streaming_match_across_rates(self) -> None:
        _write_wav(self.dir / "a.wav", _tone(24000, 48000), 48000)
        _write_wav(self.dir / "b.wav", _tone(22050, 44100, channels=1), 44100)
        manifest = _manifest([
            {"name": "a", "path": "a.wav", "pan": 0.2,
             "automation": [{"t": 0.0, "gain_db": 0.0}, {"t": 0.5, "gain_db": -6.0}]},
            {"name": "b", "path": "b.wav", "gain_db": -3.0},
        ])
        project = parse_manifest(manifest)
        report = render_project(
            project, [self.dir / "a.wav", self.dir / "b.wav"],
            self.dir / "mix.wav", chunk_frames=257,
        )
        self.assertEqual(report.frames_out, 24000)
        self.assertLessEqual(report.offline_stream_max_abs_diff, 1e-6)
        data, rate = sf.read(str(self.dir / "mix.wav"), dtype="float64", always_2d=True)
        self.assertEqual(data.shape, (24000, 2))
        self.assertEqual(rate, 48000)

    def test_last_partial_chunk(self) -> None:
        _write_wav(self.dir / "a.wav", _tone(1000, 48000), 48000)
        project = parse_manifest(_manifest([{"name": "a", "path": "a.wav"}]))
        first = render_project(project, [self.dir / "a.wav"], self.dir / "m1.wav", 333)
        second = render_project(project, [self.dir / "a.wav"], self.dir / "m2.wav", 1000)
        self.assertEqual(first.frames_out, 1000)
        self.assertEqual(second.offline_stream_max_abs_diff, 0.0)
        m1, _ = sf.read(str(self.dir / "m1.wav"), dtype="float64")
        m2, _ = sf.read(str(self.dir / "m2.wav"), dtype="float64")
        np.testing.assert_allclose(m1, m2, atol=1e-12)

    def test_clip_count_is_pre_limiter(self) -> None:
        _write_wav(self.dir / "hot.wav", _tone(2000, 48000, amp=0.9), 48000)
        manifest = _manifest([
            {"name": "one", "path": "hot.wav"},
            {"name": "two", "path": "hot.wav"},
        ])
        project = parse_manifest(manifest)
        report = render_project(project, [self.dir / "hot.wav"] * 2,
                                self.dir / "mix.wav", 128)
        self.assertGreater(report.clip_count, 0)
        data, _ = sf.read(str(self.dir / "mix.wav"), dtype="float64")
        self.assertLessEqual(float(np.max(np.abs(data))), 1.0)

    def test_rejects_empty_and_bad_channel_files(self) -> None:
        _write_wav(self.dir / "empty.wav", np.zeros((0, 2)), 48000)
        _write_wav(self.dir / "three.wav", np.zeros((100, 3)), 48000)
        for name in ("empty.wav", "three.wav"):
            project = parse_manifest(_manifest([{"name": name, "path": name}]))
            with self.assertRaises(AudioIOError):
                render_project(project, [self.dir / name], self.dir / "out.wav", 64)

    def test_rejects_non_finite_source_samples(self) -> None:
        data = np.zeros((100, 2))
        data[50, 0] = np.nan
        _write_wav(self.dir / "nan.wav", data, 48000)
        project = parse_manifest(_manifest([{"name": "nan", "path": "nan.wav"}]))
        with self.assertRaises(AudioIOError) as ctx:
            render_project(project, [self.dir / "nan.wav"], self.dir / "out.wav", 64)
        self.assertIn("nan", str(ctx.exception))
        self.assertFalse((self.dir / "out.wav").exists())

    def test_counting_channels_mapped_to_stereo(self) -> None:
        _write_wav(self.dir / "mono.wav", np.ones(500) * 0.5, 48000)
        project = parse_manifest(_manifest([{"name": "m", "path": "mono.wav"}]))
        report = render_project(project, [self.dir / "mono.wav"], self.dir / "out.wav", 64)
        data, _ = sf.read(str(self.dir / "out.wav"), dtype="float64")
        self.assertEqual(data.shape, (500, 2))
        self.assertGreater(report.tracks[0].peak, 0.0)

    def test_peak_and_rms_are_pre_bus_after_automation(self) -> None:
        data = np.zeros((100, 2))
        data[10, 0] = 0.5
        data[20, 1] = -0.25
        _write_wav(self.dir / "sparse.wav", data, 48000)
        project = parse_manifest(_manifest([
            {"name": "a", "path": "sparse.wav", "gain_db": 6.020599913279624},
        ]))
        report = render_project(project, [self.dir / "sparse.wav"],
                                self.dir / "out.wav", 7)
        track = report.tracks[0]
        self.assertAlmostEqual(track.peak, 1.0, places=6)
        # RMS over every channel sample: (1.0^2 + 0.5^2) / 200.
        self.assertAlmostEqual(track.rms, math.sqrt(1.25 / 200), places=6)

    def test_explicit_automation_frames_are_reported(self) -> None:
        _write_wav(self.dir / "a.wav", _tone(4800, 48000), 48000)
        project = parse_manifest(_manifest([{
            "name": "a", "path": "a.wav",
            "automation": [{"t": 0.0, "gain_db": 0.0}, {"t": 0.1, "gain_db": -6.0}],
        }]))
        report = render_project(
            project, [self.dir / "a.wav"], self.dir / "out.wav", 257,
            automation_frames=[0, 2400, 4799],
        )
        rows = [(r["frame"], r["gain_db"]) for r in report.automation_samples]
        self.assertEqual([f for f, _ in rows], [0, 2400, 4799])
        self.assertAlmostEqual(rows[1][1], -3.0, places=6)

    def test_output_may_not_overwrite_a_source(self) -> None:
        source = self.dir / "a.wav"
        _write_wav(source, _tone(1000, 48000), 48000)
        project = parse_manifest(_manifest([{"name": "a", "path": "a.wav"}]))
        with self.assertRaises(ValueError):
            render_project(project, [source], source, 64)
        self.assertEqual(sf.info(str(source)).frames, 1000)


class CliTests(unittest.TestCase):
    def setUp(self) -> None:
        self.tmp = tempfile.TemporaryDirectory()
        self.dir = Path(self.tmp.name)

    def tearDown(self) -> None:
        self.tmp.cleanup()

    def _run(self, *args: str) -> int:
        return main(list(args))

    def test_demo_emits_expected_report(self) -> None:
        import contextlib
        import io

        buffer = io.StringIO()
        with contextlib.redirect_stdout(buffer):
            code = self._run("demo", "--workdir", str(self.dir), "--chunk-frames", "257", "--json")
        self.assertEqual(code, 0)
        payload = json.loads(buffer.getvalue())
        self.assertGreaterEqual(payload["frames_out"], 90000)
        self.assertEqual(len(payload["tracks"]), 2)
        self.assertTrue(all("sample_rate_in" in t for t in payload["tracks"]))
        self.assertGreaterEqual(len(payload["automation_samples"]), 6)
        self.assertGreater(payload["clip_count"], 0)
        self.assertLessEqual(payload["offline_stream_max_abs_diff"], 1e-6)
        self.assertTrue((self.dir / "mix.wav").exists())

    def test_bad_manifest_exits_2_and_writes_nothing(self) -> None:
        manifest = self.dir / "bad.json"
        manifest.write_text(json.dumps({"tracks": []}), encoding="utf-8")
        code = self._run("render", "--manifest", str(manifest),
                         "--output", str(self.dir / "out.wav"))
        self.assertEqual(code, 2)
        self.assertFalse((self.dir / "out.wav").exists())

    def test_bad_audio_exits_3_and_writes_nothing(self) -> None:
        manifest = self.dir / "p.json"
        manifest.write_text(json.dumps(_manifest([{"name": "x", "path": "missing.wav"}])),
                            encoding="utf-8")
        code = self._run("render", "--manifest", str(manifest),
                         "--output", str(self.dir / "out.wav"))
        self.assertEqual(code, 2)  # path validation is a manifest problem
        self.assertFalse((self.dir / "out.wav").exists())

        broken = self.dir / "broken.wav"
        broken.write_bytes(b"not a wav file at all")
        manifest.write_text(json.dumps(_manifest([{"name": "x", "path": "broken.wav"}])),
                            encoding="utf-8")
        code = self._run("render", "--manifest", str(manifest),
                         "--output", str(self.dir / "out.wav"))
        self.assertEqual(code, 3)

    def test_invalid_chunk_size_exits_2(self) -> None:
        code = self._run("demo", "--workdir", str(self.dir), "--chunk-frames", "0")
        self.assertEqual(code, 2)

    def test_automation_frame_flag_is_repeatable(self) -> None:
        import contextlib
        import io

        buffer = io.StringIO()
        with contextlib.redirect_stdout(buffer):
            code = self._run(
                "demo", "--workdir", str(self.dir), "--json",
                "--automation-frame", "0", "--automation-frame", "95999",
            )
        self.assertEqual(code, 0)
        payload = json.loads(buffer.getvalue())
        frames = {row["frame"] for row in payload["automation_samples"]}
        self.assertEqual(frames, {0, 95999})

    def test_out_of_range_automation_frame_exits_2(self) -> None:
        manifest = self.dir / "p.json"
        _write_wav(self.dir / "a.wav", _tone(480, 48000), 48000)
        manifest.write_text(json.dumps(_manifest([{"name": "a", "path": "a.wav"}])),
                            encoding="utf-8")
        code = self._run(
            "render", "--manifest", str(manifest), "--output", str(self.dir / "o.wav"),
            "--automation-frame", "480",
        )
        self.assertEqual(code, 2)
        self.assertFalse((self.dir / "o.wav").exists())

    def test_consistency_failure_exits_4_and_writes_nothing(self) -> None:
        import contextlib
        import io
        from unittest import mock

        import audiomix.cli as cli
        import audiomix.engine as engine

        manifest = self.dir / "p.json"
        _write_wav(self.dir / "a.wav", _tone(480, 48000), 48000)
        manifest.write_text(json.dumps(_manifest([{"name": "a", "path": "a.wav"}])),
                            encoding="utf-8")
        with mock.patch.object(engine, "CONSISTENCY_TOLERANCE", -1.0):
            stderr = io.StringIO()
            with contextlib.redirect_stderr(stderr):
                code = cli.main([
                    "render", "--manifest", str(manifest),
                    "--output", str(self.dir / "out.wav"),
                ])
        self.assertEqual(code, 4)
        self.assertIn("consistency", stderr.getvalue())
        self.assertFalse((self.dir / "out.wav").exists())
        leftovers = [p.name for p in self.dir.iterdir() if p.name.endswith(".tmp")]
        self.assertEqual(leftovers, [])

    def test_clip_count_disagreement_is_fatal_before_write(self) -> None:
        import audiomix.engine as engine
        from audiomix.nodes import BusNode

        _write_wav(self.dir / "a.wav", _tone(480, 48000), 48000)
        project = parse_manifest(_manifest([{"name": "a", "path": "a.wav"}]))
        original = BusNode.finish

        def skewed_finish(self) -> int:
            count = original(self)
            # Make only the second bus to finish disagree.
            self.clip_count = count + 1 if self is not first_bus[0] else count
            return self.clip_count

        first_bus: list[BusNode] = []
        original_init = BusNode.__init__

        def tracking_init(self, frames_total: int) -> None:
            original_init(self, frames_total)
            if not first_bus:
                first_bus.append(self)

        from unittest import mock

        out = self.dir / "out.wav"
        with mock.patch.object(BusNode, "__init__", tracking_init), \
                mock.patch.object(BusNode, "finish", skewed_finish):
            with self.assertRaises(engine.ConsistencyError):
                render_project(project, [self.dir / "a.wav"], out, 64)
        self.assertFalse(out.exists())

    def test_module_entry_point_runs(self) -> None:
        result = subprocess.run(
            [sys.executable, "-m", "audiomix", "demo", "--workdir", str(self.dir), "--json"],
            capture_output=True, text=True, timeout=120,
        )
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("frames_out", result.stdout)


if __name__ == "__main__":
    unittest.main()
