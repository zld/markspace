"""Numerical and streaming tests for the polyphase resampling kernel."""

import unittest

import numpy as np

from audiopipe.resample import (
    KernelCache,
    Resampler,
    expected_output_frames,
    resample_whole,
)

RATE_PAIRS = [
    (44100, 48000), (48000, 44100), (48000, 16000), (16000, 48000),
    (32000, 48000), (22050, 48000), (11025, 44100), (8000, 48000),
]
CHUNK_SIZES = (1, 7, 256, 4096)


class OutputFrameContractTests(unittest.TestCase):
    def test_known_lengths(self):
        self.assertEqual(
            expected_output_frames(10007, 44100, 48000),
            int(np.floor(10007 * 48000 / 44100 + 0.5)))
        self.assertEqual(expected_output_frames(48000, 48000, 24000), 24000)
        self.assertEqual(expected_output_frames(24000, 24000, 48000), 48000)

    def test_resampler_honors_contract(self):
        rng = np.random.default_rng(0)
        x = rng.standard_normal((5000, 2))
        for sr_in, sr_out in RATE_PAIRS:
            out = resample_whole(x, sr_in, sr_out, 2)
            self.assertEqual(
                out.shape[0],
                expected_output_frames(x.shape[0], sr_in, sr_out))


class ChunkEquivalenceTests(unittest.TestCase):
    def test_chunked_matches_whole(self):
        rng = np.random.default_rng(1)
        x = rng.standard_normal((10007, 2))
        for sr_in, sr_out in RATE_PAIRS:
            cache = KernelCache()
            whole = resample_whole(x, sr_in, sr_out, 2, cache=cache)
            for chunk in CHUNK_SIZES:
                rs = Resampler(sr_in, sr_out, 2, cache=cache)
                parts = [rs.process(x[i:i + chunk])
                         for i in range(0, len(x), chunk)]
                tail, _ = rs.flush()
                parts.append(tail)
                streamed = np.concatenate(parts)
                self.assertEqual(streamed.shape, whole.shape)
                np.testing.assert_allclose(
                    streamed, whole, atol=1e-12, rtol=0)

    def test_single_channel_and_mono_shape(self):
        x = np.random.default_rng(2).standard_normal((3001,))
        out = resample_whole(x, 32000, 48000, 1)
        self.assertEqual(out.shape[1], 1)
        rs = Resampler(32000, 48000, 1)
        streamed = np.concatenate(
            [rs.process(x[:1500]), rs.process(x[1500:]), rs.flush()[0]])
        np.testing.assert_allclose(streamed, out, atol=1e-12)

    def test_identity_passthrough_is_exact(self):
        x = np.random.default_rng(3).standard_normal((1024, 2))
        out = resample_whole(x, 48000, 48000, 2)
        np.testing.assert_array_equal(out, x)


class FilterBehaviorTests(unittest.TestCase):
    def test_preserves_passband_amplitude(self):
        sr = 48000
        t = np.arange(sr) / sr
        tone = (0.8 * np.sin(2 * np.pi * 1000 * t))[:, None]
        down = resample_whole(tone, 48000, 24000, 1)
        up = resample_whole(tone, 24000, 48000, 1)
        self.assertTrue(0.79 < np.max(np.abs(down[200:-200])) < 0.81)
        self.assertTrue(0.79 < np.max(np.abs(up[400:-400])) < 0.81)

    def test_rejects_upsampling_images(self):
        sr = 24000
        t = np.arange(sr) / sr
        tone = np.sin(2 * np.pi * 3000 * t)[:, None]
        up = resample_whole(tone, 24000, 48000, 1)
        n = 32768
        spec = np.abs(np.fft.rfft(up[4096:4096 + n, 0] * np.hanning(n)))
        freqs = np.fft.rfftfreq(n, 1 / 48000)
        amp = lambda f: spec[np.argmin(np.abs(freqs - f))]
        self.assertLess(amp(21000) / amp(3000), 0.01)

    def test_rejects_downsampling_aliases(self):
        sr = 48000
        t = np.arange(sr) / sr
        tone = np.sin(2 * np.pi * 10000 * t)[:, None]
        down = resample_whole(tone, 48000, 16000, 1)
        residual = np.sqrt(np.mean(down[2000:6000] ** 2))
        self.assertLess(residual, 0.001)

    def test_tap_count_configurable(self):
        x = np.random.default_rng(4).standard_normal((500, 1))
        for taps in (8, 16, 24, 64):
            rs = Resampler(44100, 48000, 1, taps=taps)
            out = np.concatenate([rs.process(x), rs.flush()[0]])
            self.assertEqual(
                out.shape[0],
                expected_output_frames(500, 44100, 48000))


class CacheAndFlushingTests(unittest.TestCase):
    def test_cache_is_bounded(self):
        cache = KernelCache(max_entries=3)
        for k in range(6):
            cache.get(48000, 8000 + k, 32)
        self.assertEqual(len(cache), 3)

    def test_cache_reuses_coefficients(self):
        cache = KernelCache()
        phases_a, up_a, down_a = cache.get(44100, 48000, 32)
        phases_b, up_b, down_b = cache.get(44100, 48000, 32)
        self.assertIs(phases_a, phases_b)
        self.assertEqual((up_a, down_a), (up_b, down_b))

    def test_flush_reports_zero_padding(self):
        rs = Resampler(48000, 8000, 1)
        rs.process(np.ones((50, 1)))
        _, pad = rs.flush()
        self.assertGreater(pad, 0)

    def test_invalid_construction(self):
        with self.assertRaises(ValueError):
            Resampler(48000, 24000, 0)
        with self.assertRaises(ValueError):
            Resampler(48000, 24000, 1, taps=0)
        with self.assertRaises(ValueError):
            KernelCache(max_entries=0)

    def test_channel_mismatch_detected(self):
        rs = Resampler(48000, 24000, 2)
        with self.assertRaises(ValueError):
            rs.process(np.zeros((10, 3)))


if __name__ == "__main__":
    unittest.main()
