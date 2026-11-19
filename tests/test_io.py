"""Tests for PCM WAV probing, reading, quantisation and atomic writing."""

from __future__ import annotations

import glob
import os
import unittest

import numpy as np
import soundfile as sf

from audiopipe.io import (
    MAX_CHANNELS,
    WavReader,
    WavWriter,
    PipelineError,
    count_rail_samples,
    probe_wav,
    quantise_pcm,
)
from tests.support import TempDirMixin, sine, write_wav


class ProbeTests(TempDirMixin, unittest.TestCase):
    def test_reads_header(self) -> None:
        path = self.path("a.wav")
        write_wav(path, np.zeros((120, 3)), 44100, "PCM_24")
        info = probe_wav(path)
        self.assertEqual((info.sample_rate, info.channels, info.frames), (44100, 3, 120))
        self.assertEqual(info.subtype, "PCM_24")
        self.assertEqual(info.format, "WAV")

    def test_rejects_non_pcm_encodings(self) -> None:
        for subtype in ("FLOAT", "DOUBLE", "ALAW", "ULAW"):
            path = self.path(f"{subtype}.wav")
            write_wav(path, np.zeros((16, 1)), 22050, subtype)
            with self.assertRaises(PipelineError) as ctx:
                probe_wav(path)
            self.assertEqual(ctx.exception.code, "unsupported_format")

    def test_rejects_bad_rate_and_channels(self) -> None:
        low = self.path("low.wav")
        write_wav(low, np.zeros((16, 1)), 4000)
        with self.assertRaises(PipelineError) as ctx:
            probe_wav(low)
        self.assertEqual(ctx.exception.code, "invalid_sample_rate")

        many = self.path("many.wav")
        write_wav(many, np.zeros((16, MAX_CHANNELS + 1)), 48000)
        with self.assertRaises(PipelineError) as ctx:
            probe_wav(many)
        self.assertEqual(ctx.exception.code, "unsupported_channels")

    def test_rejects_corrupt_and_missing(self) -> None:
        corrupt = self.path("corrupt.wav")
        with open(corrupt, "wb") as fh:
            fh.write(b"RIFF\x10\x00\x00\x00WAVEjunkjunk")
        with self.assertRaises(PipelineError) as ctx:
            probe_wav(corrupt)
        self.assertEqual(ctx.exception.code, "unreadable")

        with self.assertRaises(PipelineError):
            probe_wav(self.path("does-not-exist.wav"))


class ReaderTests(TempDirMixin, unittest.TestCase):
    def test_blocks_are_frames_by_channels(self) -> None:
        path = self.path("a.wav")
        data = np.stack([sine(440, 48000, 1000), sine(880, 48000, 1000)], axis=1)
        write_wav(path, data, 48000, "PCM_24")
        with WavReader(path, block_frames=128) as reader:
            blocks = list(reader.blocks())
        self.assertTrue(all(b.shape[1] == 2 for b in blocks))
        self.assertEqual(sum(b.shape[0] for b in blocks), 1000)
        joined = np.concatenate(blocks)
        np.testing.assert_allclose(joined, data, atol=1e-6)

    def test_empty_file_yields_no_blocks(self) -> None:
        path = self.path("empty.wav")
        write_wav(path, np.zeros((0, 2)), 44100)
        with WavReader(path) as reader:
            self.assertEqual(list(reader.blocks()), [])

    def test_rail_counting(self) -> None:
        self.assertEqual(
            count_rail_samples(np.array([[-1.0], [0.5], [32767 / 32768]]), "PCM_16"), 2
        )
        self.assertEqual(count_rail_samples(np.zeros((10, 1)), "PCM_24"), 0)


class QuantiseTests(unittest.TestCase):
    def test_mapping_and_counts(self) -> None:
        data = np.array([[-0.999, 0.5], [1.5, -1.5], [0.0, 0.25]])
        ints, decoded, out_of_range, rails = quantise_pcm(data, "PCM_16")
        self.assertEqual(out_of_range, 2)  # 1.5 and -1.5
        self.assertEqual(rails, 2)  # pinned to the + and - rails
        self.assertEqual(ints[0, 0], -32735)
        self.assertEqual(ints[0, 1], 16384)
        np.testing.assert_array_equal(decoded, ints.astype(np.float64) / 32768.0)

    def test_overshoot_and_rails_are_distinguishable(self) -> None:
        data = np.array([[-0.999], [0.5], [1.0]])
        _, _decoded, out_of_range, rails = quantise_pcm(data, "PCM_16")
        # Exactly full scale pins the top code without exceeding the range.
        self.assertEqual(out_of_range, 0)
        self.assertEqual(rails, 1)
        # A sample far outside full scale is both out of range and railed.
        _, _, out_of_range, rails = quantise_pcm(np.array([[4.0]]), "PCM_16")
        self.assertEqual((out_of_range, rails), (1, 1))

    def test_mode_24_scaling(self) -> None:
        ints, decoded, _oor, _rails = quantise_pcm(np.array([[0.5]]), "PCM_24")
        self.assertEqual(ints[0, 0], 4194304)
        self.assertEqual(decoded[0, 0], 0.5)

    def test_rejects_non_finite(self) -> None:
        with self.assertRaises(PipelineError):
            quantise_pcm(np.array([[np.nan]]), "PCM_16")

    def test_rejects_unsupported_output(self) -> None:
        for subtype in ("PCM_32", "FLOAT", "PCM_U8"):
            with self.assertRaises(ValueError):
                quantise_pcm(np.zeros((2, 1)), subtype)


class WriterTests(TempDirMixin, unittest.TestCase):
    def test_round_trip_and_determinism(self) -> None:
        data = np.random.default_rng(4).uniform(-0.9, 0.9, (999, 8))
        first = self.path("first.wav")
        second = self.path("second.wav")
        for path in (first, second):
            writer = WavWriter(path, 48000, 8, "PCM_24")
            for start in range(0, len(data), 111):
                writer.write(data[start : start + 111])
            writer.commit()
        with open(first, "rb") as a, open(second, "rb") as b:
            self.assertEqual(a.read(), b.read())
        info = probe_wav(first)
        self.assertEqual((info.channels, info.frames, info.subtype), (8, 999, "PCM_24"))

    def test_abort_leaves_no_output_or_temp_file(self) -> None:
        path = self.path("out.wav")
        writer = WavWriter(path, 48000, 2, "PCM_16")
        writer.write(np.zeros((10, 2)))
        writer.abort()
        self.assertFalse(os.path.exists(path))
        self.assertEqual(glob.glob(self.path(".audiopipe-*")), [])

    def test_exception_during_context_cleans_up(self) -> None:
        path = self.path("out.wav")
        with self.assertRaises(RuntimeError):
            with WavWriter(path, 48000, 1, "PCM_16") as writer:
                writer.write(np.zeros((5, 1)))
                raise RuntimeError("boom")
        self.assertFalse(os.path.exists(path))
        self.assertEqual(glob.glob(self.path(".audiopipe-*")), [])

    def test_commit_moves_into_place(self) -> None:
        path = self.path("nested", "dir", "out.wav")
        writer = WavWriter(path, 44100, 1, "PCM_16")
        writer.write(sine(440, 44100, 200)[:, None])
        writer.commit()
        self.assertTrue(os.path.exists(path))
        self.assertEqual(probe_wav(path).frames, 200)

    def test_counts_write_clipping(self) -> None:
        path = self.path("hot.wav")
        writer = WavWriter(path, 48000, 1, "PCM_16")
        writer.write(np.array([[2.0], [-2.0], [0.5]]))
        writer.commit()
        self.assertEqual(writer.out_of_range_samples, 2)
        self.assertEqual(writer.rail_samples, 2)

    def test_rejects_bad_output_subtype(self) -> None:
        with self.assertRaises(ValueError):
            WavWriter(self.path("x.wav"), 48000, 1, "FLOAT")


if __name__ == "__main__":
    unittest.main()
