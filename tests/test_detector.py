"""Unit tests for PCM decoding and the silence detector."""
import os
import struct
import tempfile
import unittest
import wave

import numpy as np

from audiocut import DetectorParams, ParamsError, Segment
from audiocut.detector import (
    detect_array,
    detect_blocks,
    effective_threshold,
    silence_window_runs,
)
from audiocut.wav_reader import WavError, decode_block, iter_chunks

SR = 48000
PARAMS = DetectorParams()


def sine(freq, n, amp=0.5):
    t = np.arange(n, dtype=np.float64) / SR
    return amp * np.sin(2.0 * np.pi * freq * t)


def write_wav(path, samples_int, sampwidth, rate=SR, channels=1):
    frames = np.asarray(samples_int).reshape(-1, channels)
    if sampwidth == 1:
        raw = (frames + 128).astype(np.uint8).tobytes()
    elif sampwidth == 2:
        raw = frames.astype("<i2").tobytes()
    elif sampwidth == 3:
        padded = frames.astype("<i4").tobytes()
        raw = np.frombuffer(padded, dtype=np.uint8).reshape(-1, 4)[:, :3].tobytes()
    else:
        raw = frames.astype("<i4").tobytes()
    with wave.open(str(path), "wb") as wf:
        wf.setnchannels(channels)
        wf.setsampwidth(sampwidth)
        wf.setframerate(rate)
        wf.writeframes(raw)


class DecodeTests(unittest.TestCase):
    def setUp(self):
        self.dir = tempfile.TemporaryDirectory()
        self.path = os.path.join(self.dir.name, "t.wav")

    def tearDown(self):
        self.dir.cleanup()

    def read_all(self):
        blocks = [b for _, b in iter_chunks(self.path)]
        return np.concatenate(blocks) if blocks else np.empty((0, 1))

    def test_decode_16bit(self):
        write_wav(self.path, [-32768, 0, 32767], 2)
        out = self.read_all().reshape(-1)
        self.assertAlmostEqual(out[0], -1.0)
        self.assertAlmostEqual(out[1], 0.0)
        self.assertAlmostEqual(out[2], 32767 / 32768)

    def test_decode_8bit_unsigned(self):
        write_wav(self.path, [-128, 0, 127], 1)
        out = self.read_all().reshape(-1)
        self.assertAlmostEqual(out[0], -1.0)
        self.assertAlmostEqual(out[1], 0.0)
        self.assertAlmostEqual(out[2], 127 / 128)

    def test_decode_24bit_signed(self):
        write_wav(self.path, [-8388608, 0, 8388607], 3)
        out = self.read_all().reshape(-1)
        self.assertAlmostEqual(out[0], -1.0)
        self.assertAlmostEqual(out[1], 0.0)
        self.assertAlmostEqual(out[2], 8388607 / 8388608)

    def test_decode_32bit(self):
        write_wav(self.path, [-(2**31), 0, 2**31 - 1], 4)
        out = self.read_all().reshape(-1)
        self.assertAlmostEqual(out[0], -1.0)
        self.assertAlmostEqual(out[2], (2**31 - 1) / 2**31)

    def test_decode_stereo_interleave(self):
        write_wav(self.path, [[16384, -16384]] * 10, 2, channels=2)
        out = self.read_all()
        self.assertEqual(out.shape, (10, 2))
        self.assertAlmostEqual(out[0, 0], 0.5)
        self.assertAlmostEqual(out[0, 1], -0.5)

    def test_chunks_bounded_to_one_second(self):
        write_wav(self.path, np.zeros(int(2.5 * SR), dtype=np.int64), 2)
        sizes = [b.shape[0] for _, b in iter_chunks(self.path)]
        self.assertEqual(sizes, [SR, SR, SR // 2])

    def test_empty_file_fails(self):
        open(self.path, "wb").close()
        with self.assertRaises(WavError):
            list(iter_chunks(self.path))

    def test_garbage_header_fails(self):
        with open(self.path, "wb") as fh:
            fh.write(os.urandom(200))
        with self.assertRaises(WavError):
            list(iter_chunks(self.path))

    def test_truncated_file_fails(self):
        write_wav(self.path, np.zeros(SR, dtype=np.int64), 2)
        with open(self.path, "rb") as fh:
            data = fh.read()
        with open(self.path, "wb") as fh:
            fh.write(data[: len(data) // 2])
        with self.assertRaises(WavError):
            list(iter_chunks(self.path))

    def test_non_pcm_float_fails(self):
        fmt = struct.pack("<HHIIHH", 3, 1, SR, SR * 4, 4, 32)  # IEEE float
        payload = b"\x00" * 400
        riff = (b"WAVE" + b"fmt " + struct.pack("<I", 16) + fmt
                + b"data" + struct.pack("<I", len(payload)) + payload)
        with open(self.path, "wb") as fh:
            fh.write(b"RIFF" + struct.pack("<I", len(riff)) + riff)
        with self.assertRaises(WavError):
            list(iter_chunks(self.path))

    def test_unsupported_channels_fail(self):
        write_wav(self.path, np.zeros((100, 9), dtype=np.int64), 2, channels=9)
        with self.assertRaises(WavError):
            list(iter_chunks(self.path))

    def test_unsupported_sample_rate_fails(self):
        write_wav(self.path, np.zeros(100, dtype=np.int64), 2, rate=4000)
        with self.assertRaises(WavError):
            list(iter_chunks(self.path))


class DetectorSignalTests(unittest.TestCase):
    def test_two_tones_two_segments(self):
        sig = np.zeros(240000)
        sig[24000:91200] = sine(440, 67200)
        sig[104640:167040] = sine(660, 62400, amp=0.4)
        result = detect_array(sig, SR, PARAMS)
        self.assertEqual(result.clip_count, 2)
        for seg, want in zip(result.segments, [(24000, 91200), (104640, 167040)]):
            self.assertAlmostEqual(seg.start_sample, want[0], delta=960)
            self.assertAlmostEqual(seg.end_sample, want[1], delta=960)

    def test_all_silence_zero_segments(self):
        result = detect_array(np.zeros(SR), SR, PARAMS)
        self.assertEqual(result.clip_count, 0)
        self.assertEqual(result.silence_frames, SR)
        self.assertAlmostEqual(result.silence_total_seconds, 1.0)

    def test_continuous_tone_single_segment(self):
        result = detect_array(sine(440, 96000), SR, PARAMS)
        self.assertEqual([(s.start_sample, s.end_sample) for s in result.segments],
                         [(0, 96000)])

    def test_antiphase_stereo_not_cancelled(self):
        left = sine(440, 48000)
        stereo = np.column_stack([left, -left])
        result = detect_array(stereo, SR, PARAMS)
        self.assertEqual(result.clip_count, 1)
        self.assertEqual(result.segments[0].end_sample, 48000)

    def test_short_gap_merged(self):
        sig = np.concatenate([sine(440, 48000), np.zeros(4800), sine(440, 48000)])
        result = detect_array(sig, SR, PARAMS)  # 100 ms gap < 180 ms minimum
        self.assertEqual(result.clip_count, 1)
        self.assertEqual(result.segments[0].start_sample, 0)
        self.assertEqual(result.segments[0].end_sample, len(sig))

    def test_long_gap_splits(self):
        sig = np.concatenate([sine(440, 48000), np.zeros(14400), sine(440, 48000)])
        result = detect_array(sig, SR, PARAMS)  # 300 ms gap >= 180 ms
        self.assertEqual(result.clip_count, 2)

    def test_short_fade_no_fragment(self):
        sig = sine(330, 96000)
        ramp = np.linspace(0.0, 1.0, 480, endpoint=False)
        sig[:480] *= ramp
        sig[-480:] *= ramp[::-1]
        result = detect_array(sig, SR, PARAMS)
        self.assertEqual([(s.start_sample, s.end_sample) for s in result.segments],
                         [(0, 96000)])

    def test_intervals_cover_file(self):
        rng = np.random.default_rng(7)
        sig = np.zeros(200000)
        pos = 0
        while pos < 180000:
            length = int(rng.integers(2000, 30000))
            sig[pos:pos + length] = sine(500, min(length, 200000 - pos))
            pos += length + int(rng.integers(1000, 20000))
        result = detect_array(sig, SR, PARAMS)
        intervals = sorted(result.segments + result.silence,
                           key=lambda s: s.start_sample)
        self.assertEqual(intervals[0].start_sample, 0)
        self.assertEqual(intervals[-1].end_sample, 200000)
        for a, b in zip(intervals, intervals[1:]):
            self.assertEqual(a.end_sample, b.start_sample)

    def test_streaming_matches_one_shot(self):
        sig = np.zeros(120000)
        sig[10000:50000] = sine(440, 40000)
        sig[70000:110000] = sine(550, 40000)
        one_shot = detect_array(sig, SR, PARAMS)
        chunks = [sig[i:i + 17000].reshape(-1, 1) for i in range(0, len(sig), 17000)]
        streamed = detect_blocks(chunks, SR, 1, len(sig), PARAMS)
        self.assertEqual(
            [(s.start_sample, s.end_sample) for s in one_shot.segments],
            [(s.start_sample, s.end_sample) for s in streamed.segments],
        )


class DetectorThresholdTests(unittest.TestCase):
    def test_threshold_never_raised(self):
        levels = np.full(50, -20.0)
        self.assertEqual(effective_threshold(levels, PARAMS), -48.0)

    def test_threshold_lowered_to_noise_plus_margin(self):
        levels = np.full(50, -80.0)
        self.assertAlmostEqual(effective_threshold(levels, PARAMS), -72.0)

    def test_hysteresis_band_holds_state(self):
        # -45 dB sits between threshold (-48) and threshold + 6 dB.
        runs = silence_window_runs(np.array([-20.0, -45.0, -20.0]), -48.0)
        self.assertEqual(runs, [])
        runs = silence_window_runs(np.array([-20.0, -55.0, -45.0, -20.0]), -48.0)
        self.assertEqual(runs, [(1, 3)])  # -45 does not end the silent run

    def test_initial_state_from_first_window(self):
        self.assertEqual(silence_window_runs(np.array([-60.0, -20.0]), -48.0),
                         [(0, 1)])
        self.assertEqual(silence_window_runs(np.array([-20.0, -60.0]), -48.0),
                         [(1, 2)])


class ParamsValidationTests(unittest.TestCase):
    def test_valid_defaults(self):
        self.assertIs(PARAMS.validate(), PARAMS)

    def test_rejects_non_positive(self):
        for kwargs in ({"frame_ms": 0}, {"hop_ms": -1}, {"min_silence_ms": 0}):
            with self.assertRaises(ParamsError):
                DetectorParams(**kwargs).validate()

    def test_rejects_frame_smaller_than_hop(self):
        with self.assertRaises(ParamsError):
            DetectorParams(frame_ms=10, hop_ms=20).validate()

    def test_rejects_out_of_range_threshold(self):
        for bad in (1.0, -130.0):
            with self.assertRaises(ParamsError):
                DetectorParams(silence_dbfs=bad).validate()

    def test_rejects_negative_noise_margin(self):
        with self.assertRaises(ParamsError):
            DetectorParams(noise_margin_db=-0.5).validate()

    def test_digest_stable_and_sensitive(self):
        self.assertEqual(PARAMS.digest(), DetectorParams().digest())
        self.assertNotEqual(PARAMS.digest(),
                            DetectorParams(frame_ms=30).digest())


if __name__ == "__main__":
    unittest.main()
