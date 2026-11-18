"""Boundary coverage for the container probe."""

from __future__ import annotations

import struct
import sys
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "src"))
sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "scripts"))

import make_samples as samples  # noqa: E402
from mediacheck.models import UNKNOWN  # noqa: E402
from mediacheck.probe import ProbeError, _iter_boxes, probe_bytes  # noqa: E402

EBML_HEADER = b"\x1a\x45\xdf\xa3"


def ebml(doc_type: str = "matroska", *, tracks=None, duration: float | None = 5000.0,
         scale: int = 1_000_000) -> bytes:
    return samples.ebml_file(
        doc_type, scale, duration,
        tracks if tracks is not None else [
            dict(number=1, type=1, codec="V_MPEG4/ISO/AVC", codec_delay_ns=0,
                 duration_ticks=5000.0, width=640, height=360),
            dict(number=2, type=2, codec="A_AAC", codec_delay_ns=0,
                 duration_ticks=5000.0, sample_rate=48000, channels=2),
        ],
    )


class SniffingTests(unittest.TestCase):
    def test_extension_is_ignored(self) -> None:
        result = probe_bytes(ebml(), "/tmp/actually-a.mkv")
        self.assertEqual(result.container, "ebml:matroska")

    def test_bmff_sniffed_from_ftyp(self) -> None:
        data = samples.bmff_file(tracks=[
            dict(track_id=1, handler="vide", media_timescale=90000,
                 media_duration=450000, width=640, height=360),
        ])
        result = probe_bytes(data, "x.mp4")
        self.assertEqual(result.container, "bmff:isom")
        self.assertEqual(result.tracks[0].codec, "h264")
        self.assertEqual(result.tracks[0].params, {"width": 640, "height": 360})

    def test_empty_file(self) -> None:
        result = probe_bytes(b"", "empty.bin")
        self.assertEqual(result.error, "empty file")
        self.assertEqual(result.error_offset, 0)

    def test_unknown_signature(self) -> None:
        result = probe_bytes(b"RIFFdata", "unknown.avi")
        self.assertEqual(result.error, "unrecognised container signature")
        self.assertEqual(result.error_offset, 0)

    def test_truncated_ebml_header(self) -> None:
        result = probe_bytes(EBML_HEADER + b"\x84", "cut.mkv")
        self.assertIsNotNone(result.error)
        # The offset points at the element that overran the buffer.
        self.assertEqual(result.error_offset, 0)

    def test_truncated_payload_reports_inner_offset(self) -> None:
        data = ebml()
        result = probe_bytes(data[:-30], "cut2.mkv")
        self.assertIsNotNone(result.error)
        self.assertIsInstance(result.error_offset, int)

    def test_missing_segment(self) -> None:
        data = samples.elem(0x1A45DFA3, samples.elem(0x4282, b"matroska"))
        result = probe_bytes(data, "noseg.mkv")
        self.assertEqual(result.error, "missing EBML segment")


class EbmlValueTests(unittest.TestCase):
    def test_track_metadata_and_order(self) -> None:
        result = probe_bytes(ebml(), "ok.mkv")
        self.assertIsNone(result.error)
        self.assertEqual(result.timescale, 1000)
        self.assertEqual(result.duration_ms, 5000)
        self.assertEqual([t.kind for t in result.tracks], ["audio", "video"])
        self.assertEqual(result.tracks[0].codec, "aac")
        self.assertEqual(result.tracks[0].params["channels"], 2)

    def test_non_zero_start_time(self) -> None:
        data = ebml(tracks=[dict(number=1, type=2, codec="A_AAC",
                                 codec_delay_ns=40_000_000,
                                 duration_ticks=1000.0, sample_rate=48000,
                                 channels=2)])
        result = probe_bytes(data, "delay.mka")
        self.assertEqual(result.tracks[0].start_ms, 40)

    def test_missing_duration_is_unknown_not_zero(self) -> None:
        data = ebml(duration=None, tracks=[dict(
            number=1, type=2, codec="A_AAC", codec_delay_ns=0,
            sample_rate=48000, channels=2)])
        result = probe_bytes(data, "nodur.mkv")
        self.assertIsNone(result.duration_ms)
        self.assertIsNone(result.tracks[0].duration_ms)

    def test_missing_timescale_leaves_ms_unknown(self) -> None:
        # TimecodeScale absent: duration in ticks cannot be converted.
        header = samples.elem(0x1A45DFA3, samples.elem(0x4282, b"matroska"))
        info = samples.elem(0x1549A966, samples.elem(0x4489, samples.f64(5000.0)))
        tracks = samples.elem(0x1654AE6B, samples.elem(0xAE, (
            samples.elem(0xD7, samples.uint(1)) + samples.elem(0x83, samples.uint(2))
        )))
        result = probe_bytes(header + samples.elem(0x18538067, info + tracks), "x")
        self.assertIsNone(result.duration_ms)
        self.assertIsNone(result.tracks[0].timescale)

    def test_duplicate_track_numbers_flagged(self) -> None:
        data = ebml(tracks=[
            dict(number=1, type=1, codec="V_MPEG4/ISO/AVC", codec_delay_ns=0,
                 duration_ticks=5000.0, width=640, height=360),
            dict(number=1, type=2, codec="A_AAC", codec_delay_ns=0,
                 duration_ticks=5000.0, sample_rate=48000, channels=2),
        ])
        result = probe_bytes(data, "dup.mkv")
        problems = " ".join(result.tracks[0].problems + result.tracks[1].problems)
        self.assertIn("duplicate-track-number:1", problems)

    def test_track_duration_overflow_flagged(self) -> None:
        data = ebml(duration=1000.0, tracks=[dict(
            number=1, type=1, codec="V_MPEG4/ISO/AVC", codec_delay_ns=0,
            duration_ticks=5000.0, width=640, height=360)])
        result = probe_bytes(data, "over.mkv")
        self.assertIn("track-duration-overflow:5000>1000", result.tracks[0].problems)

    def test_unknown_track_type_is_not_invented(self) -> None:
        data = ebml(tracks=[dict(number=1, type=99, codec="X_UNKNOWN",
                                 codec_delay_ns=0, duration_ticks=10.0)])
        result = probe_bytes(data, "kind.mkv")
        self.assertEqual(result.tracks[0].kind, UNKNOWN)

    def test_oversized_uint_rejected(self) -> None:
        body = samples.elem(0xD7, b"\x01" * 9) + samples.elem(0x83, b"\x02")
        entry = samples.elem(0xAE, body)
        data = (samples.elem(0x1A45DFA3, samples.elem(0x4282, b"matroska")) +
                samples.elem(0x18538067, samples.elem(0x1654AE6B, entry)))
        result = probe_bytes(data, "big.mkv")
        self.assertIn("max 8", result.error or "")

    def test_large_track_number_boundary(self) -> None:
        data = ebml(tracks=[dict(number=2**32 - 1, type=1,
                                 codec="V_MPEG4/ISO/AVC", codec_delay_ns=0,
                                 duration_ticks=5000.0, width=1, height=1)])
        result = probe_bytes(data, "max.mkv")
        self.assertEqual(result.tracks[0].track_id, 2**32 - 1)


class BmffTests(unittest.TestCase):
    def make(self, **overrides) -> bytes:
        spec = dict(track_id=1, handler="vide", media_timescale=90000,
                    media_duration=450000, width=1280, height=720)
        spec.update(overrides)
        return samples.bmff_file(movie_timescale=1000, movie_duration=5000,
                                 tracks=[spec])

    def test_audio_params(self) -> None:
        data = samples.bmff_file(tracks=[
            dict(track_id=2, handler="soun", media_timescale=48000,
                 media_duration=240000, channels=2, sample_rate=48000),
        ])
        result = probe_bytes(data, "a.mp4")
        track = result.tracks[0]
        self.assertEqual(track.kind, "audio")
        self.assertEqual(track.params["channels"], 2)
        self.assertEqual(track.params["sample_rate"], 48000)

    def test_missing_sample_entry_flags_problem(self) -> None:
        data = samples.bmff_file(tracks=[
            dict(track_id=1, handler="vide", media_timescale=90000,
                 media_duration=450000, width=64, height=64),
        ])
        corrupted = bytearray(data)
        pos = corrupted.index(b"stsd")
        corrupted[pos + 8:pos + 12] = struct.pack(">I", 0)  # entry_count = 0
        result = probe_bytes(bytes(corrupted), "noentry.mp4")
        self.assertIsNone(result.error)
        self.assertIsNone(result.tracks[0].codec)
        self.assertIn("stsd declares no sample entries", result.tracks[0].problems)

    def test_unknown_sample_entry_keeps_codec_id(self) -> None:
        data = samples.bmff_file(tracks=[
            dict(track_id=1, handler="vide", media_timescale=90000,
                 media_duration=450000, width=64, height=64),
        ])
        result = probe_bytes(data.replace(b"avc1", b"zzzz"), "z.mp4")
        self.assertIsNone(result.error)
        self.assertEqual(result.tracks[0].codec, "zzzz")

    def test_box_extends_past_file(self) -> None:
        data = self.make()
        moov_start = data.index(b"moov") - 4
        corrupted = bytearray(data)
        corrupted[moov_start:moov_start + 4] = struct.pack(">I", 0x7FFFFFFF)
        result = probe_bytes(bytes(corrupted), "bad.mp4")
        self.assertIn("moov", result.error or "")
        self.assertEqual(result.error_offset, moov_start)

    def test_box_size_smaller_than_header(self) -> None:
        data = bytearray(self.make())
        data[0:4] = struct.pack(">I", 3)
        result = probe_bytes(bytes(data), "tiny.mp4")
        self.assertIn("invalid size", result.error or "")

    def test_missing_moov(self) -> None:
        ftyp = samples.box(b"ftyp", b"isom\x00\x00\x02\x00isom")
        result = probe_bytes(ftyp + samples.box(b"free", b"\x00" * 16), "nomoov.mp4")
        self.assertEqual(result.error, "missing moov box")

    def test_edit_list_start_time(self) -> None:
        data = samples.bmff_file(tracks=[
            dict(track_id=1, handler="vide", media_timescale=90000,
                 media_duration=450000, width=64, height=64,
                 edit_start_media_time=90000),
        ])
        result = probe_bytes(data, "edit.mp4")
        self.assertEqual(result.tracks[0].start_ms, 1000)

    def test_tkhd_version_unsupported(self) -> None:
        data = bytearray(self.make())
        pos = data.index(b"tkhd")
        data[pos + 4] = 9  # version byte right after box type
        result = probe_bytes(bytes(data), "v9.mp4")
        self.assertIn("unsupported tkhd version 9", result.error or "")

    def test_unknown_duration_is_unknown(self) -> None:
        data = bytearray(self.make())
        pos = data.index(b"mvhd") + 4 + 16  # version/flags + creation/modification
        data[pos:pos + 4] = b"\xff\xff\xff\xff"
        result = probe_bytes(bytes(data), "nodur.mp4")
        self.assertIsNone(result.duration_ms)


class ChapterAndOffsetTests(unittest.TestCase):
    def test_unknown_size_element_consumes_rest(self) -> None:
        header = samples.elem(0x1A45DFA3, samples.elem(0x4282, b"matroska"))
        # Segment with "unknown size" (all-ones vint) containing no children.
        segment = b"\x18\x53\x80\x67\xff"
        result = probe_bytes(header + segment, "live.mkv")
        self.assertIsNone(result.error)
        self.assertEqual(result.tracks, [])

    def test_iter_boxes_zero_size_means_rest_of_file(self) -> None:
        body = b"mdat" + b"\x00" * 8
        data = struct.pack(">I", 0) + body
        boxes = list(_iter_boxes(data, 0, len(data)))
        self.assertEqual(len(boxes), 1)
        self.assertEqual(boxes[0].data_end, len(data))

    def test_probe_error_carries_offset(self) -> None:
        with self.assertRaises(ProbeError) as ctx:
            data = b"\x18\x53\x80\x67\x05\x00"
            list(_iter_boxes(data, 0, len(data)))
        self.assertEqual(ctx.exception.offset, 0)


if __name__ == "__main__":
    unittest.main()
