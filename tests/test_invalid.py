"""Damaged-input tests: every rejection must name the box and offset."""

from __future__ import annotations

import json
import struct
import tempfile
import unittest
from pathlib import Path

from mediidx import fixture
from mediidx.box_reader import BoxReader
from mediidx.cli import main
from mediidx.errors import MediidxError, StructureError
from mediidx.rebuild import rebuild
from mediidx.stbl import find_mdat, parse_track
from mediidx.validator import validate_bytes


def _mutate_box(data: bytearray, box_type: bytes, replacement: bytes) -> bytearray:
    """Replace a top-level box's payload bytes in place (same total length)."""

    roots = BoxReader(memoryview(bytes(data))).parse_top_level()
    box = next((item for item in roots if item.type == box_type), None)
    if box is None:
        raise AssertionError(f"fixture has no {box_type!r} box")
    payload = box.payload_size
    if len(replacement) > payload:
        raise AssertionError("replacement does not fit")
    data[box.start : box.end] = (
        struct.pack(">I4s", box.size, box.type)
        + replacement
        + b"\x00" * (payload - len(replacement))
    )
    return data


def _nested_box(data: bytearray, path: list[bytes]) -> tuple[int, int]:
    view = memoryview(bytes(data))
    roots = BoxReader(view).parse_top_level()
    box = next(item for item in roots if item.type == path[0])
    for box_type in path[1:]:
        box = next(child for child in box.children if child.type == box_type)
    return box.start, box.end


class SupportedParsingTests(unittest.TestCase):
    def test_empty_input_rejected(self):
        # An empty stream parses to zero boxes; the movie layer must reject it.
        self.assertEqual(BoxReader(memoryview(b"")).parse_top_level(), [])
        with self.assertRaises(StructureError):
            validate_bytes(b"")

    def test_truncated_header_rejected(self):
        with self.assertRaises(StructureError):
            BoxReader(memoryview(b"\x00\x00\x00\x20free")).parse_top_level()

    def test_box_size_beyond_file_rejected(self):
        with self.assertRaises(StructureError):
            BoxReader(memoryview(b"\x00\x00\x00\x40free" + b"x" * 8)).parse_top_level()

    def test_size_smaller_than_header_rejected(self):
        with self.assertRaises(StructureError):
            BoxReader(memoryview(struct.pack(">I4s", 4, b"free"))).parse_top_level()

    def test_nesting_limit_rejected(self):
        payload = b""
        for _ in range(20):
            payload = fixture.box(b"moov", payload)
        with self.assertRaises(StructureError):
            BoxReader(memoryview(payload)).parse_top_level()


class DamagedInputTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(prefix="mediidx-bad-")
        self.dir = Path(self.tmp.name)
        self.baseline = fixture.build_default()

    def tearDown(self):
        self.tmp.cleanup()

    def _path(self, name: str, data: bytes) -> str:
        path = self.dir / name
        path.write_bytes(data)
        return str(path)

    def _assert_rejected(self, data: bytes, needle: str) -> None:
        with self.assertRaises(MediidxError) as ctx:
            validate_bytes(data)
        message = ctx.exception.describe()
        self.assertIn(needle, message)
        self.assertIsNotNone(ctx.exception.box_type)
        self.assertIsNotNone(ctx.exception.offset)

    def test_chunk_offset_outside_mdat(self):
        data = bytearray(self.baseline)
        start, end = _nested_box(data, [b"moov", b"trak", b"mdia", b"minf", b"stbl", b"stco"])
        # First offset entry sits after version/flags + entry count.
        entry = start + 8 + 4 + 4
        data[entry : entry + 4] = struct.pack(">I", 0x00100000)
        self._assert_rejected(bytes(data), "outside the mdat")

    def test_sample_count_mismatch(self):
        data = bytearray(self.baseline)
        start, _end = _nested_box(data, [b"moov", b"trak", b"mdia", b"minf", b"stbl", b"stsz"])
        # stsz: version/flags, uniform size, sample count.
        count_at = start + 8 + 4 + 4
        data[count_at : count_at + 4] = struct.pack(">I", 7)
        # The extra declared size needs bytes; the parser reports the shortfall.
        self._assert_rejected(bytes(data), "declares 7 sample sizes")

    def test_stts_sample_count_mismatch(self):
        # Shrink the final stts run by one so the table covers 5 of the 6
        # samples that stsz describes.
        data = bytearray(fixture.build_default())
        start, _end = _nested_box(
            data, [b"moov", b"trak", b"mdia", b"minf", b"stbl", b"stts"]
        )
        count_at = start + 8 + 4
        entries = struct.unpack_from(">I", data, count_at)[0]
        last_entry = count_at + 4 + (entries - 1) * 8
        data[last_entry : last_entry + 4] = struct.pack(">I", 0)
        self._assert_rejected(bytes(data), "stts covers")

    def test_stsc_chunk_count_mismatch(self):
        data = bytearray(self.baseline)
        start, _end = _nested_box(data, [b"moov", b"trak", b"mdia", b"minf", b"stbl", b"stsc"])
        # Zero samples per chunk is invalid even before counts are compared.
        first_entry = start + 8 + 4 + 4
        data[first_entry + 4 : first_entry + 8] = struct.pack(">I", 0)
        self._assert_rejected(bytes(data), "declares zero samples")

    def test_duplicate_critical_table(self):
        duplicate = fixture.full_box(b"stts", 0, 0, struct.pack(">III", 1, 6, 1))
        data = fixture.build_default(extra_stbl_boxes=(duplicate,))
        self._assert_rejected(data, "duplicate stts")

    def test_encrypted_sample_box_rejected(self):
        senc = fixture.box(b"senc", b"\x00" * 8)
        data = fixture.build_default(extra_stbl_boxes=(senc,))
        self._assert_rejected(data, "unsupported encrypted")

    def test_multiple_top_level_sidx_rejected(self):
        data = bytearray(self.baseline)
        roots = BoxReader(memoryview(bytes(data))).parse_top_level()
        sidx_box = next(box for box in roots if box.type == b"sidx")
        # Appending after the last box keeps every existing offset valid while
        # still presenting a second top-level sidx.
        data += bytes(data[sidx_box.start : sidx_box.end])
        self._assert_rejected(bytes(data), "at most one top-level sidx")

    def test_nested_sidx_reference_rejected(self):
        base = fixture.build_default()
        view = memoryview(base)
        roots = BoxReader(view).parse_top_level()
        mdat = find_mdat(view, roots)
        track = parse_track(view, roots, mdat)
        # Rebuild a valid file, then flip the first reference's type bit.
        with tempfile.TemporaryDirectory() as directory:
            source = Path(directory) / "a.mp4"
            output = Path(directory) / "b.mp4"
            source.write_bytes(base)
            rebuild(str(source), str(output), target_duration=2)
            good = bytearray(output.read_bytes())
        roots = BoxReader(memoryview(bytes(good))).parse_top_level()
        sidx_box = next(box for box in roots if box.type == b"sidx")
        first_reference = sidx_box.payload_start + 12 + 8 + 2 + 2
        word = struct.unpack_from(">I", good, first_reference)[0]
        good[first_reference : first_reference + 4] = struct.pack(
            ">I", word | (1 << 31)
        )
        report = validate_bytes(bytes(good))
        self.assertFalse(report.ok)
        self.assertTrue(any("nested index" in error for error in report.errors))
        self.assertEqual(track.sample_count, 6)

    def test_truncated_trailing_sidx_is_regenerated(self):
        base = fixture.build_default(include_sidx=False)
        damaged = base + struct.pack(">I4s", 500, b"sidx") + b"\x00" * 30
        source = self._path("truncated.mp4", damaged)
        output = self.dir / "truncated-out.mp4"
        result = rebuild(source, str(output), target_duration=2, timescale=1)
        self.assertEqual(result.boundaries, [[0, 2], [2, 4], [4, 6]])
        self.assertEqual(result.reference_count, 3)
        self.assertTrue(any("truncated" in note for note in result.warnings))
        self.assertTrue(validate_bytes(output.read_bytes()).ok)

    def test_multi_sample_chunks_stay_whole(self):
        data = fixture.build_default(samples_per_chunk=(2, 2, 2))
        report = validate_bytes(data)
        self.assertTrue(report.ok)
        source = self._path("multi.mp4", data)
        output = self.dir / "multi-out.mp4"
        result = rebuild(source, str(output), target_duration=3)
        # Three chunks of two samples each at target 3: every chunk is a
        # complete reference and no chunk is cut.
        self.assertEqual(result.boundaries, [[0, 4], [4, 6]])
        self.assertTrue(validate_bytes(output.read_bytes()).ok)

    def test_zero_timescale_rejected(self):
        data = bytearray(self.baseline)
        start, _end = _nested_box(data, [b"moov", b"trak", b"mdia"])
        # mdhd payload: version/flags (4), creation (4), modification (4), timescale.
        mdhd_start, _ = _nested_box(data, [b"moov", b"trak", b"mdia", b"mdhd"])
        timescale_at = mdhd_start + 8 + 4 + 4 + 4
        data[timescale_at : timescale_at + 4] = struct.pack(">I", 0)
        self._assert_rejected(bytes(data), "timescale is zero")


class CliExitCodeTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(prefix="mediidx-cli-")
        self.dir = Path(self.tmp.name)

    def tearDown(self):
        self.tmp.cleanup()

    def test_fixture_rebuild_validate_success(self):
        sample = str(self.dir / "sample.mp4")
        rebuilt = str(self.dir / "rebuilt.mp4")
        self.assertEqual(main(["fixture", "--out", sample]), 0)
        self.assertEqual(
            main(["rebuild", sample, "--out", rebuilt, "--target-duration", "2", "--json"]),
            0,
        )
        self.assertEqual(main(["validate", rebuilt, "--json"]), 0)

    def test_missing_input_is_usage_error(self):
        self.assertEqual(
            main(["validate", str(self.dir / "missing.mp4"), "--json"]), 1
        )

    def test_bad_target_duration_is_usage_error(self):
        sample = str(self.dir / "sample.mp4")
        main(["fixture", "--out", sample])
        code = main(
            [
                "rebuild",
                sample,
                "--out",
                str(self.dir / "out.mp4"),
                "--target-duration",
                "0",
            ]
        )
        self.assertEqual(code, 1)

    def test_damaged_input_is_structure_error_and_leaves_no_output(self):
        data = bytearray(fixture.build_default())
        start, _end = _nested_box(data, [b"moov", b"trak", b"mdia", b"minf", b"stbl", b"stco"])
        entry = start + 8 + 4 + 4
        data[entry : entry + 4] = struct.pack(">I", 0x00100000)
        source = self.dir / "bad.mp4"
        source.write_bytes(data)
        output = self.dir / "bad-out.mp4"
        code = main(
            [
                "rebuild",
                str(source),
                "--out",
                str(output),
                "--target-duration",
                "2",
            ]
        )
        self.assertEqual(code, 2)
        self.assertFalse(output.exists())

    def test_missing_timescale_is_usage_error(self):
        data = fixture.build_default(include_sidx=False)
        source = self.dir / "nosidx.mp4"
        source.write_bytes(data)
        output = self.dir / "nosidx-out.mp4"
        code = main(
            ["rebuild", str(source), "--out", str(output), "--target-duration", "2"]
        )
        self.assertEqual(code, 1)
        self.assertFalse(output.exists())

    def test_validate_bad_index_is_structure_error(self):
        bad = fixture.build_default(
            index_override=fixture.encode_sidx(
                version=0,
                reference_id=1,
                timescale=1,
                earliest_presentation_time=0,
                first_offset=536,
                references=[(220, 2, 0x90000000), (220, 4, 0x90000000)],
            )
        )
        source = self.dir / "bad-index.mp4"
        source.write_bytes(bad)
        self.assertEqual(main(["validate", str(source), "--json"]), 2)


if __name__ == "__main__":
    unittest.main()
