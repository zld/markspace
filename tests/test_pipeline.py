"""End-to-end and unit tests for the mediacheck pipeline.

Fixtures are synthesized in-memory through the same builders used by
scripts/make_samples.py; no external files or probing tools are required.
"""

from __future__ import annotations

import json
import os
import struct
import sys
import tempfile
import unittest

sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)),
                                "..", "src"))

from mediacheck.compare import (  # noqa: E402
    DURATION_DIFF, EXTRA, MISSING, PARAM_DIFF, compare_results,
    match_tracks,
)
from mediacheck.container_probe import probe_file, probe_many  # noqa: E402
from mediacheck.formats import (  # noqa: E402
    ContainerError, parse_matroska, parse_mp4,
)
from mediacheck.sampling.builders import build_matroska, build_mp4  # noqa: E402
from mediacheck.store import StorageError, connect, save_scan  # noqa: E402
from mediacheck.tracks import build_tracks, normalize_codec  # noqa: E402
from mediacheck.report import ReportError, write_json  # noqa: E402


VIDEO_SPEC = {"type": "video", "codec": "avc1",
              "width": 1920, "height": 1080}
AUDIO_SPEC = {"type": "audio", "codec": "mp4a",
              "sample_rate": 48000, "channels": 2}


class _BytesFile:
    def __init__(self, data: bytes):
        import io
        self._io = io.BytesIO(data)

    def seek(self, pos):
        return self._io.seek(pos)

    def read(self, n=-1):
        return self._io.read(n)


class MP4ParsingTests(unittest.TestCase):
    def setUp(self):
        self.data = build_mp4(1000, 1000, [VIDEO_SPEC, AUDIO_SPEC])
        self.parsed = parse_mp4(_BytesFile(self.data), len(self.data))

    def test_container_and_duration(self):
        self.assertEqual(self.parsed["container"], "mp4")
        self.assertEqual(self.parsed["duration_ms"], 1000)

    def test_track_ids_and_fields(self):
        tracks = self.parsed["tracks"]
        self.assertEqual([t["track_id"] for t in tracks], [1, 2])
        self.assertEqual(tracks[0]["type"], "video")
        self.assertEqual((tracks[0]["width"], tracks[0]["height"]),
                         (1920, 1080))
        self.assertIsNone(tracks[0]["sample_rate"])
        self.assertEqual(tracks[1]["type"], "audio")
        self.assertEqual(tracks[1]["sample_rate"], 48000)
        self.assertEqual(tracks[1]["channels"], 2)
        self.assertIsNone(tracks[1]["width"])

    def test_extended_size_box_is_accepted(self):
        # Rewrite the first ftyp header as a 64-bit extended-size box:
        # the header grows from 8 to 16 bytes while the payload is kept.
        orig_size = struct.unpack(">I", self.data[:4])[0]
        payload = self.data[8:orig_size]
        new_size = 16 + len(payload)
        rebuilt = (struct.pack(">I", 1) + b"ftyp"
                   + struct.pack(">Q", new_size) + payload
                   + self.data[orig_size:])
        parsed = parse_mp4(_BytesFile(rebuilt), len(rebuilt))
        self.assertEqual(len(parsed["tracks"]), 2)

    def test_box_past_eof_is_rejected(self):
        # Claim the first box spans more bytes than the file contains.
        damaged = struct.pack(">I", len(self.data) + 500) + self.data[4:]
        with self.assertRaises(ContainerError):
            parse_mp4(_BytesFile(damaged), len(damaged))

    def test_unknown_signature_is_rejected(self):
        with self.assertRaises(ContainerError):
            parse_mp4(_BytesFile(b"RIFFxxxx" + self.data[8:]),
                      len(self.data))


class MatroskaParsingTests(unittest.TestCase):
    def setUp(self):
        self.data = build_matroska(
            1_000_000, 1000.0,
            [{"type": "video", "codec_id": "V_MPEG4/ISO/AVC",
              "width": 1280, "height": 720},
             {"type": "audio", "codec_id": "A_AAC",
              "sample_rate": 44100, "channels": 2}])
        self.parsed = parse_matroska(_BytesFile(self.data), len(self.data))

    def test_container_duration_and_tracks(self):
        self.assertEqual(self.parsed["container"], "matroska")
        self.assertEqual(self.parsed["duration_ms"], 1000)
        tracks = self.parsed["tracks"]
        self.assertEqual([t["track_id"] for t in tracks], [1, 2])
        self.assertEqual(tracks[0]["codec"], "V_MPEG4/ISO/AVC")
        self.assertEqual((tracks[0]["width"], tracks[0]["height"]),
                         (1280, 720))
        self.assertEqual(tracks[1]["sample_rate"], 44100)
        self.assertEqual(tracks[1]["channels"], 2)

    def test_truncated_vint_is_rejected(self):
        with self.assertRaises(ContainerError):
            parse_matroska(_BytesFile(self.data[:20]), 20)

    def test_element_range_past_eof_is_rejected(self):
        # Cut the file in half while keeping the EBML header intact.
        with self.assertRaises(ContainerError):
            parse_matroska(_BytesFile(self.data[:len(self.data) // 2]),
                            len(self.data) // 2)

    def test_missing_ebml_header_is_rejected(self):
        with self.assertRaises(ContainerError):
            parse_matroska(_BytesFile(b"\x00" * 32), 32)


class ComparisonTests(unittest.TestCase):
    def _probe(self, path: str, data: bytes):
        with open(path, "wb") as f:
            f.write(data)
        return probe_file(path)

    def test_order_independent_matching(self):
        raw_a = build_mp4(1000, 1000, [VIDEO_SPEC, AUDIO_SPEC])
        raw_b = build_mp4(1000, 1000, [AUDIO_SPEC, VIDEO_SPEC])
        tracks_a = build_tracks(parse_mp4(_BytesFile(raw_a), len(raw_a)), "a")
        tracks_b = build_tracks(parse_mp4(_BytesFile(raw_b), len(raw_b)), "b")
        pairs, missing, extra = match_tracks(tracks_a, tracks_b)
        self.assertEqual(len(pairs), 2)
        self.assertEqual(missing, [])
        self.assertEqual(extra, [])

    def test_missing_extra_and_param_diff(self):
        with tempfile.TemporaryDirectory() as tmp:
            ref = self._probe(
                os.path.join(tmp, "g.mp4"),
                build_mp4(1000, 1000, [VIDEO_SPEC, AUDIO_SPEC]))
            other = self._probe(
                os.path.join(tmp, "g.mkv"),
                build_matroska(1_000_000, 1000.0, [
                    {"type": "video", "codec_id": "V_MPEG4/ISO/AVC",
                     "width": 640, "height": 360}]))
            report = compare_results([ref, other], tolerance_ms=0)
            kinds = {m.kind for m in report.mismatches}
            # The 2-track MP4 is the reference: MKV lacks its audio track
            # and its video parameters disagree.
            self.assertIn(MISSING, kinds)
            self.assertIn(PARAM_DIFF, kinds)
            self.assertNotIn(EXTRA, kinds)

    def test_extra_track_when_reference_is_smaller(self):
        with tempfile.TemporaryDirectory() as tmp:
            # Equal track counts so the path-sorted first file becomes the
            # reference; its counterpart's non-matching track is then extra.
            ref = self._probe(
                os.path.join(tmp, "g.mp4"),
                build_mp4(1000, 1000, [VIDEO_SPEC, AUDIO_SPEC]))
            other = self._probe(
                os.path.join(tmp, "g.mkv"),
                build_matroska(1_000_000, 1000.0, [
                    {"type": "video", "codec_id": "V_MPEG4/ISO/AVC",
                     "width": 1920, "height": 1080},
                    {"type": "subtitle", "codec_id": "S_TEXT/UTF8"}]))
            kinds = {m.kind for m in
                     compare_results([ref, other], 0).mismatches}
            self.assertIn(EXTRA, kinds)
            self.assertIn(MISSING, kinds)

    def test_duration_tolerance_boundary(self):
        with tempfile.TemporaryDirectory() as tmp:
            # Same stem (g), different extensions: probe sniffs by content
            # signature, so both MP4 byte streams are accepted.
            path_a = os.path.join(tmp, "g.mp4")
            path_b = os.path.join(tmp, "g.mkv")
            with open(path_a, "wb") as f:
                f.write(build_mp4(1000, 1000, [VIDEO_SPEC]))
            with open(path_b, "wb") as f:
                f.write(build_mp4(1000, 1040, [VIDEO_SPEC]))
            exact = compare_results(
                probe_many([path_a, path_b]), tolerance_ms=40)
            self.assertEqual(
                [m for m in exact.mismatches if m.kind == DURATION_DIFF], [])
            self.assertEqual(exact.max_duration_delta_ms, 40)
            strict = compare_results(
                probe_many([path_a, path_b]), tolerance_ms=39)
            self.assertTrue(
                any(m.kind == DURATION_DIFF for m in strict.mismatches))


class FailureRecoveryAndStorageTests(unittest.TestCase):
    def test_batch_continues_after_corrupt_file(self):
        with tempfile.TemporaryDirectory() as tmp:
            good = os.path.join(tmp, "good.mp4")
            bad = os.path.join(tmp, "bad.mkv")
            unknown = os.path.join(tmp, "strange.bin")
            with open(good, "wb") as f:
                f.write(build_mp4(1000, 1000, [VIDEO_SPEC]))
            with open(bad, "wb") as f:
                f.write(b"\x1a\x45\xdf\xa3" + b"\x00" * 16)
            with open(unknown, "wb") as f:
                f.write(b"NOT A CONTAINER" * 4)
            results = probe_many([good, bad, unknown])
            self.assertEqual([r.ok for r in results], [True, False, False])
            self.assertTrue(all(r.error for r in results if not r.ok))

    def test_scan_persisted_including_failures(self):
        with tempfile.TemporaryDirectory() as tmp:
            good = os.path.join(tmp, "good.mp4")
            bad = os.path.join(tmp, "bad.mkv")
            with open(good, "wb") as f:
                f.write(build_mp4(1000, 1000, [VIDEO_SPEC]))
            with open(bad, "wb") as f:
                f.write(b"\x1a\x45\xdf\xa3" + b"\x00" * 8)
            results = probe_many([good, bad])
            conn = connect(os.path.join(tmp, "db.sqlite"))
            record = save_scan(conn, started_at="t", reference=None,
                               tolerance_ms=0, results=results,
                               max_duration_delta_ms=None,
                               mismatch_count=0)
            self.assertEqual(record.files_total, 2)
            self.assertEqual(record.files_failed, 1)
            rows = conn.execute(
                "SELECT ok, error FROM files").fetchall()
            self.assertEqual(len(rows), 2)
            self.assertTrue(any(not ok and err for ok, err in rows))
            self.assertEqual(len(conn.execute("SELECT * FROM tracks").fetchall()),
                             1)

    def test_storage_error_on_unwritable_db(self):
        with tempfile.TemporaryDirectory() as tmp:
            target = os.path.join(tmp, "missing-dir", "db.sqlite")
            with self.assertRaises(StorageError):
                connect(target)

    def test_json_write_refuses_directory_and_input_overwrite(self):
        with tempfile.TemporaryDirectory() as tmp:
            source = os.path.join(tmp, "good.mp4")
            with open(source, "wb") as f:
                f.write(build_mp4(1000, 1000, [VIDEO_SPEC]))
            with self.assertRaises(ReportError):
                write_json(tmp, {}, [source])
            with self.assertRaises(ReportError):
                write_json(source, {}, [source])

    def test_json_report_shape(self):
        with tempfile.TemporaryDirectory() as tmp:
            source = os.path.join(tmp, "good.mp4")
            with open(source, "wb") as f:
                f.write(build_mp4(1000, 1000, [VIDEO_SPEC]))
            results = probe_many([source])
            comparison = compare_results(results, 10)
            from mediacheck.report import build_report
            report = build_report(results, comparison, 10)
            out = os.path.join(tmp, "r.json")
            write_json(out, report, [source])
            with open(out, encoding="utf-8") as handle:
                loaded = json.load(handle)
            for key in ("files_total", "files_ok", "files_failed", "tracks",
                        "max_duration_delta_ms", "codec_summary",
                        "mismatches"):
                self.assertIn(key, loaded)


class CodecNormalizationTests(unittest.TestCase):
    def test_cross_container_identities(self):
        self.assertEqual(normalize_codec("avc1", "video"),
                         normalize_codec("V_MPEG4/ISO/AVC", "video"))
        self.assertEqual(normalize_codec("mp4a", "audio"),
                         normalize_codec("A_AAC", "audio"))
        self.assertIsNone(normalize_codec(None, "video"))


if __name__ == "__main__":
    unittest.main()
