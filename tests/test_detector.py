"""Unit tests for the detection algorithm, feature extraction and WAV I/O."""

import os
import sys
import tempfile
import unittest
import wave
from pathlib import Path

import numpy as np

# Make the src/ layout importable without an editable install, regardless of
# whether unittest was invoked via discover or a dotted module path.
sys.path.insert(0, str(Path(__file__).resolve().parent.parent / "src"))

from audioslice.detector import (  # noqa: E402
    ActivityDetector,
    DetectorConfig,
    Segment,
)
from audioslice.features import extract_features, frame_energy, sliding_quantile  # noqa: E402
from audioslice.wavio import WavFormatError, read_wav  # noqa: E402

SR = 48000


def hum(rng, seconds=3.0, rms=30.0):
    n = int(SR * seconds)
    t = np.arange(n) / SR
    return rng.normal(0.0, rms, n) + 0.5 * rms * np.sin(2 * np.pi * 90.0 * t)


def detect(signal, **overrides):
    cfg = DetectorConfig(**overrides)
    feats = extract_features(signal, SR, cfg.frame_ms, cfg.noise_window_ms,
                             cfg.noise_quantile)
    return ActivityDetector(cfg).detect(feats)


def coverage_ok(result):
    covered = sum(s.length_samples for s in result.activity) + \
        sum(s.length_samples for s in result.silence)
    return covered == result.total_samples


class WavIOTest(unittest.TestCase):
    def setUp(self):
        self.dir = tempfile.mkdtemp()

    def _write(self, name, frames, channels=1, width=2, rate=SR):
        path = os.path.join(self.dir, name)
        with wave.open(path, "wb") as wf:
            wf.setnchannels(channels)
            wf.setsampwidth(width)
            wf.setframerate(rate)
            wf.writeframes(frames)
        return path

    def test_mono_and_stereo_decode(self):
        mono = self._write("m.wav", np.zeros(SR, "<i2").tobytes())
        data = read_wav(mono)
        self.assertEqual(data.samples.shape, (SR,))
        self.assertEqual(data.channels, 1)

        stereo = self._write("s.wav", np.zeros((SR, 2), "<i2").tobytes(), channels=2)
        data = read_wav(stereo)
        self.assertEqual(data.samples.shape, (SR, 2))
        self.assertEqual(data.channels, 2)

    def test_rejects_8bit_three_channel_and_empty(self):
        cases = [
            self._write("b8.wav", b"\x80" * 100, width=1),
            self._write("b3.wav", b"\x00" * 600, channels=3),
            self._write("empty.wav", b""),
        ]
        for path in cases:
            with self.assertRaises(WavFormatError) as ctx:
                read_wav(path)
            self.assertEqual(ctx.exception.path, path)

    def test_rejects_truncated_and_non_wav(self):
        trunc = os.path.join(self.dir, "trunc.wav")
        with open(trunc, "wb") as fh:
            fh.write(b"RIFF" + (36 + 2000).to_bytes(4, "little") + b"WAVEfmt ")
            fh.write((16).to_bytes(4, "little") + (1).to_bytes(2, "little")
                     + (1).to_bytes(2, "little") + SR.to_bytes(4, "little")
                     + (SR * 2).to_bytes(4, "little") + (2).to_bytes(2, "little")
                     + (16).to_bytes(2, "little") + b"data"
                     + (2000).to_bytes(4, "little") + b"\x00" * 12)
        with self.assertRaises(WavFormatError):
            read_wav(trunc)

        not_wav = os.path.join(self.dir, "x.wav")
        with open(not_wav, "wb") as fh:
            fh.write(b"NOPE" * 16)
        with self.assertRaises(WavFormatError):
            read_wav(not_wav)


class FeatureTest(unittest.TestCase):
    def test_blocked_energy_matches_unblocked(self):
        rng = np.random.default_rng(3)
        x = rng.normal(0, 100, SR).astype(np.int16)
        blocked = frame_energy(x, 480, 480, block_frames=37)
        whole = frame_energy(x, 480, 480, block_frames=10_000)
        np.testing.assert_array_equal(blocked, whole)

    def test_stereo_uses_mean_channel_energy(self):
        x = np.zeros((SR, 2), dtype=np.int16)
        x[:, 0] = 1000
        feats = extract_features(x, SR, 10, 500)
        # mean square = (1000^2 + 0) / 2 -> rms ~707.1
        self.assertAlmostEqual(feats.energy[-1], 1000 / np.sqrt(2), places=2)

    def test_sliding_quantile_is_local(self):
        values = np.array([1.0] * 50 + [100.0] * 50)
        q = sliding_quantile(values, window=21, quantile=0.25)
        self.assertLessEqual(q[10], 1.5)       # quiet region stays quiet
        self.assertGreaterEqual(q[-1], 50.0)   # loud region lifts its own floor

    def test_short_file_padded_to_one_frame(self):
        feats = extract_features(np.full(5, 500, np.int16), SR, 10, 500)
        self.assertEqual(feats.n_frames, 1)
        self.assertEqual(feats.frame_end_sample(0), feats.frame_length)


class DetectorConfigTest(unittest.TestCase):
    def test_illegal_parameters_rejected(self):
        bad = [
            dict(frame_ms=0),
            dict(noise_window_ms=-1),
            dict(high_factor=1.0),
            dict(low_factor=4.0, high_factor=2.0),
            dict(merge_gap_ms=-5),
            dict(min_event_ms=5, frame_ms=10),
            dict(noise_quantile=1.5),
            dict(abs_activation_rms=0),
        ]
        for kwargs in bad:
            with self.assertRaises(ValueError):
                ActivityDetector(DetectorConfig(**kwargs))


class DetectorTest(unittest.TestCase):
    def setUp(self):
        self.rng = np.random.default_rng(11)

    def test_all_silence_and_all_active(self):
        silent = detect(np.zeros(3 * SR, np.int16))
        self.assertEqual(silent.activity, ())
        self.assertEqual(len(silent.silence), 1)
        self.assertEqual(silent.silence[0].length_samples, silent.total_samples)

        active = detect(np.full(3 * SR, 2000, np.int16))
        self.assertEqual(len(active.activity), 1)
        self.assertEqual(active.activity[0].start_sample, 0)
        self.assertEqual(active.activity[0].end_sample, active.total_samples)
        self.assertEqual(active.silence, ())
        self.assertTrue(coverage_ok(active))

    def test_short_file_legal_result(self):
        # 60 ms of loud audio: padded to full frames, meets the 40 ms rule.
        loud = detect(np.full(int(0.06 * SR), 4000, np.int16))
        self.assertEqual(len(loud.activity), 1)
        self.assertTrue(coverage_ok(loud))
        # Extremely short silent input still yields a legal, empty result.
        quiet = detect(np.zeros(20, np.int16))
        self.assertEqual(len(quiet.activity), 0)
        self.assertEqual(len(quiet.silence), 1)
        self.assertTrue(coverage_ok(quiet))

    def test_hum_alone_not_detected_but_weak_tonal_event_is(self):
        bed = hum(self.rng)
        self.assertEqual(detect(bed).activity, ())

        t = np.arange(bed.size) / SR
        tone = np.where((t >= 1.0) & (t < 1.1),
                        45 * np.sin(2 * np.pi * 440 * t), 0.0)
        result = detect((bed + tone).astype(np.int16))
        self.assertEqual(len(result.activity), 1)
        seg = result.activity[0]
        self.assertTrue(seg.start_sample <= SR <= seg.end_sample)
        self.assertTrue(coverage_ok(result))

    def test_adjacent_events_merge_and_keep_candidates(self):
        bed = hum(self.rng)
        t = np.arange(bed.size) / SR
        for start, end in ((0.8, 0.9), (0.95, 1.05)):  # 50 ms gap < 70 ms
            m = (t >= start) & (t < end)
            bed[m] += 800 * np.sin(2 * np.pi * 500 * t[m])
        result = detect(bed)
        merged = [s for s in result.activity if s.reason.startswith("merged")]
        self.assertEqual(len(merged), 1)
        self.assertEqual(len(merged[0].candidates), 2)
        self.assertTrue(coverage_ok(result))

    def test_widely_separated_events_stay_apart(self):
        bed = hum(self.rng)
        t = np.arange(bed.size) / SR
        for start, end in ((0.5, 0.6), (1.5, 1.6)):  # 900 ms gap
            m = (t >= start) & (t < end)
            bed[m] += 800 * np.sin(2 * np.pi * 500 * t[m])
        result = detect(bed)
        self.assertEqual(len(result.activity), 2)
        self.assertTrue(all(s.reason == "initial" for s in result.activity))

    def test_fade_boundaries_claim_the_ramp(self):
        bed = hum(self.rng)
        n = bed.size
        t = np.arange(n) / SR
        env = np.zeros(n)
        fi = (t >= 1.0) & (t < 1.2)
        hold = (t >= 1.2) & (t < 1.4)
        fo = (t >= 1.4) & (t < 1.6)
        env[fi] = (t[fi] - 1.0) / 0.2
        env[hold] = 1.0
        env[fo] = (1.6 - t[fo]) / 0.2
        signal = (bed + env * 1500 * np.sin(2 * np.pi * 330 * t)).astype(np.int16)
        result = detect(signal)
        self.assertEqual(len(result.activity), 1)
        seg = result.activity[0]
        self.assertLessEqual(seg.start_sample, SR)
        self.assertGreaterEqual(seg.end_sample, int(1.6 * SR))

    def test_coordinates_are_python_ints_and_half_open(self):
        result = detect(np.full(3 * SR, 2000, np.int16))
        seg = result.activity[0]
        self.assertIsInstance(seg, Segment)
        self.assertIsInstance(seg.start_sample, int)
        self.assertIsInstance(seg.end_sample, int)
        self.assertLess(seg.start_sample, seg.end_sample)


if __name__ == "__main__":
    unittest.main()
