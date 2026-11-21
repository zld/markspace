"""Validation behaviour of the streaming PCM WAV reader."""

from __future__ import annotations

import os
import struct
import tempfile
import unittest

import numpy as np

from audioslice.errors import InputFileError, WavFormatError
from audioslice.wav import SUPPORTED_SAMPLE_RATES, WavReader, write_pcm_wav


class WavReaderTest(unittest.TestCase):
    def setUp(self) -> None:
        self.dir = tempfile.mkdtemp()
        self.signal = np.array([0, 1000, -1000, 32767, -32768, 123], dtype=np.float64)
        self.stereo = np.column_stack([self.signal, self.signal // 2])

    def path(self, name: str) -> str:
        return os.path.join(self.dir, name)

    def test_all_integer_bit_depths_roundtrip(self) -> None:
        for width in (1, 2, 3, 4):
            with self.subTest(width=width):
                path = self.path(f"w{width}.wav")
                write_pcm_wav(path, self.stereo, 48000, width)
                with WavReader(path) as reader:
                    self.assertEqual(reader.info.sample_width, width)
                    self.assertEqual(reader.info.channels, 2)
                    self.assertEqual(reader.info.frame_count, 6)
                    first = reader.read_frames(2)
                    rest = reader.read_frames(10)
                    self.assertEqual(first.shape, (2, 2))
                    self.assertEqual(rest.shape, (4, 2))
                    reader.rewind()
                    self.assertEqual(reader.read_frames(100).shape, (6, 2))

    def test_24bit_values_are_exact(self) -> None:
        values = np.array([0, 1000, -5_000_000, 8_388_607, -8_388_608], dtype=np.float64)
        path = self.path("24.wav")
        write_pcm_wav(path, values, 48000, 3)
        with WavReader(path) as reader:
            np.testing.assert_array_equal(reader.read_frames(100)[:, 0], values)

    def test_float_wav_is_rejected(self) -> None:
        header = b"RIFF" + struct.pack("<I", 44) + b"WAVE"
        header += b"fmt " + struct.pack("<IHHIIHH", 16, 3, 1, 48000, 12, 4, 32)
        header += b"data" + struct.pack("<I", 8) + b"\x00" * 8
        path = self.path("float.wav")
        with open(path, "wb") as fh:
            fh.write(header)
        with self.assertRaises(WavFormatError) as ctx:
            WavReader(path)
        self.assertIn("floating point", ctx.exception.reason)
        self.assertEqual(ctx.exception.path, path)

    def test_truncated_frames_are_rejected(self) -> None:
        path = self.path("trunc.wav")
        write_pcm_wav(path, np.zeros(10), 48000, 2)
        os.truncate(path, os.path.getsize(path) - 4)
        with WavReader(path) as reader:
            with self.assertRaises(WavFormatError):
                reader.read_frames(100)

    def test_unsupported_sample_rate_is_rejected(self) -> None:
        path = self.path("rate.wav")
        write_pcm_wav(path, np.zeros(4), 12345, 2)
        with self.assertRaises(WavFormatError):
            WavReader(path)

    def test_supported_sample_rates_accepted(self) -> None:
        for rate in sorted(SUPPORTED_SAMPLE_RATES):
            with self.subTest(rate=rate):
                path = self.path(f"r{rate}.wav")
                write_pcm_wav(path, np.zeros(3), rate, 2)
                with WavReader(path) as reader:
                    self.assertEqual(reader.info.sample_rate, rate)

    def test_missing_and_non_riff_inputs(self) -> None:
        with self.assertRaises(InputFileError):
            WavReader(self.path("missing.wav"))
        path = self.path("notriff.wav")
        with open(path, "wb") as fh:
            fh.write(b"plain text, definitely not audio")
        with self.assertRaises(WavFormatError):
            WavReader(path)

    def test_extra_chunks_are_skipped(self) -> None:
        # LIST chunk before data must not confuse the parser.
        path = self.path("listed.wav")
        write_pcm_wav(path, self.signal, 48000, 2)
        with open(path, "rb") as fh:
            raw = bytearray(fh.read())
        list_chunk = b"LIST" + struct.pack("<I", 4) + b"INFO"
        raw[12:12] = list_chunk
        struct.pack_into("<I", raw, 4, struct.unpack_from("<I", raw, 4)[0] + len(list_chunk))
        with open(path, "wb") as fh:
            fh.write(raw)
        with WavReader(path) as reader:
            self.assertEqual(reader.info.frame_count, 6)
            np.testing.assert_array_equal(reader.read_frames(100)[:, 0], self.signal)


if __name__ == "__main__":
    unittest.main()
