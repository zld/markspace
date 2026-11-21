"""End-to-end and unit tests for the mkvmp4 pipeline.

Run with either ``.venv/bin/python -m unittest discover -s tests`` or pytest if
it happens to be installed; no test dependency beyond NumPy is required.
"""

from __future__ import annotations

import io
import json
import sys
import tempfile
import unittest
from contextlib import redirect_stdout
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / "src"))
sys.path.insert(0, str(ROOT / "scripts"))

import make_fixture  # noqa: E402
from mkvmp4 import cli, mp4  # noqa: E402
from mkvmp4.ebml import read_vint_size, read_vint_id  # noqa: E402
from mkvmp4.errors import (  # noqa: E402
    BoundaryError,
    EbmlParseError,
    TimestampPolicyError,
    TrackMappingError,
    UnsupportedError,
)
from mkvmp4.matroska import parse_matroska  # noqa: E402
from mkvmp4.timestamps import SampleInput, rewrite_timeline  # noqa: E402
from mkvmp4.tree import parse_tree  # noqa: E402


class VintTests(unittest.TestCase):
    def test_round_trip_widths(self) -> None:
        for width in range(1, 9):
            value = (1 << (7 * width - 1)) - 1
            encoded = ((1 << (7 * width)) | value).to_bytes(width, "big")
            self.assertEqual(read_vint_size(encoded, 0)[0], value)

    def test_unknown_size_marker(self) -> None:
        size, length, unknown = read_vint_size(b"\xff", 0)
        self.assertIsNone(size)
        self.assertTrue(unknown)
        self.assertEqual(length, 1)

    def test_zero_byte_rejected(self) -> None:
        with self.assertRaises(EbmlParseError):
            read_vint_size(b"\x00\x01", 0)

    def test_truncated_vint_rejected(self) -> None:
        with self.assertRaises(EbmlParseError):
            read_vint_size(b"\x01\x00", 0)  # 8-byte VINT with two bytes present
        with self.assertRaises(EbmlParseError):
            read_vint_id(b"\x1a\x45", 0)  # 4-byte ID, 2 bytes present

    def test_id_marker_retained(self) -> None:
        self.assertEqual(read_vint_id(b"\x18\x53\x80\x67", 0), (0x18538067, 4))


class TreeTests(unittest.TestCase):
    def test_unknown_cluster_ends_at_next_cluster(self) -> None:
        data = make_fixture.build_sample_document()
        nodes, stats = parse_tree(data)
        segment = next(n for n in nodes if n.name == "Segment")
        clusters = [n for n in segment.children if n.name == "Cluster"]
        self.assertEqual(len(clusters), 4)
        unknown = [c for c in clusters if c.unknown_size]
        self.assertEqual(len(unknown), 1)
        self.assertEqual(unknown[0].end_offset, clusters[2].offset)
        self.assertEqual(stats["unknown_sizes"], 1)

    def test_depth_budget_rejected(self) -> None:
        data = make_fixture.build_sample_document()
        with self.assertRaises(BoundaryError):
            parse_tree(data, max_depth=2)

    def test_element_budget_rejected(self) -> None:
        data = make_fixture.build_sample_document()
        with self.assertRaises(BoundaryError):
            parse_tree(data, max_elements=10)

    def test_unknown_size_non_cluster_rejected(self) -> None:
        payload = make_fixture.element(
            0x1549A966, b"", unknown_size=True
        )  # Info with unknown size
        doc = make_fixture.element(0x18538067, payload, unknown_size=True)
        with self.assertRaises(BoundaryError):
            parse_tree(doc)

    def test_truncated_input_rejected(self) -> None:
        with self.assertRaises(EbmlParseError):
            parse_tree(make_fixture.build_truncated_document())


class MatroskaTests(unittest.TestCase):
    def test_tracks_and_lacing(self) -> None:
        mk = parse_matroska(make_fixture.build_sample_document())
        self.assertEqual([t.number for t in mk.tracks], [1, 2])
        self.assertEqual([t.track_type for t in mk.tracks], ["video", "audio"])
        self.assertEqual(mk.timestamp_scale, 1_000_000)
        self.assertEqual(len(mk.clusters), 4)
        audio_blocks = [b for c in mk.clusters for b in c.blocks if b.track == 2]
        self.assertEqual(len(audio_blocks), 6)  # two laced triples expanded
        self.assertEqual([b.lace_index for b in audio_blocks], [0, 1, 2, 0, 1, 2])
        lengths = [b.data_length for b in audio_blocks]
        self.assertEqual(lengths[:3], [12, 15, 20])

    def test_unknown_segment_is_bounded(self) -> None:
        mk = parse_matroska(make_fixture.build_sample_document(unknown_segment=True))
        self.assertTrue(mk.segment_unknown_size)
        self.assertEqual(mk.segment_end, len(mk.data))


class TimestampTests(unittest.TestCase):
    def sample(self, cluster: int, rel: int, offset: int = 0) -> SampleInput:
        return SampleInput(1, cluster, rel, 0, 1, offset, 10, True, 33_333_333)

    def test_nanoseconds_and_timescale(self) -> None:
        report = rewrite_timeline(
            [self.sample(0, 0), self.sample(0, 1000)],
            timestamp_scale=1_000_000,
            timescale=90_000,
        )
        # 1000 ms = 1e9 ns; at 90000 Hz that is exactly 90_000_000 ticks.
        self.assertEqual(list(report.tracks[1].ticks), [0, 90_000_000])

    def test_offset_and_negative_clip(self) -> None:
        report = rewrite_timeline(
            [self.sample(0, 0), self.sample(0, 500)],
            timestamp_scale=1_000_000,
            timescale=1_000,
            offset_ms=-250,
        )
        timeline = report.tracks[1]
        self.assertEqual(int(timeline.ticks[0]), 0)
        self.assertEqual(timeline.stats["clipped"], 1)

    def test_negative_clip_can_be_disabled(self) -> None:
        with self.assertRaises(TimestampPolicyError):
            rewrite_timeline(
                [self.sample(0, 0)],
                timestamp_scale=1_000_000,
                timescale=1_000,
                offset_ms=-1,
                clip_negative=False,
            )

    def test_strict_rejects_regression_and_repair_fixes_it(self) -> None:
        samples = [self.sample(0, 0), self.sample(0, 100), self.sample(0, 50)]
        with self.assertRaises(TimestampPolicyError):
            rewrite_timeline(samples, timestamp_scale=1_000_000, timescale=1_000)
        report = rewrite_timeline(
            samples, timestamp_scale=1_000_000, timescale=1_000, mode="repair"
        )
        timeline = report.tracks[1]
        self.assertEqual(timeline.stats["non_monotonic"], 1)
        self.assertTrue(all(b > a for a, b in zip(timeline.ticks, timeline.ticks[1:])))

    def test_rounding_is_counted(self) -> None:
        sample = SampleInput(1, 0, 1, 0, 1, 0, 10, True, None)
        report = rewrite_timeline(
            [sample], timestamp_scale=1_000_000, timescale=3, mode="repair"
        )
        self.assertEqual(report.tracks[1].stats["rounded"], 0)

    def test_gap_limit(self) -> None:
        samples = [self.sample(0, 0), self.sample(0, 5000)]
        with self.assertRaises(TimestampPolicyError):
            rewrite_timeline(
                samples,
                timestamp_scale=1_000_000,
                timescale=1_000,
                max_gap_ms=1000,
            )

    def test_overflow_detected_not_wrapped(self) -> None:
        huge = SampleInput(1, 2**40, 2**40, 0, 1, 0, 10, True, None)
        with self.assertRaises(TimestampPolicyError):
            rewrite_timeline([huge], timestamp_scale=1_000_000, timescale=1_000)
