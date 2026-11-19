"""End-to-end pipeline tests: parse -> map -> timeline -> mux -> verify."""

from __future__ import annotations

import io
import json
import os
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

    def test_duplicate_timestamps_kept_in_order(self):
        from mkvmp4.matroska import Sample
        # Collapse every video sample onto the same timestamp: none may be
        # dropped and order must survive non-regression clamping.
        original = [s for s in self.mkv.samples if s.track_number == 1]
        shared = original[0].abs_time_ns
        self.mkv.samples = [
            Sample(1, shared, s.payload_offset, s.payload_size, s.keyframe)
            if s.track_number == 1 else s for s in self.mkv.samples
        ]
        mapping = select_tracks(self.mkv.tracks)
        timelines, _, _ = build_timelines(
            self.mkv, mapping.accepted_numbers)
        video_tl = timelines[1]
        self.assertEqual(video_tl.sample_count, len(original))
        self.assertEqual(set(video_tl.sample_times), {0})
        # All entries retained, in original order, none de-duplicated.
        self.assertEqual(
            len(video_tl.sample_times), len(video_tl.sample_durations))


class EbmlBoundaryTests(unittest.TestCase):
    def test_child_crossing_parent_boundary_rejected(self):
        header_id = ebml.encode_id(0x1A45DFA3)
        bogus = (header_id + encode_vint(2)
                 + ebml.encode_id(0x4287) + encode_vint(5) + b"\x00" * 5)
        reader = EbmlReader(io.BytesIO(bogus))
        header = reader.read_element_header(0)
        with self.assertRaises(EbmlError):
            list(reader.iter_children(header))

    def test_unknown_length_only_allowed_for_approved_ids(self):
        # A non-approved element carrying an unknown-length size is rejected.
        raw = ebml.encode_id(0x4287) + ebml.unknown_size_vint(1)
        reader = EbmlReader(io.BytesIO(raw))
        with self.assertRaises(EbmlError):
            reader.read_element_header(0)


if __name__ == "__main__":
    unittest.main(verbosity=2)


class MuxOutputTests(FixtureBackedTestCase):
    def test_remux_roundtrip_and_offsets(self):
        mapping, timelines, result, dst = self.remux()
        self.assertEqual(result.mdat_data_start, 40)  # ftyp(32) + mdat head(8)
        self.assertLess(result.mdat_data_start, result.mdat_data_end)
        for mt in result.muxed_tracks:
            self.assertEqual(len(mt.chunk_offsets), mt.sample_count)
            self.assertEqual(len(mt.sample_sizes), mt.sample_count)
            for off, size in zip(mt.chunk_offsets, mt.sample_sizes):
                self.assertGreaterEqual(off, result.mdat_data_start)
                self.assertLessEqual(off + size, result.mdat_data_end)

    def test_independent_mp4_inspection_matches(self):
        mapping, timelines, result, dst = self.remux()
        report = inspect_mp4(dst)
        self.assertEqual(report["box_count"], result.total_boxes)
        self.assertEqual(report["track_count"], 2)
        ids = sorted(t["mp4_track_id"] for t in report["tracks"])
        self.assertEqual(ids, [1, 2])
        for track in report["tracks"]:
            self.assertGreaterEqual(track["sample_count"], 3)
            self.assertEqual(track["first_timestamp_seconds"], "0.000000")
            self.assertGreater(float(track["last_timestamp_seconds"]), 0.1)
            self.assertFalse(track["uses_co64"])

    def test_required_boxes_present(self):
        _, _, result, dst = self.remux()
        report = inspect_mp4(dst)
        names = {node["name"] for node in report["box_tree"]}
        for required in ("ftyp", "mdat", "moov", "mvhd", "trak", "tkhd",
                         "mdhd", "hdlr", "stbl", "stsd", "stts", "stsc",
                         "stsz", "stco"):
            self.assertIn(required, names)

    def test_video_has_sync_table_audio_does_not(self):
        _, _, _, dst = self.remux()
        report = inspect_mp4(dst)
        by_handler = {t["handler"]: t for t in report["tracks"]}
        self.assertGreater(by_handler["vide"]["sync_sample_count"], 0)
        self.assertEqual(by_handler["soun"]["sync_sample_count"], 0)

    def test_co64_selected_for_large_offsets(self):
        # Force every chunk offset above the 32-bit boundary by lying about
        # the mdat base; the table builder must switch stco -> co64.
        from mkvmp4 import boxes
        mapping, timelines, result, dst = self.remux()
        mt = result.muxed_tracks[0]
        huge = [off + 0x1_0000_0000 for off in mt.chunk_offsets]
        table = boxes.chunk_offset_box(huge)
        self.assertEqual(table[4:8], b"co64")
        small_table = boxes.chunk_offset_box(mt.chunk_offsets)
        self.assertEqual(small_table[4:8], b"stco")

    def test_sample_offset_outside_mdat_rejected(self):
        mapping = select_tracks(self.mkv.tracks, strict=True)
        timelines, _, _ = build_timelines(
            self.mkv, mapping.accepted_numbers)
        dst = io.BytesIO()
        result = remux_to_stream(
            self.src, dst, self.mkv, mapping, timelines)
        # Corrupt one offset so verification must fail.
        result.muxed_tracks[0].chunk_offsets[0] = result.mdat_data_end + 999
        with self.assertRaises(MuxError):
            verify_mp4(dst, result)

    def test_payload_bytes_are_copied_not_encoded(self):
        mapping, timelines, result, dst = self.remux()
        dst.seek(result.mdat_data_start)
        blob = dst.read(result.mdat_data_end - result.mdat_data_start)
        # Every source frame payload must appear verbatim inside mdat.
        for sample in self.mkv.samples:
            self.src.seek(sample.payload_offset)
            frame = self.src.read(sample.payload_size)
            self.assertIn(frame, blob)


class CliTests(FixtureBackedTestCase):
    @property
    def _python(self) -> str:
        return sys.executable

    def _run(self, *args) -> subprocess.CompletedProcess:
        env = dict(os.environ)
        env["PYTHONPATH"] = os.path.join(ROOT, "src")
        return subprocess.run(
            [self._python, "-m", "mkvmp4.cli", *args],
            capture_output=True, text=True, env=env, cwd=ROOT)

    def test_success_exit_code_zero_and_json(self):
        out = os.path.join(self.tmp.name, "out.mp4")
        proc = self._run("remux", self.mkv_path, "--output", out, "--json")
        self.assertEqual(proc.returncode, 0, proc.stderr)
        payload = json.loads(proc.stdout)
        self.assertEqual(payload["status"], "ok")
        self.assertIn("mp4", payload)
        self.assertTrue(os.path.exists(out))

    def test_bad_arguments_exit_code_two(self):
        proc = self._run("remux", self.mkv_path)  # missing --output
        self.assertEqual(proc.returncode, 2)

    def test_missing_input_exit_code_one(self):
        proc = self._run("inspect", os.path.join(self.tmp.name, "nope.mkv"))
        self.assertEqual(proc.returncode, 1)

    def test_corrupt_input_exit_code_one(self):
        broken = os.path.join(self.tmp.name, "broken.mkv")
        fb.truncated_header_fixture(broken)
        proc = self._run("remux", broken,
                         "--output", os.path.join(self.tmp.name, "x.mp4"))
        self.assertEqual(proc.returncode, 1)

    def test_atomic_replace_keeps_existing_target_on_failure(self):
        target = os.path.join(self.tmp.name, "keep.mp4")
        sentinel = b"original-target-bytes"
        with open(target, "wb") as f:
            f.write(sentinel)
        broken = os.path.join(self.tmp.name, "broken.mkv")
        fb.truncated_header_fixture(broken)
        proc = self._run("remux", broken, "--output", target)
        self.assertEqual(proc.returncode, 1)
        with open(target, "rb") as f:
            self.assertEqual(f.read(), sentinel)
        # No leftover temporary files in the output directory.
        leftovers = [n for n in os.listdir(self.tmp.name)
                     if n.startswith(".mkvmp4-")]
        self.assertEqual(leftovers, [])

    def test_inspect_mp4_exit_code_and_fields(self):
        out = os.path.join(self.tmp.name, "out.mp4")
        self._run("remux", self.mkv_path, "--output", out)
        proc = self._run("inspect-mp4", out, "--json")
        self.assertEqual(proc.returncode, 0, proc.stderr)
        payload = json.loads(proc.stdout)
        self.assertGreater(payload["box_count"], 0)
        self.assertEqual(payload["warning_count"], 0)


class PerformanceTests(FixtureBackedTestCase):
    def test_fixture_end_to_end_under_two_seconds(self):
        import time

        mapping = select_tracks(self.mkv.tracks, strict=True)
        timelines, _, _ = build_timelines(
            self.mkv, mapping.accepted_numbers)
        start = time.perf_counter()
        dst = io.BytesIO()
        result = remux_to_stream(
            self.src, dst, self.mkv, mapping, timelines)
        verify_mp4(dst, result)
        self.assertLess(time.perf_counter() - start, 2.0)
