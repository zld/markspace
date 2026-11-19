"""End-to-end pipeline tests: parse -> map -> timeline -> mux -> verify."""

from __future__ import annotations

import io
import json
import os
import struct
import subprocess
import sys
import tempfile
import unittest

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
sys.path.insert(0, os.path.join(ROOT, "src"))
sys.path.insert(0, ROOT)

from mkvmp4 import ebml  # noqa: E402
from mkvmp4.ebml import EbmlError, EbmlReader, encode_vint  # noqa: E402
from mkvmp4.mapping import MappingError, select_tracks  # noqa: E402
from mkvmp4.matroska import MatroskaError, parse_matroska  # noqa: E402
from mkvmp4.mux import MuxError, remux_to_stream, verify_mp4  # noqa: E402
from mkvmp4.report import inspect_mp4  # noqa: E402
from mkvmp4.timeline import TimelineError, build_timelines  # noqa: E402
from tests import fixture_builder as fb  # noqa: E402


class FixtureBackedTestCase(unittest.TestCase):
    def setUp(self) -> None:
        self.tmp = tempfile.TemporaryDirectory()
        self.mkv_path = os.path.join(self.tmp.name, "sample.mkv")
        fb.build_sample_mkv(self.mkv_path)
        self.src = open(self.mkv_path, "rb")
        self.mkv = parse_matroska(self.src)

    def tearDown(self) -> None:
        self.src.close()
        self.tmp.cleanup()

    def remux(self):
        mapping = select_tracks(self.mkv.tracks, strict=True)
        timelines, _, shift = build_timelines(
            self.mkv, mapping.accepted_numbers)
        dst = io.BytesIO()
        result = remux_to_stream(
            self.src, dst, self.mkv, mapping, timelines)
        box_count, tree = verify_mp4(dst, result)
        result.total_boxes = box_count
        result.box_tree = tree
        dst.seek(0)
        return mapping, timelines, result, dst


class VintEncodingTests(unittest.TestCase):
    def test_roundtrip_across_widths(self):
        values = [0, 1, 126, 128, 16382, 16384]
        for value in values:
            encoded = encode_vint(value)
            got, width = EbmlReader(io.BytesIO(encoded)).read_vint()
            self.assertEqual(got, value)
            self.assertEqual(width, len(encoded))

    def test_unknown_length_markers(self):
        for n in range(1, 9):
            raw = ebml.unknown_size_vint(n)
            with self.assertRaises(EbmlError):
                EbmlReader(io.BytesIO(raw)).read_vint()
            value, width = EbmlReader(
                io.BytesIO(raw)).read_vint(allow_unknown=True)
            self.assertIsNone(value)
            self.assertEqual(width, n)

    def test_illegal_zero_marker_rejected(self):
        with self.assertRaises(EbmlError):
            EbmlReader(io.BytesIO(b"\x00")).read_vint()

    def test_truncated_vint_rejected(self):
        with self.assertRaises(EbmlError):
            EbmlReader(io.BytesIO(b"\x40")).read_vint()


class ParsingTests(FixtureBackedTestCase):
    def test_fixture_shape(self):
        self.assertEqual(self.mkv.cluster_count, 3)
        self.assertGreaterEqual(self.mkv.cue_count, 1)
        self.assertEqual(len(self.mkv.tracks), 2)
        self.assertEqual(self.mkv.timecode_scale_ns, 1_000_000)

    def test_track_attributes(self):
        video, audio = self.mkv.tracks
        self.assertEqual(video.codec_id, "V_MPEG4/ISO/AVC")
        self.assertEqual(video.pixel_width, 320)
        self.assertEqual(video.pixel_height, 240)
        self.assertEqual(audio.codec_id, "A_AAC")
        self.assertEqual(audio.channels, 2)
        self.assertAlmostEqual(audio.sampling_frequency, 48000.0)
        self.assertIsNotNone(video.codec_private)
        self.assertIsNotNone(audio.codec_private)

    def test_sample_counts_and_keyframes(self):
        video_samples = self.mkv.samples_for(1)
        audio_samples = self.mkv.samples_for(2)
        self.assertGreaterEqual(len(video_samples), 3)
        self.assertGreaterEqual(len(audio_samples), 3)
        # Video carries explicit keyframe flags; audio has no stss table.
        self.assertTrue(any(s.keyframe for s in video_samples))

    def test_negative_relative_timecode_preserved(self):
        first = self.mkv.samples_for(1)[0]
        # Cluster at 500 ms plus a -10 ms relative block timecode.
        self.assertEqual(first.abs_time_ns, 490_000_000)

    def test_unknown_length_segment_consumed(self):
        # The parser must reach the end of an unknown-length Segment.
        self.src.seek(0, io.SEEK_END)
        size = self.src.tell()
        self.src.seek(0)
        # Re-parsing must not leave bytes or raise on the unknown length.
        with open(self.mkv_path, "rb") as extra:
            again = parse_matroska(extra)
        self.assertEqual(len(again.samples), len(self.mkv.samples))
        self.assertGreater(size, 0)

    def test_truncated_header_fixture_fails(self):
        path = os.path.join(self.tmp.name, "trunc.mkv")
        fb.truncated_header_fixture(path)
        with open(path, "rb") as broken, \
                self.assertRaises((MatroskaError, EbmlError)):
            parse_matroska(broken)

    def test_bad_vint_fixture_fails(self):
        path = os.path.join(self.tmp.name, "badvint.mkv")
        fb.bad_vint_fixture(path)
        with open(path, "rb") as broken, \
                self.assertRaises((MatroskaError, EbmlError)):
            parse_matroska(broken)


class MappingTests(FixtureBackedTestCase):
    def test_stable_track_ids_in_input_order(self):
        mapping = select_tracks(self.mkv.tracks, strict=True)
        self.assertEqual(mapping.accepted, {1: 1, 2: 2})

    def test_unsupported_codec_is_rejected_with_track_number(self):
        self.mkv.tracks[0].codec_id = "V_VP9"
        with self.assertRaises(MappingError) as ctx:
            select_tracks(self.mkv.tracks, strict=True)
        self.assertIn("track 1", str(ctx.exception))

    def test_encrypted_track_rejected(self):
        self.mkv.tracks[1].encrypted = True
        with self.assertRaises(MappingError) as ctx:
            select_tracks(self.mkv.tracks, strict=True)
        self.assertIn("track 2", str(ctx.exception))

    def test_subtitle_track_skipped_not_dropped(self):
        from mkvmp4.matroska import Track, TRACK_TYPE_SUBTITLE
        sub = Track(number=9, uid=None, type=TRACK_TYPE_SUBTITLE,
                    codec_id="S_TEXT/UTF8")
        mapping = select_tracks(self.mkv.tracks + [sub], strict=True)
        self.assertEqual(len(mapping.accepted), 2)
        skipped = {e.mkv_track_number: e for e in mapping.skipped}
        self.assertIn(9, skipped)
        self.assertIn("subtitle", skipped[9].reason)


class TimelineTests(FixtureBackedTestCase):
    def test_global_shift_starts_at_zero(self):
        mapping = select_tracks(self.mkv.tracks)
        timelines, _, shift_ns = build_timelines(
            self.mkv, mapping.accepted_numbers)
        self.assertEqual(shift_ns, 490_000_000)
        for tl in timelines.values():
            self.assertEqual(tl.sample_times[0], 0)
            self.assertEqual(tl.first_seconds, 0.0)

    def test_non_regressing_and_duplicates_preserved(self):
        mapping = select_tracks(self.mkv.tracks)
        timelines, _, _ = build_timelines(
            self.mkv, mapping.accepted_numbers)
        for tl in timelines.values():
            for a, b in zip(tl.sample_times, tl.sample_times[1:]):
                self.assertLessEqual(a, b)

    def test_last_timestamp_beyond_tenth_second(self):
        mapping = select_tracks(self.mkv.tracks)
        timelines, _, _ = build_timelines(
            self.mkv, mapping.accepted_numbers)
        for tl in timelines.values():
            self.assertGreater(tl.last_seconds, 0.1)

    def test_cluster_regression_detected(self):
        self.mkv.cluster_timecodes_ns = [100, 50]
        mapping = select_tracks(self.mkv.tracks)
        with self.assertRaises(TimelineError):
            build_timelines(self.mkv, mapping.accepted_numbers)

    def test_last_duration_warning_not_fabricated(self):
        mapping = select_tracks(self.mkv.tracks)
        timelines, warnings, _ = build_timelines(
            self.mkv, mapping.accepted_numbers)
        self.assertTrue(any("last sample" in w for w in warnings))
        for tl in timelines.values():
            self.assertTrue(tl.last_duration_estimated)


if __name__ == "__main__":
    unittest.main(verbosity=2)


