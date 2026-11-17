"""Unit tests for the self-implemented detector and WAV I/O layer."""

from __future__ import annotations

import tempfile
import unittest
import wave
from pathlib import Path

import numpy as np

from audiocut.detector import DetectorConfig, SilenceDetector
from audiocut.fixtures import (
    build_demo_signal,
    create_demo_wav,
    create_noise_wav,
    create_tone_wav,
    write_mono_wav,
)
from audiocut.wavio import WavFormatError, WavReader, WavWriter, copy_segment

SR = 16000


class TempWavCase(unittest.TestCase):
    def setUp(self) -> None:
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.dir = Path(self.tmp.name)

    def path(self, name: str) -> str:
        return str(self.dir / name)


class TestWavIO(TempWavCase):
    def test_all_sample_widths_and_channels_roundtrip(self) -> None:
        rng = np.random.default_rng(0)
        samples = rng.normal(0.0, 0.2, size=SR // 2).astype(np.float32)
        for width in (1, 2, 3, 4):
            for channels in (1, 2):
                target = self.path(f"w{width}c{channels}.wav")
                write_mono_wav(target, samples, SR, sample_width=width, channels=channels)
                with WavReader(target) as reader:
                    self.assertEqual(reader.sample_width, width)
                    self.assertEqual(reader.channels, channels)
                    self.assertEqual(reader.n_frames, samples.size)
                    blocks = list(reader.iter_blocks(0.1))
                reconstructed = np.concatenate([block for _, block in blocks])
                # Quantization tolerates at most half an LSB per format
                # (8-bit LSB is 1/128, wider formats use half of 1/(2^(b-1))).
                half_lsb = 1.0 / (1 << (8 * width - 1))
                tol = half_lsb + (0.5 * half_lsb if width > 1 else 0.5 / 128.0)
                self.assertEqual(reconstructed.size, samples.size)
                self.assertLessEqual(float(np.max(np.abs(reconstructed - samples))), tol + 1e-6)

    def test_block_streaming_never_exceeds_block_size(self) -> None:
        create_tone_wav(self.path("tone.wav"), SR, duration_s=5.0)
        with WavReader(self.path("tone.wav")) as reader:
            for start, block in reader.iter_blocks(2.0):
                self.assertLessEqual(block.size, 2 * SR)
                self.assertEqual(start % SR if start else 0, 0)

    def test_truncated_wav_rejected_with_path(self) -> None:
        good = self.path("good.wav")
        create_tone_wav(good, SR, duration_s=1.0)
        broken = self.path("truncated.wav")
        with open(good, "rb") as src, open(broken, "wb") as dst:
            data = src.read()
            dst.write(data[: len(data) - 97])  # chop PCM payload bytes
        with self.assertRaises(WavFormatError) as ctx:
            with WavReader(broken) as reader:
                list(reader.iter_blocks(0.5))
        self.assertIn("truncated.wav", str(ctx.exception))

    def test_compressed_wav_rejected(self) -> None:
        target = self.path("compressed.wav")
        # Hand-craft a RIFF/WAVE header advertising A-law (format tag 6),
        # since the stdlib wave writer refuses non-PCM compression types.
        import struct

        frames = b"\x00" * 64
        fmt_chunk = struct.pack("<HHIIHH", 1, 6, SR, SR * 2, 2, 8)
        header = b"RIFF" + struct.pack("<I", 36 + len(frames)) + b"WAVE"
        header += b"fmt " + struct.pack("<I", 16) + fmt_chunk
        header += b"data" + struct.pack("<I", len(frames)) + frames
        with open(target, "wb") as handle:
            handle.write(header)
        with self.assertRaises(WavFormatError):
            WavReader(target)

    def test_unsupported_channel_count_rejected(self) -> None:
        target = self.path("six.wav")
        with wave.open(target, "wb") as wf:
            wf.setnchannels(6)
            wf.setsampwidth(2)
            wf.setframerate(SR)
            wf.writeframes(b"\x00" * (6 * 2 * 100))
        with self.assertRaises(WavFormatError):
            WavReader(target)

    def test_copy_segment_preserves_pcm(self) -> None:
        source = self.path("src.wav")
        create_tone_wav(source, SR, duration_s=2.0)
        dest = self.path("dst.wav")
        with WavReader(source) as reader, WavWriter(dest, SR, 1, 2) as writer:
            copy_segment(reader, writer, 1000, 5000)
        with WavReader(dest) as reader:
            self.assertEqual(reader.n_frames, 4000)
            raw_a = reader.read_raw_range(0, 4000)
        with WavReader(source) as reader:
            raw_b = reader.read_raw_range(1000, 5000)
        self.assertEqual(raw_a, raw_b)


class TestDetectorConfig(unittest.TestCase):
    def test_reject_negative_and_zero(self) -> None:
        with self.assertRaises(ValueError):
            DetectorConfig(frame_ms=0).validate()
        with self.assertRaises(ValueError):
            DetectorConfig(min_segment_s=-1).validate()
        with self.assertRaises(ValueError):
            DetectorConfig(noise_quantile=1.0).validate()

    def test_reject_contradictory_thresholds_and_gates(self) -> None:
        with self.assertRaises(ValueError):
            DetectorConfig(voice_factor=1.5, silence_factor=2.0).validate()
        with self.assertRaises(ValueError):
            DetectorConfig(merge_gap_s=0.05, min_silence_s=0.2).validate()

    def test_fingerprint_changes_with_config(self) -> None:
        base = DetectorConfig()
        self.assertEqual(base.fingerprint(), DetectorConfig().fingerprint())
        self.assertNotEqual(base.fingerprint(), DetectorConfig(frame_ms=30).fingerprint())


class TestDetection(TempWavCase):
    def _detect(self, path: str, config: DetectorConfig | None = None):
        with WavReader(path) as reader:
            return SilenceDetector(config).analyze(reader)

    def test_demo_signal_three_segments_within_tolerance(self) -> None:
        target = self.path("demo.wav")
        create_demo_wav(target)
        result = self._detect(target)
        self.assertEqual(len(result.segments), 3)
        expected = ((4000, 20000), (28000, 44000), (52000, 68000))
        for segment, (exp_start, exp_end) in zip(result.segments, expected):
            self.assertLessEqual(abs(segment.start_sample - exp_start), 800)
            self.assertLessEqual(abs(segment.end_sample - exp_end), 800)
        self.assertAlmostEqual(result.silence_total_seconds, 1.5, delta=0.1)
        self.assertTrue(np.isfinite(result.noise_floor))
        self.assertGreater(result.high_threshold, result.low_threshold)

    def test_all_silence_yields_no_segments(self) -> None:
        target = self.path("silence.wav")
        create_noise_wav(target, SR, duration_s=2.0)
        result = self._detect(target)
        self.assertEqual(result.segments, [])
        self.assertEqual(len(result.silences), 1)
        self.assertAlmostEqual(result.silence_total_seconds, 2.0, delta=0.02)

    def test_no_silence_single_segment_spans_file(self) -> None:
        target = self.path("tone.wav")
        create_tone_wav(target, SR, duration_s=2.0)
        result = self._detect(target)
        self.assertEqual(len(result.segments), 1)
        self.assertLessEqual(result.segments[0].start_sample, SR // 10)
        self.assertGreaterEqual(result.segments[0].end_sample, 2 * SR - SR // 10)
        self.assertEqual(result.silences, [])

    def test_empty_input(self) -> None:
        target = self.path("empty.wav")
        with WavWriter(target, SR, 1, 2):
            pass
        result = self._detect(target)
        self.assertEqual(result.segments, [])
        self.assertEqual(result.silences, [])
        self.assertEqual(result.n_frames_total, 0)

    def test_shorter_than_one_frame(self) -> None:
        target = self.path("short.wav")
        write_mono_wav(target, np.full(100, 0.4, dtype=np.float32), SR)
        result = self._detect(target)
        self.assertEqual(result.n_frames_total, 1)
        if result.segments:
            self.assertLessEqual(result.segments[0].end_sample, 100)
            self.assertGreaterEqual(result.segments[0].start_sample, 0)

    def test_trailing_residual_frame(self) -> None:
        target = self.path("residual.wav")
        n = SR  # 320-sample frames: 50 full frames exactly; make it odd
        samples = np.zeros(n + 137, dtype=np.float32)
        samples[: SR] = 0.3 * np.sin(np.linspace(0, 40, SR))
        write_mono_wav(target, samples, SR)
        result = self._detect(target)
        self.assertTrue(result.n_frames_total >= 50)
        for segment in result.segments:
            self.assertGreaterEqual(segment.start_sample, 0)
            self.assertLessEqual(segment.end_sample, n + 137)

    def test_segments_ordered_and_in_bounds(self) -> None:
        target = self.path("demo.wav")
        create_demo_wav(target)
        result = self._detect(target)
        with WavReader(target) as reader:
            total = reader.n_frames
        prev_end = 0
        for segment in result.segments:
            self.assertLess(segment.start_sample, segment.end_sample)
            self.assertGreaterEqual(segment.start_sample, prev_end)
            self.assertLessEqual(segment.end_sample, total)
            prev_end = segment.end_sample
        for region in result.silences:
            self.assertGreaterEqual(region.start_sample, 0)
            self.assertLessEqual(region.end_sample, total)

    def test_min_segment_gate_drops_short_burst(self) -> None:
        target = self.path("blip.wav")
        samples = np.full(SR, 0.0001, dtype=np.float32)  # quiet floor
        samples[100:140] = 0.5  # 2.5 ms blip, far below min_segment_s
        write_mono_wav(target, samples, SR)
        result = self._detect(target, DetectorConfig(min_segment_s=0.1))
        self.assertEqual(result.segments, [])

    def test_merge_gap_joins_close_bursts(self) -> None:
        target = self.path("gap.wav")
        samples = np.full(2 * SR, 0.0001, dtype=np.float32)
        samples[1000:5000] = 0.4
        # 50 ms gap: wider than one analysis frame, so frame-level hysteresis
        # can split it, while still below the default merge_gap_s (200 ms).
        samples[5800:9400] = 0.4
        write_mono_wav(target, samples, SR)
        merged = self._detect(target, DetectorConfig(merge_gap_s=0.2, min_silence_s=0.0))
        self.assertEqual(len(merged.segments), 1)
        split = self._detect(
            target, DetectorConfig(merge_gap_s=0.0, min_silence_s=0.0, min_segment_s=0.0)
        )
        self.assertEqual(len(split.segments), 2)


if __name__ == "__main__":
    unittest.main()
