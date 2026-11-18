"""Low-level unit tests: EBML codec, element tree, Matroska blocks, timestamps.

Runnable with the standard library (no third-party test dependency)::

    .venv/bin/python -m unittest discover -s tests -v

All inputs are built in-process from :mod:`mkvmp4.fixture`; the suite never
touches samples outside the repository.
"""
from __future__ import annotations

import unittest

import numpy as np

from mkvmp4 import ebml, elements, fixture, matroska, timestamps


def vint(value: int) -> bytes:
    """Encode an EBML data-size vint (marker bit set, unknown-free)."""
    width = 1
    while value >= (1 << (7 * width)) - 1:
        width += 1
    return (value | (1 << (7 * width))).to_bytes(width, "big")


class EbmlTests(unittest.TestCase):
    def test_read_id_widths_and_marker_retained(self):
        eid, w = ebml.read_id(b"\xa3", 0)
        self.assertEqual((eid, w), (0xA3, 1))
        eid, w = ebml.read_id(b"\x42\x86", 0)
        self.assertEqual((eid, w), (0x4286, 2))
        eid, w = ebml.read_id(b"\x1a\x45\xdf\xa3", 0)
        self.assertEqual((eid, w), (0x1A45DFA3, 4))

    def test_read_size_strips_marker_and_detects_unknown(self):
        value, w = ebml.read_size(b"\x85", 0)
        self.assertEqual((value, w), (5, 1))
        value, _ = ebml.read_size(b"\xff", 0, allow_unknown=True)
        self.assertEqual(value, ebml.UNKNOWN_SIZE)
        with self.assertRaises(ebml.EbmlError):
            ebml.read_size(b"\xff", 0, allow_unknown=False)

    def test_truncation_and_illegal_width_report_offset(self):
        with self.assertRaises(ebml.EbmlError) as ctx:
            ebml.read_id(b"\x0f", 7)  # zero first byte -> no marker -> illegal
        self.assertEqual(ctx.exception.offset, 7)
        with self.assertRaises(ebml.EbmlError) as ctx:
            ebml.read_header(b"\x1a\x45", 0)  # header truncated mid-ID
        self.assertEqual(ctx.exception.offset, 0)
        # A zero first byte has no width marker: width runs past the limit.
        with self.assertRaises(ebml.EbmlError):
            ebml.read_size(b"\x00\x00", 0)

    def test_element_out_of_bounds_rejected(self):
        data = b"\xa3" + vint(10) + b"short"
        with self.assertRaises(ebml.EbmlError):
            elements.parse_file(data)


class ElementTreeTests(unittest.TestCase):
    def setUp(self):
        self.data = fixture.build_fixture()
        self.tops = elements.parse_file(self.data)

    def test_unknown_size_segment_and_cluster_boundaries(self):
        seg = next(e for e in self.tops if e.id == elements.ID_SEGMENT)
        self.assertTrue(seg.unknown_size)
        self.assertEqual(seg.end, len(self.data))  # resolved to EOF
        clusters = [c for c in seg.children if c.id == elements.ID_CLUSTER]
        self.assertGreaterEqual(len(clusters), 2)
        unknown = [c for c in clusters if c.unknown_size]
        self.assertTrue(unknown, "fixture must include an unknown-size cluster")
        mid = next(c for c in clusters if c.unknown_size)
        self.assertLess(mid.end, len(self.data))  # stops at next sibling

    def test_unknown_size_forbidden_on_generic_element(self):
        bad = bytes([elements.ID_SIMPLE_BLOCK]) + b"\xff" + b"\x00" * 8
        with self.assertRaises(ebml.EbmlError):
            elements.parse_file(bad)

    def test_depth_limit_enforced(self):
        payload = b"\x01"
        for _ in range(elements.MAX_DEPTH + 2):
            payload = b"\xae" + vint(len(payload)) + payload
        data = (fixture._ebml_header() + b"\x18\x53\x80\x67"
                + vint(len(payload)) + payload)
        with self.assertRaises(ebml.EbmlError):
            elements.parse_file(data)

    def test_summary_counts_elements_and_blocks(self):
        summary = elements.summarize(self.tops)
        self.assertIn(summary["element_count"], range(20, 81))
        self.assertGreater(summary["block_elements"], 0)


class MatroskaBlockTests(unittest.TestCase):
    def test_simple_block_absolute_timestamp_and_keyframe(self):
        body = vint(2) + (1500).to_bytes(2, "big", signed=True) + bytes([0x80]) + b"data"
        blk = matroska.parse_block(body, cluster_ts=40000, offset=123)
        self.assertEqual(blk.track_number, 2)
        self.assertEqual(blk.timestamp, 41500)
        self.assertTrue(blk.keyframe)
        self.assertEqual(blk.frames, [b"data"])
        self.assertEqual(blk.offset, 123)

    def test_negative_relative_timestamp(self):
        body = vint(1) + (-2).to_bytes(2, "big", signed=True) + bytes([0]) + b"x"
        self.assertEqual(matroska.parse_block(body, 0, 0).timestamp, -2)

    def test_xiph_lacing_splits_three_frames(self):
        lace = bytes([2, 2, 1]) + b"abcd"  # count=2 -> 3 frames; sizes 2, 1
        body = vint(1) + b"\x00\x00" + bytes([1 << 1]) + lace
        self.assertEqual(matroska.parse_block(body, 0, 0).frames,
                         [b"ab", b"c", b"d"])

    def test_fixed_size_lacing_splits_evenly(self):
        lace = bytes([2]) + b"xyzxyz"  # 3 frames; six bytes -> two each
        body = vint(1) + b"\x00\x00" + bytes([2 << 1]) + lace
        self.assertEqual(matroska.parse_block(body, 0, 0).frames,
                         [b"xy", b"zx", b"yz"])

    def test_empty_frame_rejected(self):
        body = vint(1) + b"\x00\x00" + bytes([0])  # no lacing, empty payload
        with self.assertRaises(matroska.MatroskaError):
            matroska.parse_block(body, 0, 0)

    def test_truncated_block_header_rejected(self):
        with self.assertRaises(matroska.MatroskaError):
            matroska.parse_block(b"\x81", 0, 0)


class TimestampTests(unittest.TestCase):
    def test_negative_start_shifted_to_zero_and_strictly_increasing(self):
        pts, dur = timestamps.expand_timestamps([-50, -50, 0, 40], 1_000_000, 1000)
        self.assertEqual(int(pts[0]), 0)
        self.assertTrue(bool((np.diff(pts) > 0).all()))
        self.assertEqual(len(dur), 4)
        self.assertGreaterEqual(int(dur[-1]), 1)

    def test_duplicate_timestamps_nudged_apart(self):
        pts, _ = timestamps.expand_timestamps([100, 100, 100], 1_000_000, 90_000)
        self.assertEqual(len(set(pts.tolist())), 3)

    def test_scale_and_rounding_to_integer_ticks(self):
        pts, _ = timestamps.expand_timestamps([0, 1, 2], 1_000_000, 90_000)
        self.assertEqual(pts.tolist(), [0, 90, 180])

    def test_overflow_rejected(self):
        with self.assertRaises(timestamps.TimestampError):
            timestamps.expand_timestamps([10 ** 15], 1_000_000, 1000)

    def test_empty_and_sample_limit(self):
        with self.assertRaises(timestamps.TimestampError):
            timestamps.expand_timestamps([], 1_000_000, 1000)
        too_many = [0] * (timestamps.MAX_SAMPLES_PER_TRACK + 1)
        with self.assertRaises(timestamps.TimestampError):
            timestamps.expand_timestamps(too_many, 1_000_000, 1000)

    def test_timescale_choice(self):
        self.assertEqual(timestamps.choose_timescale(2, 44100.0), 44100)
        self.assertEqual(timestamps.choose_timescale(1, 0.0), 90_000)


if __name__ == "__main__":
    unittest.main()
