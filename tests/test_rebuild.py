"""Correctness tests for fixture layout, rebuild and validation."""

from __future__ import annotations

import json
import os
import tempfile
import unittest
from pathlib import Path

from mediidx import fixture
from mediidx.box_reader import BoxReader
from mediidx.errors import UsageError
from mediidx.layout import plan_layout, segment_boundaries
from mediidx.rebuild import rebuild
from mediidx.sidx import encode_sidx, find_sidx, reference_offsets
from mediidx.stbl import find_mdat, parse_track
from mediidx.validator import validate_bytes


def _parsed(data: bytes):
    view = memoryview(data)
    roots = BoxReader(view).parse_top_level()
    mdat = find_mdat(view, roots)
    track = parse_track(view, roots, mdat)
    return view, roots, mdat, track


class DefaultFixtureTests(unittest.TestCase):
    def test_fixture_shape(self):
        _view, roots, mdat, track = _parsed(fixture.build_default())
        self.assertEqual([box.type for box in roots], [b"ftyp", b"sidx", b"moov", b"mdat"])
        self.assertEqual(track.sample_count, 6)
        self.assertEqual(track.timescale, 1)
        self.assertEqual([s.size for s in track.samples], [100, 120, 80, 140, 100, 160])
        self.assertEqual([s.duration for s in track.samples], [1, 1, 2, 1, 1, 2])
        self.assertEqual([c.size for c in track.chunks], [100, 120, 80, 140, 100, 160])

    def test_layout_matches_acceptance_boundaries(self):
        _view, _roots, _mdat, track = _parsed(fixture.build_default())
        layout = plan_layout(track, 2)
        self.assertEqual(segment_boundaries(layout), [[0, 2], [2, 4], [4, 6]])
        self.assertEqual(len(layout.segments), 3)
        self.assertEqual(layout.segments[0].byte_range, (track.chunks[0].offset, track.chunks[1].end))

    def test_oversized_chunk_is_isolated(self):
        _view, _roots, _mdat, track = _parsed(
            fixture.build_default(
                durations=(5, 1, 1, 1, 1, 1), sizes=(100, 120, 80, 140, 100, 160)
            )
        )
        layout = plan_layout(track, 2)
        self.assertIsNotNone(layout.segments[0].isolated_reason)
        self.assertEqual(segment_boundaries(layout)[0], [0, 1])
        self.assertTrue(any("exceeds target" in note for note in layout.notes))

    def test_validation_of_default_fixture(self):
        report = validate_bytes(fixture.build_default())
        self.assertTrue(report.ok)
        self.assertEqual(report.sample_entries, 6)
        self.assertEqual(report.reference_count, 3)
        self.assertEqual(report.boundaries, [[0, 2], [2, 4], [4, 6]])
        self.assertEqual(report.index_offset_check, "ok")
        self.assertTrue(all(check["status"] == "ok" for check in report.offset_checks))
        self.assertEqual(report.duration_check["sidx_total"], 8)
        self.assertEqual(report.coverage_check["covered_chunks"], 6)


class RebuildTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(prefix="mediidx-test-")
        self.dir = Path(self.tmp.name)

    def tearDown(self):
        self.tmp.cleanup()

    def _write(self, name: str, data: bytes) -> str:
        path = self.dir / name
        path.write_bytes(data)
        return str(path)

    def test_rebuild_acceptance_summary(self):
        source = self._write("sample.mp4", fixture.build_default())
        output = str(self.dir / "rebuilt.mp4")
        result = rebuild(source, output, target_duration=2)
        summary = result.to_dict()
        self.assertEqual(summary["sample_entries"], 6)
        self.assertEqual(summary["index_offset_check"], "ok")
        self.assertEqual(summary["boundaries"], [[0, 2], [2, 4], [4, 6]])
        self.assertEqual(summary["reference_count"], 3)
        self.assertTrue(os.path.exists(output))

    def test_validate_rebuilt_file_matches(self):
        source = self._write("sample.mp4", fixture.build_default())
        output = str(self.dir / "rebuilt.mp4")
        rebuild(source, output, target_duration=2)
        report = validate_bytes(Path(output).read_bytes())
        self.assertTrue(report.ok)
        self.assertEqual(report.sample_entries, 6)
        self.assertEqual(report.boundaries, [[0, 2], [2, 4], [4, 6]])
        for check in report.offset_checks:
            self.assertEqual(check["status"], "ok")
            self.assertEqual(check["reference_offset"], check["expected_offset"])
            self.assertEqual(check["referenced_size"], check["expected_size"])
            self.assertEqual(check["reference_duration"], check["expected_duration"])

    def test_source_is_not_modified(self):
        original = fixture.build_default()
        source = self._write("sample.mp4", original)
        output = str(self.dir / "rebuilt.mp4")
        rebuild(source, output, target_duration=2)
        self.assertEqual(Path(source).read_bytes(), original)

    def test_same_path_rejected(self):
        source = self._write("sample.mp4", fixture.build_default())
        with self.assertRaises(UsageError):
            rebuild(source, source, target_duration=2)

    def test_stale_index_is_replaced_and_preserves_ept(self):
        bad = encode_sidx(
            version=0,
            reference_id=1,
            timescale=1,
            earliest_presentation_time=7,
            first_offset=536,
            references=[(220, 2, (1 << 31) | (1 << 28)), (220, 3, (1 << 31) | (1 << 28))],
        )
        data = fixture.build_default(index_override=bad)
        self.assertFalse(validate_bytes(data).ok)
        source = self._write("stale.mp4", data)
        output = str(self.dir / "fixed.mp4")
        result = rebuild(source, output, target_duration=2)
        self.assertEqual(result.earliest_presentation_time, 7)
        self.assertEqual(result.reference_count, 3)
        self.assertTrue(validate_bytes(Path(output).read_bytes()).ok)

    def test_co64_offsets_round_trip(self):
        data = fixture.build_default(use_co64=True)
        source = self._write("co64.mp4", data)
        output = str(self.dir / "co64-out.mp4")
        result = rebuild(source, output, target_duration=2)
        self.assertEqual(result.boundaries, [[0, 2], [2, 4], [4, 6]])
        view = memoryview(Path(output).read_bytes())
        roots = BoxReader(view).parse_top_level()
        self.assertIsNotNone(
            next(
                box
                for root in roots
                for box in root.walk()
                if box.type == b"co64"
            )
        )

    def test_sidx_version_1_round_trip(self):
        data = fixture.build_default(
            sidx_version=1, earliest_presentation_time=2**33
        )
        source = self._write("v1.mp4", data)
        output = str(self.dir / "v1-out.mp4")
        result = rebuild(source, output, target_duration=2)
        self.assertEqual(result.version, 1)
        self.assertEqual(result.earliest_presentation_time, 2**33)
        self.assertTrue(validate_bytes(Path(output).read_bytes()).ok)

    def test_missing_index_requires_timescale_argument(self):
        source = self._write("nosidx.mp4", fixture.build_default(include_sidx=False))
        output = str(self.dir / "nosidx-out.mp4")
        with self.assertRaises(UsageError):
            rebuild(source, output, target_duration=2)
        result = rebuild(source, output, target_duration=2, timescale=1)
        self.assertEqual(result.boundaries, [[0, 2], [2, 4], [4, 6]])

    def test_reference_ranges_are_exact_chunk_spans(self):
        source = self._write("sample.mp4", fixture.build_default())
        output = str(self.dir / "rebuilt.mp4")
        result = rebuild(source, output, target_duration=2)
        data = Path(output).read_bytes()
        view = memoryview(data)
        roots = BoxReader(view).parse_top_level()
        sidx = find_sidx(view, roots)
        _view, _roots, _mdat, track = _parsed(data)
        ranges = reference_offsets(sidx)
        self.assertEqual(len(ranges), 3)
        for (start, end), segment in zip(ranges, result.segments):
            self.assertEqual(start, segment["byte_start"])
            self.assertEqual(end, segment["byte_end"])
        self.assertEqual(ranges[0], (track.chunks[0].offset, track.chunks[1].end))


if __name__ == "__main__":
    unittest.main()
