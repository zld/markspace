"""Tests for box/sidx/stbl parsing, rebuilding and diagnostics."""

from __future__ import annotations

import io
import os
import sys
import tempfile
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "src"))
sys.path.insert(0, str(Path(__file__).resolve().parent))

import fixtures  # noqa: E402
from sidxcheck import boxes, model  # noqa: E402
from sidxcheck.pipeline import analyze  # noqa: E402
from sidxcheck.rebuild import rebuild  # noqa: E402
from sidxcheck.sidx import parse_all as parse_sidx_all  # noqa: E402
from sidxcheck.stbl import build_sample_table  # noqa: E402
from sidxcheck.validate import validate_references  # noqa: E402


class BoxParsingTests(unittest.TestCase):
    def _scan(self, data, strict=False):
        stream = io.BytesIO(data)
        return boxes.scan_stream(
            stream, len(data), boxes.CONTAINER_TYPES | {"sidx", "stsz"},
            strict=strict,
        )

    def test_extended_largesize_box_is_accepted(self):
        import struct
        payload = b"x" * 40
        extended = struct.pack(
            ">I4sQ", 1, b"sidx", 16 + len(payload)
        ) + payload
        found, diags = self._scan(extended)
        self.assertEqual(diags, [])
        types = {b.header.box_type for b in found}
        self.assertIn("sidx", types)
        sidx = next(b for b in found if b.header.box_type == "sidx")
        self.assertEqual(sidx.header.header_size, 16)
        self.assertEqual(sidx.header.content_size, len(payload))

    def test_truncated_header_is_diagnosed(self):
        found, diags = self._scan(fixtures.corrupt_truncated_header())
        self.assertTrue(
            any(d.code == model.BOX_TRUNCATED_HEADER for d in diags)
        )

    def test_box_beyond_file_boundary_is_rejected(self):
        found, diags = self._scan(fixtures.corrupt_box_beyond_file())
        self.assertTrue(any(d.code == model.BOX_BEYOND_FILE for d in diags))

    def test_extended_size_below_header_is_rejected(self):
        found, diags = self._scan(
            fixtures.corrupt_extended_size_too_small()
        )
        self.assertTrue(
            any(d.code == model.BOX_SIZE_BELOW_HEADER for d in diags)
        )

    def test_strict_mode_raises_on_structural_error(self):
        with self.assertRaises(model.CorruptContainerError):
            self._scan(fixtures.corrupt_box_beyond_file(), strict=True)

    def test_size_two_size32_is_rejected(self):
        data = b"\x00\x00\x00\x02" + b"mdat"
        _, diags = self._scan(data)
        self.assertTrue(
            any(d.code == model.BOX_SIZE_BELOW_HEADER for d in diags)
        )

    def test_diagnostic_carries_box_offset(self):
        _, diags = self._scan(fixtures.corrupt_box_beyond_file())
        diag = next(d for d in diags if d.code == model.BOX_BEYOND_FILE)
        self.assertEqual(diag.box_offset, 0)


class SidxParsingTests(unittest.TestCase):
    def _parse(self, data):
        stream = io.BytesIO(data)
        located, diags = boxes.scan_stream(
            stream, len(data), {"sidx"}, strict=False
        )
        scan = boxes._BoxScan(False)
        scan.diagnostics.extend(diags)
        return parse_sidx_all(located, scan), scan

    def test_version0_references_resolved(self):
        index, scan = self._parse(fixtures.build_container())
        self.assertIsNotNone(index)
        self.assertEqual(index.version, 0)
        self.assertEqual(index.timescale, fixtures.TIMESCALE)
        self.assertEqual(len(index.references), fixtures.FRAGMENT_COUNT)
        self.assertTrue(index.references[0].starts_with_sap)
        # Offsets must be strictly increasing and contiguous.
        offsets = [r.target_offset for r in index.references]
        self.assertEqual(offsets, sorted(offsets))
        for earlier, later in zip(index.references, index.references[1:]):
            self.assertEqual(
                earlier.target_offset + earlier.referenced_size,
                later.target_offset,
            )

    def test_version1_uses_64bit_fields(self):
        index, _ = self._parse(fixtures.build_container(sidx_version=1))
        self.assertEqual(index.version, 1)
        self.assertEqual(len(index.references), fixtures.FRAGMENT_COUNT)

    def test_unknown_version_diagnosed_with_offset(self):
        index, scan = self._parse(fixtures.corrupt_sidx_unknown_version())
        self.assertIsNone(index)
        diag = next(d for d in scan.diagnostics
                    if d.code == model.SIDX_UNKNOWN_VERSION)
        self.assertIsNotNone(diag.box_offset)

    def test_zero_timescale_diagnosed(self):
        index, scan = self._parse(fixtures.corrupt_sidx_zero_timescale())
        self.assertIsNotNone(index)  # lenient mode keeps parsing
        self.assertTrue(
            any(d.code == model.SIDX_ZERO_TIMESCALE for d in scan.diagnostics)
        )


class StblParsingTests(unittest.TestCase):
    def _table(self, data, *, variant_names=None):
        stream = io.BytesIO(data)
        targets = {"stsz", "stsc", "stco", "co64", "stts"}
        located, _ = boxes.scan_stream(
            stream, len(data), targets, strict=False
        )
        leaves = {}
        for item in located:
            if item.header.box_type in targets:
                leaves.setdefault(item.header.box_type, item)
        scan = boxes._BoxScan(False)
        return build_sample_table(leaves, scan), scan

    def test_valid_layout_120_samples_four_chunks(self):
        table, scan = self._table(fixtures.build_container())
        self.assertIsNotNone(table)
        self.assertEqual(table.total_samples, fixtures.SAMPLE_COUNT)
        self.assertEqual(len(table.chunk_offsets), fixtures.FRAGMENT_COUNT)
        self.assertEqual(table.offset_mode, "stco")
        self.assertEqual(scan.diagnostics, [])

    def test_co64_offsets_supported(self):
        table, scan = self._table(
            fixtures.build_container(offset_variant="co64")
        )
        self.assertIsNotNone(table)
        self.assertEqual(table.offset_mode, "co64")
        self.assertEqual(table.total_samples, fixtures.SAMPLE_COUNT)

    def test_sample_offsets_are_contiguous_within_chunks(self):
        table, _ = self._table(fixtures.build_container())
        from sidxcheck.stbl import sample_size_at
        for i in range(1, table.total_samples):
            if i % fixtures.CHUNK_SAMPLES != 0:
                self.assertEqual(
                    table.sample_offsets[i],
                    table.sample_offsets[i - 1]
                    + sample_size_at(table, i - 1),
                )

    def test_stsz_count_mismatch_diagnosed(self):
        table, scan = self._table(fixtures.corrupt_stsz_bad_count())
        self.assertTrue(
            any(d.code == model.SAMPLE_COUNT_INCONSISTENT
                for d in scan.diagnostics)
        )

    def test_stsc_first_chunk_not_one_diagnosed(self):
        table, scan = self._table(fixtures.corrupt_stsc_bad_first_chunk())
        self.assertIsNone(table)
        self.assertTrue(
            any(d.code == model.STSC_FIRST_NOT_ONE for d in scan.diagnostics)
        )

    def test_truncated_stbl_stops_track_parsing(self):
        # A truncated table box yields either a diagnostic or an unusable
        # table; lenient mode must never invent sample data.
        table, scan = self._table(fixtures.corrupt_truncated_stbl())
        self.assertTrue(scan.diagnostics or table is None)


class RebuildAndValidateTests(unittest.TestCase):
    def _components(self, data):
        fd, path = tempfile.mkstemp(suffix=".mp4")
        with os.fdopen(fd, "wb") as handle:
            handle.write(data)
        self.addCleanup(lambda: Path(path).unlink(missing_ok=True))
        stream = open(path, "rb")
        self.addCleanup(stream.close)
        located, _ = boxes.scan_stream(
            stream, len(data),
            {"sidx", "stsz", "stsc", "stco", "co64", "stts"},
            strict=False,
        )
        scan = boxes._BoxScan(False)
        sidx_index = parse_sidx_all(
            [b for b in located if b.header.box_type == "sidx"], scan
        )
        leaves = {}
        for item in located:
            if item.header.box_type in {"stsz", "stsc", "stco", "co64",
                                        "stts"}:
                leaves.setdefault(item.header.box_type, item)
        table = build_sample_table(leaves, scan)
        return table, sidx_index, scan, stream

    def test_valid_container_rebuilds_four_segments(self):
        data = fixtures.build_container()
        table, sidx_index, scan, stream = self._components(data)
        segments, media_end = rebuild(
            table, sidx_index, len(data), scan, stream=stream
        )
        checks = validate_references(
            sidx_index, segments, len(data), media_end, scan,
            rebuild_diagnostics=scan.diagnostics,
        )
        self.assertEqual(len(segments), fixtures.FRAGMENT_COUNT)
        self.assertTrue(all(c.passed for c in checks))
        self.assertEqual(scan.diagnostics, [])
        self.assertEqual(segments[0].first_sample, 0)
        self.assertEqual(segments[-1].last_sample, fixtures.SAMPLE_COUNT - 1)
        # Media ends at the final sample, which is exactly the EOF here.
        self.assertEqual(media_end, len(data))

    def test_offset_mismatch_is_classified(self):
        data = fixtures.corrupt_sidx_offset_mismatch()
        table, sidx_index, scan, stream = self._components(data)
        segments, media_end = rebuild(
            table, sidx_index, len(data), scan, stream=stream
        )
        validate_references(
            sidx_index, segments, len(data), media_end, scan,
            rebuild_diagnostics=scan.diagnostics,
        )
        codes = {d.code for d in scan.diagnostics}
        self.assertTrue(
            model.REF_OFFSET_MISMATCH in codes
            or model.CROSS_BOUNDARY_SAMPLE in codes
        )

    def test_size_mismatch_is_classified(self):
        data = fixtures.corrupt_sidx_size_mismatch()
        table, sidx_index, scan, stream = self._components(data)
        segments, media_end = rebuild(
            table, sidx_index, len(data), scan, stream=stream
        )
        validate_references(
            sidx_index, segments, len(data), media_end, scan,
            rebuild_diagnostics=scan.diagnostics,
        )
        codes = {d.code for d in scan.diagnostics}
        self.assertIn(model.REF_SIZE_MISMATCH, codes)

    def test_trailing_data_is_reported(self):
        data = fixtures.corrupt_trailing_data()
        table, sidx_index, scan, stream = self._components(data)
        segments, media_end = rebuild(
            table, sidx_index, len(data), scan, stream=stream
        )
        validate_references(
            sidx_index, segments, len(data), media_end, scan,
            rebuild_diagnostics=scan.diagnostics,
        )
        self.assertTrue(
            any(d.code == model.TRAILING_DATA for d in scan.diagnostics)
        )


class PipelineDiagnosticTests(unittest.TestCase):
    def _write(self, data: bytes) -> str:
        fd, path = tempfile.mkstemp(suffix=".mp4")
        with os.fdopen(fd, "wb") as handle:
            handle.write(data)
        self.addCleanup(lambda: Path(path).unlink(missing_ok=True))
        return path

    def test_valid_container_summary_fields(self):
        path = self._write(fixtures.build_container())
        summary = analyze(path)
        self.assertEqual(summary.sample_entries, 120)
        self.assertEqual(summary.index_refs, 4)
        self.assertEqual(summary.offset_check_passed, 4)
        self.assertEqual(summary.offset_check_total, 4)
        self.assertEqual(len(summary.rebuilt_segments), 4)
        self.assertEqual(summary.diagnostics, [])
        segment = summary.rebuilt_segments[0]
        self.assertLess(segment.start, segment.end)

    def test_diagnostics_are_deterministically_sorted(self):
        path = self._write(fixtures.corrupt_sidx_size_mismatch())
        first = analyze(path).to_dict()
        second = analyze(path).to_dict()
        self.assertEqual(first, second)
        offsets = [
            d.get("box_offset", -1) for d in first["diagnostics"]
        ]
        self.assertEqual(offsets, sorted(offsets))

    def test_lenient_mode_keeps_going_while_strict_fails(self):
        path = self._write(fixtures.corrupt_sidx_zero_timescale())
        lenient = analyze(path, strict=False)
        strict = analyze(path, strict=True)
        self.assertTrue(lenient.diagnostics)
        self.assertTrue(strict.fatal)

    def test_untrusted_entry_count_does_not_allocate(self):
        # A stsz claiming 2**30 entries but containing no entry payload.
        import struct
        stsz_body = (
            bytes([0, 0, 0, 0]) + struct.pack(">II", 0, 1 << 30)
        )
        stsz = struct.pack(">I4s", 8 + len(stsz_body), b"stsz") + stsz_body
        stbl = struct.pack(">I4s", 8 + len(stsz), b"stbl") + stsz
        moov = struct.pack(">I4s", 8 + len(stbl), b"moov") + stbl
        path = self._write(moov)
        summary = analyze(path)
        self.assertTrue(
            any(d.code == model.ENTRY_COUNT_UNTRUSTED
                for d in summary.diagnostics)
        )


if __name__ == "__main__":
    unittest.main()
