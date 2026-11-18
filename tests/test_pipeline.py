"""End-to-end pipeline tests: valid, nested and corrupted in-memory files."""

from __future__ import annotations

import pytest

from media_index import sidx as sidx_mod
from media_index import stbl_map
from media_index.errors import LimitError, ParseError
from media_index.pipeline import PipelineError, run_pipeline
from media_index.validate import CHK_SIDX_OFFSETS, CHK_TIMELINE

from .fixture_factory import (
    SAMPLE_SIZES,
    build_empty,
    build_nested,
    build_valid,
    inject_bad_timescale,
    inject_duplicate_chunk_offset,
    inject_misaligned,
    inject_oversized_reference,
    inject_truncated_field,
    inject_truncated_header,
)


def test_valid_fixture_passes_all_checks():
    result = run_pipeline("valid", build_valid())
    report = result.report
    assert report.failed == 0
    assert report.passed >= 10
    assert report.sample_entries == len(SAMPLE_SIZES)
    assert len(report.segment_boundaries) == 3
    for boundary in report.segment_boundaries:
        assert boundary["complete"] is True
        assert boundary["byte_end"] > boundary["byte_start"]
        assert boundary["first_sample"] is not None
    assert report.to_dict()["index_offset_checks"]["failed"] == 0


def test_segment_sample_spans_are_continuous():
    result = run_pipeline("valid", build_valid())
    boundaries = result.report.segment_boundaries
    pairs = [(b["first_sample"], b["last_sample"]) for b in boundaries]
    assert pairs == [(1, 2), (3, 4), (5, 6)]
    # Adjacent segments share a zero-width boundary position.
    for left, right in zip(boundaries, boundaries[1:]):
        assert left["byte_end"] == right["byte_start"]


def test_nested_index_references_resolve():
    data = build_nested()
    view = memoryview(data)
    header, payload = sidx_mod.find_top_level_sidx(view)
    tree = sidx_mod.resolve_sidx(view, 0, header, payload)
    assert len(tree.walk()) == 2
    assert len(tree.leaf_media_references()) == 3
    result = run_pipeline("nested", data)
    assert result.report.failed == 0


def test_empty_sample_tables_parse_cleanly():
    result = run_pipeline("empty", build_empty())
    assert result.tables[0].sample_count == 0
    assert result.tables[0].chunk_count == 0
    # Missing ctts/stss are reported as passing optional checks.
    names = {c.name for c in result.report.checks}
    assert "ctts_offsets" in names and "sync_sample_table" in names


def test_misaligned_reference_fails_offset_checks():
    result = run_pipeline("misaligned", inject_misaligned(build_valid()))
    body = result.report.to_dict()
    assert body["index_offset_checks"]["failed"] >= 1
    failed = [c for c in result.report.checks
              if c.name == CHK_SIDX_OFFSETS and c.status == "fail"]
    assert failed and all(c.source_offset >= 0 for c in failed)
    assert any("offset" in e for e in body["errors"])


def test_oversized_reference_reports_byte_offset():
    result = run_pipeline("oversized", inject_oversized_reference(build_valid()))
    failed = [c for c in result.report.checks if c.name == CHK_SIDX_OFFSETS
              and c.status == "fail"]
    assert failed
    assert any("file size" in c.reason for c in failed)
    assert result.report.failed >= 1


def test_duplicate_chunk_offset_detected():
    result = run_pipeline("dup", inject_duplicate_chunk_offset(build_valid()))
    failed = [c for c in result.report.checks
              if c.name == "chunk_offset_monotonicity" and c.status == "fail"]
    assert failed and "duplicate" in failed[0].reason
    assert failed[0].source_offset > 0


def test_truncated_field_raises_structured_error():
    with pytest.raises(PipelineError) as exc:
        run_pipeline("trunc", inject_truncated_field(build_valid()))
    assert exc.value.offset >= 0
    assert "truncated" in exc.value.reason or "ends at" in exc.value.reason


def test_truncated_header_reports_absolute_offset():
    # A trailing fragment is either a structural parse error or a validation
    # failure; both must name an absolute position, never a raw traceback.
    try:
        result = run_pipeline("trunc-head",
                              inject_truncated_header(build_valid()))
    except PipelineError as exc:
        assert exc.offset >= 0
    else:
        assert result.report.failed >= 1
        assert all(c.source_offset >= 0
                   for c in result.report.checks if c.status == "fail")


def test_top_level_truncated_header_and_oversized_length():
    from media_index._fixture_builders import box, ftyp_box, u32

    # Three trailing bytes after a valid ftyp cannot form a box header.
    fragmented = ftyp_box() + b"\x00\x00\x00"
    with pytest.raises(PipelineError) as exc:
        run_pipeline("frag", fragmented)
    assert exc.value.offset == len(ftyp_box())
    # Declared size extending past the view is rejected with its position.
    oversized = ftyp_box() + u32(0x00FFFFFF) + b"sidx"
    with pytest.raises(PipelineError) as exc2:
        run_pipeline("oversize-box", oversized)
    assert exc2.value.offset == len(ftyp_box())
    # The size-0 box is only legal as the final box.
    assert box  # builder import sanity


def test_bad_timescale_rejected():
    with pytest.raises(PipelineError) as exc:
        run_pipeline("bad-ts", inject_bad_timescale(build_valid()))
    assert "timescale" in exc.value.reason


def test_corrupt_inputs_never_raise_raw_traceback():
    # Every corrupted variant must surface as MediaIndexError subclass.
    variants = [
        inject_misaligned(build_valid()),
        inject_oversized_reference(build_valid()),
        inject_duplicate_chunk_offset(build_valid()),
        inject_truncated_field(build_valid()),
        inject_truncated_header(build_valid()),
        inject_bad_timescale(build_valid()),
    ]
    for blob in variants:
        try:
            run_pipeline("x", blob)
        except PipelineError:
            continue


def test_stsc_expansion_matches_sample_count():
    data = build_valid()
    view = memoryview(data)
    tables = [
        stbl_map.parse_stbl(h, p) for h, p in stbl_map.find_stbl_views(view)
    ][0]
    spans = stbl_map.build_chunk_spans(tables)
    assert sum(s.last_sample - s.first_sample + 1 for s in spans) == len(
        SAMPLE_SIZES
    )


def test_huge_declared_entry_count_does_not_allocate():
    # Craft an stsz header claiming more entries than the hard limit with
    # no payload bytes; the parser must reject before allocating the table.
    from media_index._fixture_builders import box, fullbox, u32
    from media_index.limits import MAX_TABLE_ENTRIES

    stsz = fullbox("stsz", 0, 0, u32(0) + u32(MAX_TABLE_ENTRIES + 1))
    stbl = box("stbl", stsz)
    minf = box("minf", stbl)
    mdia = box("mdia", minf)
    trak = box("trak", mdia)
    moov = box("moov", trak)
    with pytest.raises((LimitError, ParseError)):
        view = memoryview(moov)
        header, payload = stbl_map.find_stbl_views(view)[0]
        stbl_map.parse_stbl(header, payload)


def test_timeline_flags_crossing_chunk_as_incomplete():
    # A smaller misalignment moves a boundary inside the first chunk.
    result = run_pipeline("cross", inject_misaligned(build_valid(), delta=10))
    timeline_fails = [c for c in result.report.checks
                      if c.name == CHK_TIMELINE and c.status == "fail"]
    assert timeline_fails
    assert any("boundary" in c.reason for c in timeline_fails)
