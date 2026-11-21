"""Unit tests for fixtures, codec, metrics and parameter validation."""

from __future__ import annotations

import unittest

import numpy as np

from abrscan.codec import CodecError, decode_clip, encode_clip
from abrscan.fixture import render_fixture
from abrscan.metrics import MetricError, compute_metrics, psnr_db
from abrscan.params import ParamError, parse_scan_spec, parse_targets

WIDTH, HEIGHT, FRAMES, FPS = 32, 32, 6, 24


def _fixture():
    return render_fixture("moving-texture", WIDTH, HEIGHT, FRAMES, FPS)


class FixtureTests(unittest.TestCase):
    def test_content_hash_is_repeatable_and_geometry_sensitive(self):
        first = _fixture()
        second = _fixture()
        self.assertEqual(first.content_hash, second.content_hash)
        other = render_fixture("moving-texture", WIDTH, HEIGHT, FRAMES + 1, FPS)
        self.assertNotEqual(first.content_hash, other.content_hash)

    def test_frames_are_uint8_and_not_uniform(self):
        fixture = _fixture()
        self.assertEqual(fixture.frames.dtype, np.uint8)
        self.assertEqual(fixture.frames.shape, (FRAMES, HEIGHT, WIDTH))
        self.assertGreater(int(fixture.frames.max()), int(fixture.frames.min()))

    def test_unknown_fixture_rejected(self):
        with self.assertRaises(ParamError):
            render_fixture("nope", WIDTH, HEIGHT, FRAMES, FPS)


class CodecTests(unittest.TestCase):
    def test_round_trip_is_finite_and_shape_preserving(self):
        fixture = _fixture()
        clip = encode_clip(fixture.frames, 40, 8, FPS)
        decoded = decode_clip(clip)
        self.assertEqual(decoded.shape, fixture.frames.shape)
        self.assertEqual(clip.encoded_bytes, len(clip.payload))
        quality = psnr_db(fixture.frames, decoded)
        self.assertTrue(np.isfinite(quality))
        self.assertGreater(quality, 10.0)

    def test_payload_is_deterministic(self):
        fixture = _fixture()
        a = encode_clip(fixture.frames, 40, 8, FPS)
        b = encode_clip(fixture.frames, 40, 8, FPS)
        self.assertEqual(a.payload, b.payload)

    def test_corrupted_payload_is_rejected(self):
        fixture = _fixture()
        clip = encode_clip(fixture.frames, 40, 8, FPS)
        corrupt = bytearray(clip.payload)
        corrupt[-8] ^= 0xFF
        with self.assertRaises(CodecError):
            decode_clip(bytes(corrupt))
        with self.assertRaises(CodecError):
            decode_clip(clip.payload[:10])
        with self.assertRaises(CodecError):
            decode_clip(clip.payload + b"trailing")

    def test_invalid_settings_rejected(self):
        fixture = _fixture()
        with self.assertRaises(CodecError):
            encode_clip(fixture.frames, 0, 8, FPS)
        with self.assertRaises(CodecError):
            encode_clip(fixture.frames, 40, 7, FPS)
        with self.assertRaises(CodecError):
            encode_clip(fixture.frames.astype(np.float32), 40, 8, FPS)


class MetricTests(unittest.TestCase):
    def test_non_finite_and_identical_are_rejected(self):
        fixture = _fixture()
        with self.assertRaises(MetricError):
            psnr_db(fixture.frames, fixture.frames)
        with self.assertRaises(MetricError):
            psnr_db(fixture.frames, np.zeros_like(fixture.frames).astype(np.float64))
        with self.assertRaises(MetricError):
            compute_metrics(fixture.frames, fixture.frames, 0, FPS)

    def test_bitrate_uses_duration(self):
        fixture = _fixture()
        clip = encode_clip(fixture.frames, 40, 8, FPS)
        measured = compute_metrics(
            fixture.frames, decode_clip(clip), clip.encoded_bytes, FPS
        )
        expected = clip.encoded_bytes * 8 / (FRAMES / FPS) / 1000
        self.assertAlmostEqual(measured.actual_kbps, expected, places=6)
        self.assertGreater(measured.quality_db, 0)


class ParamTests(unittest.TestCase):
    def test_duplicates_are_reported_once(self):
        unique, duplicates = parse_targets("300,100,300,300")
        self.assertEqual(unique, (300, 100))
        self.assertEqual(duplicates, (300,))

    def test_rejects_bad_ranges(self):
        for kwargs in (
            {"targets_raw": ""},
            {"targets_raw": "80,,160"},
            {"targets_raw": "80", "quant_raw": "0"},
            {"targets_raw": "80", "quant_raw": "8,300"},
            {"targets_raw": "80", "block_raw": "7"},
            {"targets_raw": "80", "quant_raw": "8", "block_raw": ""},
        ):
            with self.assertRaises(ParamError, msg=str(kwargs)):
                parse_scan_spec(**kwargs)

    def test_empty_scan_range_rejected(self):
        with self.assertRaises(ParamError):
            parse_scan_spec("80", quant_raw=",", block_raw="8")

    def test_combo_limit_enforced(self):
        quants = ",".join(str(v) for v in range(1, 13))
        with self.assertRaises(ParamError):
            parse_scan_spec("80", quant_raw=quants, block_raw="4,8,16")
        spec = parse_scan_spec("80", quant_raw="8,8,8,8,8,8,8,8", block_raw="8")
        self.assertEqual(spec.combos, ((8, 8),))


if __name__ == "__main__":
    unittest.main()
