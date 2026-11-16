"""End-to-end and unit tests for the sidxmap pipeline."""

from __future__ import annotations

import json
import os
import struct
import tempfile
import unittest

from sidxmap import fixture as fx
from sidxmap.boxes import parse_bytes
from sidxmap.cli import main
from sidxmap.fixture import build_mp4
from sidxmap.model import (
    DEFAULT_MAX_NESTING,
    MappingError,
    MdatExtent,
    StructureError,
    UsageError,
)
from sidxmap.rebuild import build_boundaries
from sidxmap.sidx import compare_references, parse_sidx, rebuild_references
from sidxmap.stbl import parse_tracks
from sidxmap.validate import validate_tracks


def _media(case: str):
    data = build_mp4(case)
    boxes = parse_bytes(data)
    mdats = [
        MdatExtent(offset=b.start + b.header_size, size=len(b.payload))
        for b in boxes
        if b.type == b"mdat"
    ]
    return data, boxes, mdats


class BoxParserTests(unittest.TestCase):
    def test_32_and_64_bit_boxes(self):
        small = fx.box(b"free", b"hello")
        big = fx.box(b"free", b"world", largesize=True)
        roots = parse_bytes(small + big)
        self.assertEqual([b.type for b in roots], [b"free", b"free"])
        self.assertEqual(roots[1].header_size, 16)

    def test_zero_and_truncated_box_rejected(self):
        with self.assertRaises(StructureError):
            parse_bytes(struct.pack(">I", 4) + b"free")  # smaller than header
        with self.assertRaises(StructureError):
            parse_bytes(b"\x00\x00\x00\x20free" + b"x" * 8)  # size beyond data
        with self.assertRaises(StructureError):
            parse_bytes(b"\x00" * 8)  # zero length at top level

    def test_nesting_limit_enforced(self):
        payload = b"x"
        for _ in range(DEFAULT_MAX_NESTING + 2):
            payload = fx.box(b"moov", payload)
        with self.assertRaises(StructureError):
            parse_bytes(payload)

    def test_size_limit_enforced(self):
        data = fx.box(b"free", b"x" * 32)
        with self.assertRaises(UsageError):
            parse_bytes(data, max_file_bytes=8)


class StblPipelineTests(unittest.TestCase):
    def test_valid_multitrack_expands_48_samples(self):
        _data, boxes, mdats = _media("valid-multitrack")
        tracks = parse_tracks(boxes, mdats)
        self.assertEqual([t.track_id for t in tracks], [1, 2])
        self.assertEqual([t.sample_count for t in tracks], [24, 24])
        self.assertTrue(all(s.size == fx.SAMPLE_SIZE for t in tracks for s in t.samples))
        warnings = validate_tracks(tracks, mdats)
        self.assertEqual(warnings, [])

    def test_64bit_offsets_parse(self):
        _data, boxes, mdats = _media("valid-64bit")
        tracks = parse_tracks(boxes, mdats)
        self.assertEqual(sum(t.sample_count for t in tracks), 48)
        co64 = [b for b in _walk(boxes) if b.type == b"co64"]
        self.assertTrue(co64)

    def test_corrupt_stbl_raises_structure(self):
        _data, boxes, mdats = _media("corrupt-stbl")
        with self.assertRaises(StructureError):
            parse_tracks(boxes, mdats)

    def test_truncated_box_raises_structure(self):
        data = build_mp4("truncated-box")
        with self.assertRaises(StructureError):
            parse_bytes(data)


class SidxAndRebuildTests(unittest.TestCase):
    def test_sidx_matches_rebuilt_boundaries(self):
        _data, boxes, mdats = _media("valid-multitrack")
        tracks = parse_tracks(boxes, mdats)
        validate_tracks(tracks, mdats)
        boundaries = build_boundaries(tracks, fx.GROUP_SIZE)
        checked, mismatches = compare_references(
            parse_sidx(boxes, mdats), rebuild_references(boundaries)
        )
        self.assertEqual(checked, 6)
        self.assertEqual(mismatches, 0)
        self.assertEqual(len(boundaries), 6)
        self.assertTrue(all(b.starts_with_sync for b in boundaries))

    def test_stale_sidx_still_rebuilds_but_mismatches(self):
        _data, boxes, mdats = _media("stale-sidx")
        tracks = parse_tracks(boxes, mdats)
        boundaries = build_boundaries(tracks, fx.GROUP_SIZE)
        checked, mismatches = compare_references(
            parse_sidx(boxes, mdats), rebuild_references(boundaries)
        )
        self.assertEqual(checked, 6)
        self.assertGreater(mismatches, 0)
        self.assertEqual(len(boundaries), 6)  # usable manifest still produced

    def test_hierarchical_sidx_rejected(self):
        _data, boxes, mdats = _media("hierarchy-sidx")
        with self.assertRaises(StructureError):
            parse_sidx(boxes, mdats)

    def test_oversize_sample_is_error_not_fake_boundary(self):
        _data, boxes, mdats = _media("valid-multitrack")
        tracks = parse_tracks(boxes, mdats)
        with self.assertRaises(MappingError):
            build_boundaries(tracks, fx.SAMPLE_SIZE - 1)

    def test_duration_budget_splits_groups(self):
        _data, boxes, mdats = _media("valid-multitrack")
        tracks = parse_tracks(boxes, mdats)
        # Video samples are 1000 units long; cap at 2000 -> 2 samples per group,
        # each still starting on a sync boundary where reachable.
        boundaries = build_boundaries(tracks, fx.GROUP_SIZE, max_duration=2000)
        video = [b for b in boundaries if b.track_id == 1]
        self.assertEqual(len(video), 12)
        self.assertEqual(video[0].end_time - video[0].start_time, 2000)

    def test_missing_sync_sample_rejected(self):
        from sidxmap.model import Track
        from sidxmap.model import Sample
        samples = [
            Sample(index=i, offset=1000 + 10 * i, size=10, duration=1000,
                   dts=1000 * i, is_sync=False)
            for i in range(4)
        ]
        track = Track(track_id=9, handler="vide", timescale=24000,
                      samples=samples, has_stss=True)
        mdat = MdatExtent(offset=0, size=10000)
        with self.assertRaises(MappingError):
            validate_tracks([track], [mdat])

    def test_stz2_nibble_sizes_expand(self):
        from sidxmap.boxes import Box
        from sidxmap.stbl import _parse_sizes
        payload = b"\x00\x00\x00\x00" + b"\x00\x00\x00\x04" + bytes([0x12, 0x34])
        stbl = Box(type=b"stbl", header_size=8, payload=b"", start=0,
                   children=(Box(type=b"stz2", header_size=8, payload=payload,
                                 start=0),))
        self.assertEqual(_parse_sizes(stbl, 4), [1, 2, 3, 4])


class CliExitCodeTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.mkdtemp(prefix="sidxmap-")

    def _path(self, name):
        return os.path.join(self.tmp, name)

    def test_valid_fixture_rebuild_exit_zero(self):
        mp4 = self._path("c.mp4")
        out = self._path("r.json")
        self.assertEqual(main(["fixture", "--case", "valid-multitrack", "--out", mp4]), 0)
        self.assertEqual(
            main(["rebuild", mp4, "--max-bytes", str(fx.GROUP_SIZE), "--json", out]), 0
        )
        with open(out, encoding="utf-8") as handle:
            manifest = json.load(handle)
        self.assertEqual(manifest["track_count"], 2)
        self.assertEqual(manifest["sample_entries"], 48)
        self.assertEqual(manifest["index_offset_check"], "PASS")
        self.assertEqual(manifest["checked_refs"], 6)
        self.assertEqual(manifest["mismatches"], 0)
        self.assertEqual(manifest["reconstructed_boundaries"], 6)

    def test_damaged_files_never_emit_manifest(self):
        for case, expected in [
            ("corrupt-stbl", 2),
            ("truncated-box", 2),
            ("hierarchy-sidx", 2),
        ]:
            mp4 = self._path(case + ".mp4")
            out = self._path(case + ".json")
            main(["fixture", "--case", case, "--out", mp4])
            self.assertEqual(
                main(["rebuild", mp4, "--max-bytes", str(fx.GROUP_SIZE), "--json", out]),
                expected,
            )
            self.assertFalse(os.path.exists(out))

    def test_stale_sidx_exit_three_with_manifest(self):
        mp4 = self._path("stale.mp4")
        out = self._path("stale.json")
        main(["fixture", "--case", "stale-sidx", "--out", mp4])
        self.assertEqual(
            main(["rebuild", mp4, "--max-bytes", str(fx.GROUP_SIZE), "--json", out]), 3
        )
        self.assertTrue(os.path.exists(out))

    def test_usage_errors_exit_four(self):
        self.assertEqual(main(["rebuild", self._path("missing.mp4"),
                               "--max-bytes", "1024"]), 4)
        mp4 = self._path("c.mp4")
        main(["fixture", "--case", "valid-multitrack", "--out", mp4])
        self.assertEqual(main(["rebuild", mp4, "--max-bytes", "0"]), 4)
        self.assertEqual(main(["rebuild", mp4]), 4)  # missing required --max-bytes
        self.assertEqual(main(["nonsense"]), 4)      # unknown subcommand


def _walk(boxes):
    found = []
    for box in boxes:
        found.append(box)
        found.extend(_walk(box.children))
    return found


if __name__ == "__main__":
    unittest.main()
