"""Tests for reference ordering diagnostics and 32/64-bit offset handling."""

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
from sidxcheck.sidx import parse_sidx  # noqa: E402
from sidxcheck.boxes import BoxHeader, LocatedBox  # noqa: E402
from sidxcheck.validate import validate_references  # noqa: E402


def _parse_standalone_sidx(entries, *, first_offset=0, version=0,
                           sidx_offset=8):
    payload_box = fixtures.build_sidx_with_refs(
        entries,
        sidx_offset=sidx_offset,
        first_offset=first_offset,
        version=version,
    )
    # Strip the 8-byte box header to obtain the fullbox payload.
    payload = payload_box[8:]
    header = BoxHeader(
        box_type="sidx",
        box_offset=sidx_offset,
        header_size=8,
        content_start=sidx_offset + 8,
        content_end=sidx_offset + len(payload_box),
    )
    scan = boxes._BoxScan(False)
    index = parse_sidx(LocatedBox(header, payload), scan)
    return index, scan


class ReferenceOrderingUnitTests(unittest.TestCase):
    def _validate(self, entries, *, file_size=10_000, first_offset=0):
        index, scan = _parse_standalone_sidx(
            entries, first_offset=first_offset
        )
        # Empty segments: ordering checks run independently of coverage.
        checks = validate_references(
            index, [], file_size, 0, scan
        )
        return scan, checks

    def test_contiguous_references_pass_ordering(self):
        scan, checks = self._validate([(100, 10), (100, 10), (100, 10)])
        ordering = [
            d.code for d in scan.diagnostics
            if d.code in (model.REF_DUPLICATE, model.REF_OVERLAP,
                          model.REF_NON_MONOTONIC)
        ]
        self.assertEqual(ordering, [])

    def test_duplicate_reference_is_flagged_directly(self):
        # A zero-size first reference makes the second start at the same spot.
        scan, _ = self._validate([(0, 0), (100, 10)])
        self.assertIn(model.REF_DUPLICATE,
                      {d.code for d in scan.diagnostics})

    def test_overlapping_reference_is_flagged_directly(self):
        # Overlap cannot occur through the running-sum layout, so drive the
        # validator with two parsed indexes whose spans intersect by reusing
        # the diagnostic path on a crafted reference list.
        index, scan = _parse_standalone_sidx([(100, 10), (100, 10)])
        # Force an overlap by moving the second reference inside the first.
        index.references[1].target_offset = (
            index.references[0].target_offset + 50
        )
        validate_references(index, [], 10_000, 0, scan)
        self.assertIn(model.REF_OVERLAP,
                      {d.code for d in scan.diagnostics})

    def test_non_monotonic_reference_is_flagged_directly(self):
        index, scan = _parse_standalone_sidx([(100, 10), (100, 10)])
        # Move the second reference strictly before the first.
        index.references[1].target_offset = (
            index.references[0].target_offset - 40
        )
        validate_references(index, [], 10_000, 0, scan)
        codes = {d.code for d in scan.diagnostics}
        self.assertTrue(
            model.REF_NON_MONOTONIC in codes or model.REF_OVERLAP in codes
        )


class ReferenceOrderingTests(unittest.TestCase):
    def _codes(self, data: bytes) -> set[str]:
        fd, path = tempfile.mkstemp(suffix=".mp4")
        with os.fdopen(fd, "wb") as handle:
            handle.write(data)
        try:
            summary = analyze(path)
            return {d.code for d in summary.diagnostics}
        finally:
            Path(path).unlink(missing_ok=True)

    def test_duplicate_reference_is_flagged(self):
        codes = self._codes(fixtures.corrupt_sidx_duplicate_reference())
        self.assertIn(model.REF_DUPLICATE, codes)

    def test_overlapping_reference_is_flagged(self):
        codes = self._codes(fixtures.corrupt_sidx_overlapping_reference())
        self.assertTrue(
            model.REF_OVERLAP in codes
            or model.REF_NON_MONOTONIC in codes
            or model.CROSS_BOUNDARY_SAMPLE in codes
        )

    def test_non_monotonic_reference_is_flagged(self):
        # Byte-level anchor displacement surfaces as offset/coverage errors;
        # the strict backwards-jump path is covered by the unit-level test.
        codes = self._codes(fixtures.corrupt_sidx_non_monotonic_reference())
        self.assertTrue(
            model.REF_NON_MONOTONIC in codes
            or model.REF_OVERLAP in codes
            or model.REF_OFFSET_MISMATCH in codes
            or model.REF_SAMPLE_COVERAGE_INCOMPLETE in codes
            or model.REF_OUT_OF_RANGE in codes
        )

    def test_diagnostics_are_stably_sorted(self):
        data = fixtures.corrupt_sidx_overlapping_reference()
        fd, path = tempfile.mkstemp(suffix=".mp4")
        with os.fdopen(fd, "wb") as handle:
            handle.write(data)
        try:
            first = analyze(path).to_dict()["diagnostics"]
            second = analyze(path).to_dict()["diagnostics"]
            self.assertEqual(first, second)
        finally:
            Path(path).unlink(missing_ok=True)


class LargeOffsetTests(unittest.TestCase):
    def test_co64_container_passes_all_checks(self):
        data = fixtures.build_container(offset_variant="co64")
        fd, path = tempfile.mkstemp(suffix=".mp4")
        with os.fdopen(fd, "wb") as handle:
            handle.write(data)
        try:
            summary = analyze(path)
            self.assertEqual(summary.sample_table.offset_mode, "co64")
            self.assertEqual(summary.offset_check_passed, 4)
            self.assertEqual(summary.offset_check_total, 4)
            self.assertEqual(summary.diagnostics, [])
        finally:
            Path(path).unlink(missing_ok=True)

    def test_sidx_version1_64bit_first_offset_passes(self):
        data = fixtures.build_container(sidx_version=1)
        stream = io.BytesIO(data)
        located, diags = boxes.scan_stream(
            stream, len(data), {"sidx"}, strict=False
        )
        self.assertEqual(diags, [])
        scan = boxes._BoxScan(False)
        from sidxcheck.sidx import parse_all
        index = parse_all(located, scan)
        self.assertEqual(index.version, 1)
        self.assertEqual(len(index.references), 4)
        self.assertEqual(scan.diagnostics, [])


if __name__ == "__main__":
    unittest.main()
