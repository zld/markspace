"""Tests for container sniffing and bounded byte parsing."""

from __future__ import annotations

import sys
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / "src"))
sys.path.insert(0, str(ROOT / "scripts"))

import make_samples  # noqa: E402
from mediacheck.binary import BinaryError, Reader, ticks_to_ms  # noqa: E402
from mediacheck.ebml import (  # noqa: E402
    encode_ebml_size,
    read_ebml_id,
    read_ebml_size,
)
from mediacheck.probe import MAX_FILE_BYTES, probe_path, sniff  # noqa: E402


class FixtureCase(unittest.TestCase):
    """Writes the sample set once per class into a temporary directory."""

    @classmethod
    def setUpClass(cls) -> None:
        cls._tmp = tempfile.TemporaryDirectory()
        cls.fixtures = Path(cls._tmp.name)
        make_samples.main(["--out", str(cls.fixtures)])

    @classmethod
    def tearDownClass(cls) -> None:
        cls._tmp.cleanup()

    def fixture(self, name: str) -> str:
        return str(self.fixtures / name)


class ReaderBoundsTest(unittest.TestCase):
    def test_read_past_limit_raises_with_offset(self) -> None:
        reader = Reader(b"\x00\x01\x02\x03", 0, 4)
        reader.read_uint16()
        with self.assertRaises(BinaryError) as ctx:
            reader.read_uint32()
        self.assertEqual(ctx.exception.offset, 2)

    def test_subreader_cannot_escape_parent(self) -> None:
        reader = Reader(b"\x00" * 8, 0, 8)
        with self.assertRaises(BinaryError):
            reader.subreader(9)

    def test_negative_length_rejected(self) -> None:
        reader = Reader(b"\x00" * 4)
        with self.assertRaises(BinaryError):
            reader.skip(-1)

    def test_invalid_bounds_rejected(self) -> None:
        with self.assertRaises(BinaryError):
            Reader(b"\x00" * 4, 2, 1)

    def test_ticks_to_ms(self) -> None:
        self.assertAlmostEqual(ticks_to_ms(30000, 30000), 1000.0)
        self.assertAlmostEqual(ticks_to_ms(1, 1000), 1.0)
        with self.assertRaises(ValueError):
            ticks_to_ms(1, 0)


class EbmlPrimitiveTest(unittest.TestCase):
    def test_size_round_trip(self) -> None:
        for value in (0, 1, 126, 127, 128, 16382, 1 << 20):
            encoded = encode_ebml_size(value)
            got, length = read_ebml_size(Reader(encoded))
            self.assertEqual(got, value)
            self.assertEqual(length, len(encoded))

    def test_unknown_size_marker(self) -> None:
        value, length = read_ebml_size(Reader(b"\xff\xff\xff\xff"))
        self.assertIsNone(value)
        self.assertEqual(length, 1)
        value, length = read_ebml_size(Reader(b"\x01" + b"\xff" * 7))
        self.assertIsNone(value)
        self.assertEqual(length, 8)

    def test_zero_first_byte_rejected(self) -> None:
        with self.assertRaises(BinaryError):
            read_ebml_size(Reader(b"\x00\x01"))

    def test_id_keeps_marker_bit(self) -> None:
        value, length = read_ebml_id(Reader(b"\x1a\x45\xdf\xa3"))
        self.assertEqual(value, 0x1A45DFA3)
        self.assertEqual(length, 4)

    def test_truncated_id(self) -> None:
        with self.assertRaises(BinaryError):
            read_ebml_id(Reader(b"\x1a\x45"))


class SniffTest(unittest.TestCase):
    def test_detects_iso_bmff(self) -> None:
        self.assertEqual(sniff(b"\x00\x00\x00\x20ftypisom" + b"\x00" * 4),
                         "mp4")

    def test_detects_matroska(self) -> None:
        self.assertEqual(sniff(b"\x1a\x45\xdf\xa3\x01\x00"), "matroska")

    def test_rejects_text(self) -> None:
        self.assertIsNone(sniff(b"this is not media"))

    def test_rejects_short_input(self) -> None:
        self.assertIsNone(sniff(b"\x00\x00\x00"))
        self.assertIsNone(sniff(b""))

    def test_rejects_degenerate_box_size(self) -> None:
        self.assertIsNone(sniff(b"\x00\x00\x00\x04ftyp"))


class ProbeFixtureTest(FixtureCase):
    def test_mp4_tracks_and_duration(self) -> None:
        media = probe_path(self.fixture("base.mp4"))
        self.assertEqual(media.container, "mp4")
        self.assertFalse(media.fatal)
        self.assertEqual(media.track_count, 2)
        self.assertAlmostEqual(media.duration_ms, 1000.0, places=3)
        video, audio = media.tracks
        self.assertEqual(video.kind, "video")
        self.assertEqual(video.codec, "avc1")
        self.assertEqual(video.time_base, "1/30000")
        self.assertEqual(video.codec_detail["width"], 320)
        self.assertEqual(audio.kind, "audio")
        self.assertEqual(audio.codec, "mp4a")
        self.assertEqual(audio.time_base, "1/48000")
        self.assertEqual(audio.codec_detail["channels"], 2)
        self.assertEqual(len(media.sha256 or ""), 64)

    def test_matroska_tracks_and_duration(self) -> None:
        media = probe_path(self.fixture("candidate.mkv"))
        self.assertEqual(media.container, "matroska")
        self.assertFalse(media.fatal)
        self.assertEqual(media.track_count, 2)
        self.assertAlmostEqual(media.duration_ms, 1012.0, places=3)
        video, audio = media.tracks
        self.assertEqual(video.codec, "V_MPEG4/ISO/AVC")
        self.assertEqual(video.codec_detail["width"], 320)
        self.assertEqual(audio.codec, "A_OPUS")
        self.assertEqual(audio.codec_detail["channels"], 2)

    def test_extension_does_not_decide_container(self) -> None:
        disguised = self.fixtures / "actually_matroska.mp4"
        disguised.write_bytes(Path(self.fixture("candidate.mkv")).read_bytes())
        media = probe_path(disguised)
        self.assertEqual(media.container, "matroska")
        self.assertEqual(media.track_count, 2)

    def test_empty_file_reports_zero_offset(self) -> None:
        media = probe_path(self.fixture("empty.mp4"))
        self.assertTrue(media.fatal)
        self.assertEqual(media.issues[0].code, "io.empty_file")
        self.assertEqual(media.issues[0].offset, 0)

    def test_unknown_container_reports_signature(self) -> None:
        media = probe_path(self.fixture("unknown.bin"))
        self.assertTrue(media.fatal)
        self.assertEqual(media.issues[0].code, "sniff.unknown_container")
        self.assertIn("74 68 69 73", media.issues[0].message)

    def test_truncated_file_reports_offset_and_reason(self) -> None:
        media = probe_path(self.fixture("truncated.mp4"))
        self.assertTrue(media.fatal)
        codes = {i.code for i in media.issues}
        self.assertIn("mp4.box_overrun", codes)
        overrun = next(i for i in media.issues if i.code == "mp4.box_overrun")
        self.assertEqual(overrun.offset, 104)
        self.assertIn("exceeds limit", overrun.message)

    def test_oversized_declared_length_is_caught(self) -> None:
        media = probe_path(self.fixture("oversize.mp4"))
        self.assertTrue(media.fatal)
        self.assertTrue(any(i.code == "mp4.box_overrun"
                            for i in media.issues))

    def test_missing_file(self) -> None:
        media = probe_path(self.fixtures / "does_not_exist.mp4")
        self.assertTrue(media.fatal)
        self.assertEqual(media.issues[0].code, "io.not_found")

    def test_mp4_without_moov_is_fatal(self) -> None:
        ftyp = make_samples.box(b"ftyp", b"isom" + b"\x00" * 8)
        path = self.fixtures / "no_moov.mp4"
        path.write_bytes(ftyp + make_samples.mdat(b"\x00" * 16))
        media = probe_path(path)
        self.assertTrue(media.fatal)
        self.assertIn("mp4.missing_moov",
                      {i.code for i in media.issues})

    def test_truncated_matroska_header(self) -> None:
        path = self.fixtures / "truncated.mkv"
        path.write_bytes(b"\x1a\x45\xdf\xa3\x01\x00")
        media = probe_path(path)
        self.assertTrue(media.fatal)
        self.assertIn("mkv.truncated_element_header",
                      {i.code for i in media.issues})

    def test_matroska_element_overrun_is_reported(self) -> None:
        header = make_samples.ebml_header()
        # Segment header declaring 65536 bytes inside a file of ~30 bytes,
        # with a 4-byte size vint so the declared length is unambiguous.
        path = self.fixtures / "overrun.mkv"
        path.write_bytes(header + b"\x18\x53\x80\x67" + b"\x11\x00\x00\x00")
        media = probe_path(path)
        self.assertTrue(media.fatal)
        self.assertIn("mkv.element_overrun",
                      {i.code for i in media.issues})

    def test_unknown_size_segment_and_cluster_are_bounded(self) -> None:
        media = probe_path(self.fixture("unknown_size.mkv"))
        self.assertFalse(media.fatal)
        self.assertAlmostEqual(media.duration_ms, 500.0, places=3)
        self.assertEqual(media.track_count, 2)
        codes = {i.code for i in media.issues}
        self.assertIn("mkv.unknown_size_segment", codes)
        self.assertIn("mkv.unknown_size_cluster", codes)
        for issue in media.issues:
            if issue.code == "mkv.unknown_size_cluster":
                self.assertIn("boundary inferred", issue.message)

    def test_unreadable_moov_is_fatal(self) -> None:
        # A moov whose child declares a size larger than moov itself yields
        # no mvhd and no tracks; that must not read as a clean empty file.
        inner = b"\x00\x00\x10\x00" + b"trak" + b"\x00" * 16
        moov = (len(inner) + 8).to_bytes(4, "big") + b"moov" + inner
        ftyp = make_samples.box(b"ftyp", b"isom" + b"\x00" * 8)
        path = self.fixtures / "bad_moov.mp4"
        path.write_bytes(ftyp + moov)
        media = probe_path(path)
        self.assertTrue(media.fatal)
        self.assertEqual(media.tracks, [])
        codes = {i.code for i in media.issues}
        self.assertTrue({"mp4.box_overrun", "mp4.moov_unreadable"} & codes)

    def test_truncated_element_inside_segment_is_fatal(self) -> None:
        # A Tracks element declares more bytes than the segment provides.
        segment = make_samples.el(
            b"\x18\x53\x80\x67",
            b"\x16\x54\xAE\x6B" + b"\x11\x00\x10\x00" + b"\xAE" * 4)
        path = self.fixtures / "truncated_segment.mkv"
        path.write_bytes(make_samples.ebml_header() + segment)
        media = probe_path(path)
        self.assertTrue(media.fatal)
        self.assertIn("mkv.element_overrun",
                      {i.code for i in media.issues})

    def test_segment_without_tracks_is_not_fatal(self) -> None:
        path = self.fixtures / "info_only.mkv"
        segment = make_samples.el(
            b"\x18\x53\x80\x67",
            make_samples.el(b"\x15\x49\xA9\x66",
                            make_samples.el_float(b"\x44\x89", 500.0)))
        path.write_bytes(make_samples.ebml_header() + segment)
        media = probe_path(path)
        self.assertFalse(media.fatal)
        self.assertEqual(media.tracks, [])
        self.assertAlmostEqual(media.duration_ms, 500.0, places=3)

    def test_size_limit_is_enforced(self) -> None:
        path = self.fixtures / "sparse.mp4"
        with path.open("wb") as handle:
            handle.seek(MAX_FILE_BYTES + 1)
            handle.write(b"\x00")
        media = probe_path(path)
        self.assertTrue(media.fatal)
        self.assertEqual(media.issues[0].code, "io.too_large")
        path.unlink()


class BoundedBoxTest(unittest.TestCase):
    def test_child_box_larger_than_parent_stops_parsing(self) -> None:
        from mediacheck.bmff import iter_boxes
        from mediacheck.model import MediaFile

        # moov declares 32 bytes but its single child claims 4096.
        payload = b"\x00\x00\x10\x00" + b"trak" + b"\x00" * 16
        data = (len(payload) + 8).to_bytes(4, "big") + b"moov" + payload
        media = MediaFile("mem", len(data), None)
        boxes = list(iter_boxes(Reader(data, 0, len(data)), media))
        self.assertEqual([b.name for b in boxes], ["moov"])
        inner = list(iter_boxes(boxes[0].body, media, 1))
        self.assertEqual(inner, [])
        self.assertTrue(any(i.code == "mp4.box_overrun"
                            for i in media.issues))


if __name__ == "__main__":
    unittest.main()
