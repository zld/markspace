"""Pipeline tests: resample kernel, layout, loudness, batch and CLI paths."""
from __future__ import annotations

import contextlib
import io
import json
import math
import os
import tempfile
import unittest
from pathlib import Path

import numpy as np
import soundfile as sf

from audioprep import batch, cli, layout, loudness, resample, wavio


def tone(freq, rate, n, amp=0.5):
    t = np.arange(n, dtype=np.float64) / rate
    return amp * np.sin(2 * np.pi * freq * t)


class ResampleKernelTests(unittest.TestCase):
    def test_reduce_ratio_and_validation(self):
        self.assertEqual(resample.reduce_ratio(44100, 48000), (160, 147))
        self.assertEqual(resample.reduce_ratio(48000, 48000), (1, 1))
        self.assertEqual(resample.reduce_ratio(96000, 48000), (1, 2))
        for bad in (0, -1, 1.5):
            with self.assertRaises(ValueError):
                resample.reduce_ratio(bad, 48000)

    def test_output_frame_count_rounding(self):
        self.assertEqual(resample.output_frame_count(44100, 44100, 48000), 48000)
        self.assertEqual(resample.output_frame_count(1000, 44100, 48000), 1089)
        self.assertEqual(resample.output_frame_count(40, 44100, 48000), 44)
        self.assertEqual(resample.output_frame_count(96000, 96000, 48000), 48000)

    def test_identity_same_rate(self):
        x = tone(1000, 48000, 4800)
        np.testing.assert_array_equal(resample.resample(x, 48000, 48000), x)

    def test_passband_unity_and_length(self):
        x = tone(1000, 44100, 44100)
        y = resample.resample(x, 44100, 48000)
        self.assertEqual(len(y), 48000)
        rms = lambda a: float(np.sqrt(np.mean(a * a)))
        self.assertAlmostEqual(rms(y), rms(x), delta=0.01 * rms(x))

    def test_alias_suppression_decimation(self):
        x = tone(30000, 96000, 96000)  # above 24 kHz output Nyquist
        y = resample.resample(x, 96000, 48000)
        self.assertLess(20 * math.log10(np.sqrt(np.mean(y * y)) / 0.5), -50)

    def test_image_suppression_interpolation(self):
        x = tone(20000, 44100, 44100)
        y = resample.resample(x, 44100, 48000)
        spec = np.abs(np.fft.rfft(y))
        freqs = np.fft.rfftfreq(len(y), 1 / 48000)
        image = spec[np.argmin(abs(freqs - 23900))]   # 44100-20000 folds here
        signal = spec[np.argmin(abs(freqs - 20000))]
        self.assertLess(20 * math.log10(image / signal), -50)

    def test_frequency_response_shape(self):
        plan = resample.plan_resample(44100, 48000)
        h, c = resample.impulse_response(plan)
        k = np.arange(h.size) - c
        r_up = 44100 * plan.up

        def mag(f):
            return abs(np.sum(h * np.exp(-2j * np.pi * f * k / r_up))) / plan.up
        self.assertAlmostEqual(mag(1000), 1.0, delta=0.02)
        self.assertAlmostEqual(mag(10000), 1.0, delta=0.02)
        self.assertLess(mag(26000), 1e-3)

    def test_short_signal_and_zero_extension(self):
        y = resample.resample(np.ones(5), 44100, 48000)
        self.assertEqual(len(y), resample.output_frame_count(5, 44100, 48000))
        self.assertTrue(np.all(np.isfinite(y)))
        x = np.ones(44100)  # DC must stay DC away from the zero-padded edges
        y = resample.resample(x, 44100, 48000)
        np.testing.assert_allclose(y[1000:-1000], 1.0, atol=1e-3)


class LayoutTests(unittest.TestCase):
    def test_ensure_2d_and_mapping(self):
        self.assertEqual(layout.ensure_2d(np.zeros(7)).shape, (7, 1))
        self.assertEqual(layout.channel_mapping(4), (0, 1, 2, 3))
        with self.assertRaises(ValueError):
            layout.channel_mapping(0)

    def test_pad_and_crop(self):
        frames = np.ones((10, 2))
        padded, n_pad = layout.pad_to_block(frames, 147)
        self.assertEqual((padded.shape[0], n_pad), (147, 137))
        self.assertEqual(layout.pad_to_block(frames, 5)[1], 0)
        cropped = layout.crop_frames(padded, 10)
        np.testing.assert_array_equal(cropped, frames)
        with self.assertRaises(ValueError):
            layout.crop_frames(frames, 11)

    def test_interleave_roundtrip_preserves_order(self):
        frames = np.arange(12, dtype=float).reshape(4, 3)
        np.testing.assert_array_equal(
            layout.interleave(layout.deinterleave(frames)), frames)


class LoudnessTests(unittest.TestCase):
    def test_rms_lufs_truepeak_clipping(self):
        stereo = np.stack([tone(997, 48000, 48000)] * 2, axis=1)
        self.assertAlmostEqual(loudness.rms_dbfs(stereo), -9.03, places=2)
        self.assertAlmostEqual(loudness.lufs(stereo, 48000), -6.06, delta=0.2)
        self.assertAlmostEqual(loudness.true_peak_dbtp(stereo, 48000), -6.02,
                               delta=0.1)
        self.assertEqual(loudness.clipped_samples(stereo), 0)
        hot = np.array([[1.0], [-1.2], [0.5]])
        self.assertEqual(loudness.clipped_samples(hot), 2)

    def test_silence_not_normalizable(self):
        m = loudness.measure(np.zeros((64, 2)), 48000)
        self.assertIsNone(m["lufs"])
        self.assertEqual(m["measured_frames"], 64)
        gain, ok = loudness.normalization_gain(m["lufs"], m["true_peak_dbtp"],
                                               -18, 12)
        self.assertEqual((gain, ok), (0.0, False))

    def test_gain_clamp_and_peak_backoff(self):
        self.assertEqual(loudness.normalization_gain(-50, -40, -18, 12),
                         (12.0, True))
        gain, _ = loudness.normalization_gain(-30, -0.5, -18, 12)
        self.assertAlmostEqual(gain, 0.2)  # -0.3 dBTP ceiling
        self.assertLessEqual(-0.5 + gain, loudness.PEAK_CEILING_DB)


class BatchTestBase(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.root = Path(self.tmp.name)
        self.inp, self.out = self.root / "in", self.root / "out"
        self.inp.mkdir()
        self.report = self.root / "report.json"

    def tearDown(self):
        self.tmp.cleanup()

    def write_wav(self, rel, data, rate, subtype="PCM_24"):
        path = self.inp / rel
        path.parent.mkdir(parents=True, exist_ok=True)
        sf.write(path, data, rate, subtype=subtype)
        return path

    def run_process(self, **over):
        argv = ["process", "--input", str(self.inp), "--output", str(self.out),
                "--report", str(self.report), "--rate", "48000",
                "--target-lufs", "-18", "--max-boost-db", "12",
                "--workers", "2"]
        for k, v in over.items():
            argv += [f"--{k.replace('_', '-')}", str(v)]
        buf = io.StringIO()
        with contextlib.redirect_stdout(buf):
            code = cli.main(argv)
        lines = [json.loads(x) for x in buf.getvalue().splitlines()]
        return code, lines

    def run_verify(self, **over):
        argv = ["verify", "--report", str(self.report), "--output",
                str(self.out), "--tolerance", str(over.get("tolerance", 0.1))]
        buf = io.StringIO()
        with contextlib.redirect_stdout(buf):
            code = cli.main(argv)
        return code, [json.loads(x) for x in buf.getvalue().splitlines()]


class BatchTests(BatchTestBase):
    def test_end_to_end_and_report(self):
        self.write_wav("a.wav", tone(997, 44100, 44100, 0.1), 44100)
        self.write_wav("sub/b.wav", np.stack(
            [tone(880, 32000, 16000, 0.1)] * 2, axis=1), 32000)
        code, lines = self.run_process()
        self.assertEqual(code, 0)
        entry = {l["relpath"]: l for l in lines[:-1]}["a.wav"]
        self.assertEqual((entry["output_rate"], entry["channels"],
                          entry["output_frames"]), (48000, 1, 48000))
        self.assertAlmostEqual(entry["output_lufs"], -18.0, delta=0.1)
        self.assertEqual(entry["output_clipped_samples"], 0)
        data, rate = sf.read(self.out / "sub" / "b.wav", always_2d=True)
        self.assertEqual((rate, data.shape), (48000, (24000, 2)))
        rep = json.loads(self.report.read_text())
        paths = [f["relpath"] for f in rep["files"]]
        self.assertEqual(paths, sorted(paths))
        self.assertEqual(rep["summary"]["failed"], 0)
        self.assertFalse(list(self.out.rglob("*.tmp-*")))  # atomic, no debris
        code, summary = self.run_verify()
        self.assertEqual(code, 0)
        self.assertEqual(summary[-1],
                         {"checked": 2, "mismatch": 0, "failed": 0})

    def test_corrupt_file_fails_but_batch_continues(self):
        self.write_wav("good.wav", tone(440, 48000, 4800, 0.1), 48000)
        (self.inp / "bad.wav").write_bytes(b"RIFF\x00\x00\x00\x00WAVEjunk")
        code, lines = self.run_process()
        self.assertEqual(code, 1)
        bad = {l["relpath"]: l for l in lines[:-1]}["bad.wav"]
        self.assertEqual((bad["status"], bad["stage"]), ("failed", "read"))
        self.assertTrue(bad["error_type"])
        self.assertTrue((self.out / "good.wav").exists())
        rep = json.loads(self.report.read_text())
        self.assertEqual(rep["summary"]["failed"], 1)

    def test_nonfinite_and_channel_limit_rejected(self):
        self.write_wav("nan.wav", np.full((100, 1), np.nan), 48000,
                       subtype="FLOAT")
        self.write_wav("wide.wav", np.zeros((100, 16)), 48000)
        code, lines = self.run_process()
        self.assertEqual(code, 1)
        err = {l["relpath"]: l["error_type"] for l in lines[:-1]}
        self.assertEqual(err["nan.wav"], "WavValidationError")
        self.assertEqual(err["wide.wav"], "WavValidationError")

    def test_write_failure_leaves_no_fake_wav(self):
        self.write_wav("x.wav", tone(440, 48000, 4800, 0.1), 48000)
        (self.out / "x.wav").mkdir(parents=True)  # os.replace onto a dir fails
        code, lines = self.run_process()
        self.assertEqual(code, 1)
        entry = lines[0]
        self.assertEqual((entry["status"], entry["stage"]), ("failed", "write"))
        self.assertTrue((self.out / "x.wav").is_dir())
        self.assertFalse(list(self.out.rglob("*.tmp-*")))

    def test_config_errors_exit_2(self):
        self.write_wav("a.wav", tone(440, 48000, 4800, 0.1), 48000)
        code, _ = self.run_process(output=str(self.inp / "nested"))
        self.assertEqual(code, 2)  # overlapping directories
        code, _ = self.run_process(input=str(self.root / "missing"))
        self.assertEqual(code, 2)  # missing input directory

    def test_duplicate_output_target_rejected(self):
        # a.wav and a.WAV both map to a.wav; on case-insensitive filesystems
        # they cannot coexist, so exercise the mapping check directly.
        cfg = batch.BatchConfig(input_dir=self.inp, output_dir=self.out,
                                rate=48000, target_lufs=-18, max_boost_db=12)
        files = [self.inp / "a.wav", self.inp / "a.WAV"]
        with self.assertRaises(batch.BatchError):
            batch._targets(files, cfg)

    def test_symlink_loop_reported_and_skipped(self):
        self.write_wav("a.wav", tone(440, 48000, 4800, 0.1), 48000)
        os.symlink(".", self.inp / "loop")
        code, _ = self.run_process()
        self.assertEqual(code, 0)
        rep = json.loads(self.report.read_text())
        self.assertEqual(rep["warnings"][0]["reason"], "symlink_loop")
        self.assertTrue(rep["warnings"][0]["path"].endswith("loop"))

    def test_verify_detects_tampered_report(self):
        self.write_wav("a.wav", tone(997, 44100, 44100, 0.1), 44100)
        self.assertEqual(self.run_process()[0], 0)
        rep = json.loads(self.report.read_text())
        rep["files"][0]["output_frames"] += 1
        rep["files"][0]["output_lufs"] += 5.0
        self.report.write_text(json.dumps(rep))
        code, lines = self.run_verify()
        self.assertEqual(code, 3)
        fields = {d["field"] for d in lines[0]["diffs"]}
        self.assertEqual(fields, {"output_frames", "output_lufs"})
        self.assertEqual(lines[-1]["mismatch"], 1)
        code, _ = self.run_verify_missing()

    def run_verify_missing(self):
        buf = io.StringIO()
        with contextlib.redirect_stdout(buf):
            code = cli.main(["verify", "--report", str(self.root / "no.json"),
                             "--output", str(self.out)])
        self.assertEqual(code, 2)
        return code, []


if __name__ == "__main__":
    unittest.main()
