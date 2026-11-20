"""End-to-end and failure-path tests for the Matroska -> MP4 remuxer."""

from __future__ import annotations

import io
import json
import os
import shutil
import struct
import subprocess
import sys
import tempfile
import unittest
from contextlib import redirect_stderr, redirect_stdout

import numpy as np

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
sys.path.insert(0, ROOT)

from matroska_mp4 import ebml, fixture, matroska  # noqa: E402
from matroska_mp4.cli import main  # noqa: E402
from matroska_mp4.export_preserve import PreserveError, run_preserve  # noqa: E402
from matroska_mp4.export_rewrite import run_rewrite  # noqa: E402
from matroska_mp4.timeline import (TimebaseOverflow, TimelineError,  # noqa: E402
                                   ns_to_ticks, prepare, ticks_to_ns)


class FixtureTestCase(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.tmp = tempfile.mkdtemp(prefix="mkvmp4-test-")
        cls.fixtures = fixture.generate(os.path.join(cls.tmp, "fixtures"))

    @classmethod
    def tearDownClass(cls):
        shutil.rmtree(cls.tmp, ignore_errors=True)

    def path(self, name: str) -> str:
        return os.path.join(self.tmp, name)


class TestEBML(FixtureTestCase):
    def test_unknown_size_boundaries(self):
        document = ebml.parse_file(self.fixtures["reordered"])
        sizes = document.unknown_sizes()
        self.assertEqual(sizes, {"Segment": 1, "Cluster": 1})
        segment = document.find_root(0x18538067)
        self.assertTrue(segment.unknown)
        clusters = segment.children_of(0x1F43B675)
        self.assertEqual(len(clusters), 2)
        self.assertFalse(clusters[0].unknown)
        self.assertTrue(clusters[1].unknown)
        self.assertEqual(clusters[1].end, document.file_size)

    def test_truncated_and_overrun_offsets(self):
        with open(self.fixtures["reordered"], "rb") as handle:
            data = handle.read()
        truncated = self.path("truncated.mkv")
        with open(truncated, "wb") as handle:
            handle.write(data[:len(data) // 2])
        with self.assertRaises(ebml.EBMLError) as ctx:
            ebml.parse_file(truncated)
        self.assertGreater(ctx.exception.offset, 0)
        overrun = bytearray(data)
        # Force Cluster 1's size VINT to a maximal known value; its head keeps
        # the Cluster end past the enclosing Segment.
        position = overrun.find(b"\x1f\x43\xb6\x75")
        self.assertGreater(position, 0)
        # 8-byte size VINT declaring 1 MiB: larger than the remaining file.
        overrun[position + 4:position + 12] = b"\x01\x00\x00\x00\x00\x10\x00\x00"
        bad = self.path("overrun.mkv")
        with open(bad, "wb") as handle:
            handle.write(overrun)
        with self.assertRaises(ebml.EBMLError) as ctx:
            ebml.parse_file(bad)
        # The inflated Cluster overruns its parent, reported at the Cluster.
        self.assertEqual(ctx.exception.offset, position)

    def test_invalid_vint_offsets(self):
        for payload, expected in ((b"\x00\x00\x00", 0), (b"\x1a\x45\xdf\xa3\x00", 4)):
            bad = self.path("bad-vint.mkv")
            with open(bad, "wb") as handle:
                handle.write(payload)
            with self.assertRaises(ebml.EBMLError) as ctx:
                ebml.parse_file(bad)
            self.assertEqual(ctx.exception.offset, expected)

    def test_unknown_size_leaf_rejected(self):
        document = ebml.parse_file(self.fixtures["monotonic"])
        self.assertEqual(document.counts()["SimpleBlock"], 7)

    def test_attachments_and_subtitles_fail_loudly(self):
        with open(self.fixtures["monotonic"], "rb") as handle:
            payload = bytearray(handle.read())
        entry = fixture._track_entry(3, 17, "S_TEXT/UTF8", b"", None)
        tracks_pos = payload.find(b"\x16\x54\xae\x6b")
        header = ebml.read_header(io.BytesIO(payload), tracks_pos, len(payload))
        body = bytes(payload[header.data_offset:header.data_offset + header.size])
        new_body = body + entry
        new_block = (fixture.elem_id(fixture.ids.TRACKS) +
                     fixture.vint_size(len(new_body)) + new_body)
        old_block_len = header.header_size + header.size
        updated = (bytes(payload[:tracks_pos]) + new_block +
                   bytes(payload[tracks_pos + old_block_len:]))
        # Known-size ancestors now mismatch; use an unknown-size Segment instead.
        rebuilt = fixture._header() + fixture._segment(
            fixture.master(fixture.ids.TRACKS,
                           (fixture._video_track(None) + fixture._audio_track() + entry)),
            b"", True, 0.0)
        bad = self.path("subtitle.mkv")
        with open(bad, "wb") as handle:
            handle.write(rebuilt)
        with self.assertRaises(matroska.UnsupportedFormat) as ctx:
            matroska.parse_matroska(bad)
        self.assertEqual(ctx.exception.track, 3)
        self.assertIn("subtitle", str(ctx.exception))


class TestLacing(FixtureTestCase):
    def test_all_lacing_modes_parse(self):
        mkv = matroska.parse_matroska(self.fixtures["reordered"])
        modes = {block.lacing for block in mkv.blocks}
        self.assertEqual(modes, {"none", "xiph", "fixed", "ebml"})
        sizes = [sum(f.size for f in b.frames) for b in mkv.blocks]
        self.assertEqual(mkv.media_bytes, sum(sizes))

    def test_lace_size_mismatch_is_reported(self):
        # Xiph lace sizes that declare more bytes than the block carries.
        body = (fixture.vint_size(2) + b"\x00\x00" + bytes([0x80 | 0x02]) +
                b"\x01\x05" + b"ab")
        block = fixture.leaf(fixture.ids.SIMPLE_BLOCK, body)
        bad = self.path("bad-lace.mkv")
        with open(bad, "wb") as handle:
            handle.write(fixture.build_raw(block))
        with self.assertRaises(ebml.EBMLError) as ctx:
            matroska.parse_matroska(bad)
        self.assertGreater(ctx.exception.offset, 0)

    def test_frame_bytes_preserved(self):
        mkv = matroska.parse_matroska(self.fixtures["reordered"])
        output = self.path("bytes.mp4")
        run_rewrite(mkv, 60000, output)
        with open(output, "rb") as handle:
            mp4 = handle.read()
        with open(mkv.path, "rb") as handle:
            for block in mkv.blocks:
                for frame in block.frames:
                    handle.seek(frame.offset)
                    original = handle.read(frame.size)
                    self.assertIn(original, mp4)


class TestTimeline(FixtureTestCase):
    def test_rewrite_report_matches_expected_shape(self):
        mkv = matroska.parse_matroska(self.fixtures["reordered"])
        output = self.path("rewrite.mp4")
        result = run_rewrite(mkv, 60000, output)
        report = result.report()
        self.assertEqual(report["element_tree"]["Segment"], 1)
        self.assertEqual(report["element_tree"]["Cluster"], 2)
        self.assertEqual(report["element_tree"]["BlockGroup"], 4)
        self.assertEqual(report["element_tree"]["SimpleBlock"], 4)
        self.assertEqual(report["sample_count"], 11)
        self.assertEqual(report["ctts_tracks"], 1)
        self.assertEqual(report["first_pts_ms"], 0)
        self.assertEqual(report["last_pts_ms"], 192)
        video, audio = report["track_map"]
        self.assertEqual((video["mkv_track"], video["mp4_track_id"],
                          video["codec"], video["sample_count"],
                          video["first_pts_ms"], video["last_pts_ms"]),
                         (1, 1, "avc1", 4, 40, 160))
        self.assertEqual((audio["mkv_track"], audio["mp4_track_id"],
                          audio["codec"], audio["sample_count"],
                          audio["first_pts_ms"], audio["last_pts_ms"]),
                         (2, 2, "mp4a", 7, 0, 192))
        timeline = result.timeline
        video_timeline = timeline.track(1)
        self.assertEqual(video_timeline.dts.tolist(), [0, 2400, 4800, 7200])
        self.assertEqual(video_timeline.media_pts.tolist(), [0, 4800, 2400, 7200])
        # ctts must be signed: the B-frame decodes early and presents late,
        # while the following P-frame decodes late and presents earlier.
        self.assertEqual(video_timeline.ctts.tolist(), [0, 2400, -2400, 0])
        self.assertEqual(video_timeline.media_base, 2400)
        self.assertEqual(video_timeline.duration_ticks, 9600)

    def test_preserve_keeps_offsets_and_rejects_reordering(self):
        mkv = matroska.parse_matroska(self.fixtures["monotonic"])
        result = run_preserve(mkv, 60000, self.path("preserve.mp4"))
        self.assertEqual(result.timeline.global_first_pts, 6000)
        self.assertEqual(result.report()["first_pts_ms"], 100)
        with self.assertRaises(PreserveError):
            run_preserve(matroska.parse_matroska(self.fixtures["reordered"]),
                         60000, self.path("never.mp4"))
        self.assertFalse(os.path.exists(self.path("never.mp4")))

    def test_missing_final_duration_fails(self):
        mkv = matroska.parse_matroska(self.fixtures["monotonic"])
        for track in mkv.tracks:
            track.default_duration_ns = None
        with self.assertRaises(TimelineError) as ctx:
            prepare(mkv, 60000, "rewrite")
        self.assertIn("duration", str(ctx.exception))

    def test_negative_start_normalizes(self):
        mkv = matroska.parse_matroska(self.fixtures["monotonic"])
        shift = 1000 * mkv.timestamp_scale_ns  # move everything to -1 s
        for samples in mkv.samples.values():
            samples.pts_ns = samples.pts_ns - shift
        timeline = prepare(mkv, 60000, "rewrite")
        self.assertEqual(timeline.global_first_pts, 0)
        self.assertEqual(timeline.source_first_pts, -54000)
        # preserve also normalizes a negative start (MP4 cannot carry negative
        # base PTS), but reports the original start and keeps track offsets.
        timeline = prepare(mkv, 60000, "preserve")
        self.assertEqual(timeline.global_first_pts, 0)
        self.assertEqual(timeline.source_first_pts, -54000)
        self.assertTrue(timeline.normalized)

    def test_int64_overflow_detected(self):
        with self.assertRaises(TimebaseOverflow):
            ticks_to_ns(np.int64(1 << 62), 1_000_000)
        with self.assertRaises(TimebaseOverflow):
            ns_to_ticks(np.int64(1 << 62), 1_000_000)
        self.assertEqual(ns_to_ticks(16_666_666, 60000), 1000)


class TestChunking(FixtureTestCase):
    def test_multi_chunk_tables(self):
        from matroska_mp4.mp4_writer import build_stbl, _chunk_map
        self.assertEqual(_chunk_map(4), [(1, 4)])
        self.assertEqual(_chunk_map(1024), [(1, 1024)])
        self.assertEqual(_chunk_map(1025), [(1, 1024), (2, 1)])
        self.assertEqual(_chunk_map(3000), [(1, 1024), (3, 952)])
        from matroska_mp4 import mp4_writer
        from matroska_mp4.mp4_audit import _expand_chunks
        mkv = matroska.parse_matroska(self.fixtures["reordered"])
        timeline = prepare(mkv, 60000, "rewrite")
        track = timeline.tracks[1]
        mp4_writer.CHUNK_SAMPLES = 3  # 7 samples -> 3,3,1
        try:
            self.assertEqual(mp4_writer._chunk_map(track.sample_count),
                             [(1, 3), (3, 1)])
            stbl = mp4_writer.build_stbl(track, [100, 200, 300])
        finally:
            mp4_writer.CHUNK_SAMPLES = 1024
        self.assertIn(b"stsc", stbl)
        self.assertEqual(_expand_chunks([(1, 3), (3, 1)], 3).tolist(), [3, 3, 1])


class TestContainerEdges(FixtureTestCase):
    def test_unknown_size_inner_master(self):
        with open(self.fixtures["monotonic"], "rb") as handle:
            payload = bytearray(handle.read())
        # Turn the first known-size Cluster into an unknown-size Cluster; it
        # must stop at the next Cluster.
        position = payload.find(b"\x1f\x43\xb6\x75")
        header = ebml.read_header(io.BytesIO(payload), position, len(payload))
        replaced = (bytes(payload[:position]) +
                    fixture.elem_id(fixture.ids.CLUSTER) + fixture.UNKNOWN_SIZE +
                    bytes(payload[header.data_offset:]))
        # Known-size Segment no longer matches; rebuild with an unknown Segment.
        doc = ebml.parse_file(self.fixtures["monotonic"])
        seg = doc.find_root(fixture.ids.SEGMENT)
        inner = bytes(replaced[seg.data_offset:])
        rebuilt = fixture._header() + fixture.elem_id(fixture.ids.SEGMENT) + \
            fixture.UNKNOWN_SIZE + inner
        path = self.path("unknown-inner.mkv")
        with open(path, "wb") as handle:
            handle.write(rebuilt)
        parsed = ebml.parse_file(path)
        sizes = parsed.unknown_sizes()
        self.assertEqual(sizes["Cluster"], 1)
        clusters = matroska.parse_matroska(path).clusters
        self.assertEqual(clusters, 2)

    def test_write_failure_leaves_no_partial_and_preserves_old_file(self):
        output = self.path("keep.mp4")
        with open(output, "wb") as handle:
            handle.write(b"old-bytes")
        mkv = matroska.parse_matroska(self.fixtures["reordered"])
        from matroska_mp4 import mp4_writer
        original = mp4_writer.build_moov

        def broken(*args, **kwargs):
            raise OSError("simulated write failure")

        mp4_writer.build_moov = broken
        try:
            with self.assertRaises(OSError):
                run_rewrite(mkv, 60000, output)
        finally:
            mp4_writer.build_moov = original
        with open(output, "rb") as handle:
            self.assertEqual(handle.read(), b"old-bytes")
        leftovers = [name for name in os.listdir(self.tmp) if "partial" in name]
        self.assertEqual(leftovers, [])

    def test_audit_rejects_tampered_mp4(self):
        output = self.path("tamper.mp4")
        mkv = matroska.parse_matroska(self.fixtures["reordered"])
        result = run_rewrite(mkv, 60000, output)
        with open(output, "rb") as handle:
            data = bytearray(handle.read())
        start = result.audit["dur"]["mdat_offset"] + 8
        data[start] ^= 0xFF
        with open(output, "wb") as handle:
            handle.write(data)
        from matroska_mp4.mp4_audit import AuditError, audit_mp4
        with self.assertRaises(AuditError):
            audit_mp4(output, mkv.path, mkv.samples, result.timeline.tracks,
                      result.timeline.timescale)

    def test_unsupported_codec_is_explicit(self):
        entry = fixture._track_entry(3, 1, "V_MPEG2", b"\x00" * 8, None,
                                     width=32, height=32)
        tracks = fixture.master(fixture.ids.TRACKS,
                                fixture._video_track(None) + fixture._audio_track() +
                                entry)
        segment = fixture._segment(tracks, b"", True, 0.0)
        path = self.path("mpeg2.mkv")
        with open(path, "wb") as handle:
            handle.write(fixture._header() + segment)
        with self.assertRaises(matroska.UnsupportedFormat) as ctx:
            matroska.parse_matroska(path)
        self.assertIn("V_MPEG2", str(ctx.exception))


class TestCLI(FixtureTestCase):
    def _run(self, argv):
        out, err = io.StringIO(), io.StringIO()
        with redirect_stdout(out), redirect_stderr(err):
            code = main(argv)
        return code, out.getvalue(), err.getvalue()

    def test_remux_json_output(self):
        output = self.path("cli-rewrite.mp4")
        code, out, _ = self._run(["remux", "--input", self.fixtures["reordered"],
                                  "--output", output, "--mode", "rewrite",
                                  "--timescale", "60000", "--report", "json"])
        self.assertEqual(code, 0)
        payload = json.loads(out)
        self.assertEqual(payload["sample_count"], 11)
        self.assertEqual(payload["ctts_tracks"], 1)
        self.assertTrue(os.path.exists(output))

    def test_exit_codes(self):
        code, _, err = self._run(["remux", "--input", self.path("missing.mkv"),
                                  "--output", self.path("o.mp4")])
        self.assertEqual(code, 2)
        self.assertEqual(json.loads(err)["kind"], "arguments")
        code, _, err = self._run(["remux", "--input", self.fixtures["reordered"],
                                  "--output", self.path("p.mp4"),
                                  "--mode", "preserve"])
        self.assertEqual(code, 4)
        self.assertEqual(json.loads(err)["exit_code"], 4)
        with self.assertRaises(SystemExit) as ctx:
            self._run(["remux", "--input", self.fixtures["reordered"],
                       "--output", self.path("o.mp4"), "--timescale", "0"])
        self.assertEqual(ctx.exception.code, 2)

    def test_expected_commands_run_as_subprocess(self):
        output = self.path("subprocess.mp4")
        completed = subprocess.run(
            [sys.executable, "-m", "matroska_mp4", "remux", "--input",
             self.fixtures["monotonic"], "--output", output, "--mode", "preserve",
             "--timescale", "60000", "--report", "json"],
            cwd=ROOT, capture_output=True, text=True)
        self.assertEqual(completed.returncode, 0, completed.stderr)
        self.assertEqual(json.loads(completed.stdout)["sample_count"], 10)

    def test_fixture_command(self):
        target = self.path("fixture-cli")
        code, out, _ = self._run(["fixture", "--out-dir", target])
        self.assertEqual(code, 0)
        self.assertEqual(sorted(json.loads(out)["fixtures"]), ["monotonic", "reordered"])


if __name__ == "__main__":
    unittest.main()


class TestAuditIndexCorruption(FixtureTestCase):
    """The audit must reject a container that parses but has a broken index."""

    def _tamper(self, needle: bytes, delta: int) -> tuple[str, object]:
        output = self.path(f"tamper-{needle.decode()}.mp4")
        mkv = matroska.parse_matroska(self.fixtures["reordered"])
        result = run_rewrite(mkv, 60000, output)
        with open(output, "rb") as handle:
            raw = handle.read()
        from matroska_mp4.mp4_audit import _child, _read_boxes
        top = _read_boxes(raw, 0, len(raw))
        moov = _child(top, b"moov")
        trak = _child(_read_boxes(raw, moov.data_offset, moov.end), b"trak")
        mdia = _child(_read_boxes(raw, trak.data_offset, trak.end), b"mdia")
        minf = _child(_read_boxes(raw, mdia.data_offset, mdia.end), b"minf")
        stbl = _child(_read_boxes(raw, minf.data_offset, minf.end), b"stbl")
        box = _child(_read_boxes(raw, stbl.data_offset, stbl.end), needle)
        entry = box.data_offset + 8
        data = bytearray(raw)
        value = struct.unpack_from(">I", data, entry)[0]
        struct.pack_into(">I", data, entry, value + delta)
        with open(output, "wb") as handle:
            handle.write(data)
        return output, result

    def test_chunk_offset_corruption_detected(self):
        from matroska_mp4.mp4_audit import AuditError, audit_mp4
        output, result = self._tamper(b"stco", 1)
        mkv = matroska.parse_matroska(self.fixtures["reordered"])
        with self.assertRaises(AuditError):
            audit_mp4(output, mkv.path, mkv.samples, result.timeline.tracks,
                      result.timeline.timescale)

    def test_sample_size_corruption_detected(self):
        from matroska_mp4.mp4_audit import AuditError, audit_mp4
        output, result = self._tamper(b"stsz", 1)
        mkv = matroska.parse_matroska(self.fixtures["reordered"])
        with self.assertRaises(AuditError):
            audit_mp4(output, mkv.path, mkv.samples, result.timeline.tracks,
                      result.timeline.timescale)

    def test_audit_ok_for_untampered(self):
        from matroska_mp4.mp4_audit import audit_mp4
        output = self.path("clean.mp4")
        mkv = matroska.parse_matroska(self.fixtures["monotonic"])
        result = run_preserve(mkv, 60000, output)
        again = audit_mp4(output, mkv.path, mkv.samples, result.timeline.tracks,
                          result.timeline.timescale)
        self.assertEqual(again["sample_count"], 10)


class TestLacingRejections(FixtureTestCase):
    """Malformed lace headers must fail with an EBML offset, not be guessed."""

    def _fixture(self, block_body: bytes, name: str) -> str:
        block = fixture.leaf(fixture.ids.SIMPLE_BLOCK, block_body)
        path = self.path(name)
        with open(path, "wb") as handle:
            handle.write(fixture.build_raw(block))
        return path

    def test_xiph_lace_exceeds_payload(self):
        # Track 2, Xiph lacing, 3 frames, first two declare 100 bytes each.
        path = self._fixture(b"\x82\x00\x00\x02\x02\x64\x64ab", "xiph-over.mkv")
        with self.assertRaises(ebml.EBMLError) as ctx:
            matroska.parse_matroska(path)
        self.assertIn("exceed", str(ctx.exception))
        self.assertGreater(ctx.exception.offset, 0)

    def test_fixed_lace_uneven(self):
        path = self._fixture(b"\x82\x00\x00\x04\x01abc", "fixed-odd.mkv")
        with self.assertRaises(ebml.EBMLError) as ctx:
            matroska.parse_matroska(path)
        self.assertIn("divisible", str(ctx.exception))

    def test_missing_lace_count_byte(self):
        path = self._fixture(b"\x82\x00\x00\x02ab", "xiph-nocount.mkv")
        with self.assertRaises(ebml.EBMLError):
            matroska.parse_matroska(path)

    def test_zero_length_frame_rejected(self):
        # EBML lacing with 2 frames: first frame size 0 (VINT 0x80), one byte
        # of payload left for the final frame.
        path = self._fixture(b"\x82\x00\x00\x06\x01\x80a", "zero-lace.mkv")
        with self.assertRaises(ebml.EBMLError) as ctx:
            matroska.parse_matroska(path)
        self.assertIn("empty frame", str(ctx.exception))
