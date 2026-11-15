"""End-to-end analysis pipeline wiring boxes, sidx, stbl, rebuild and checks."""

from __future__ import annotations

import os

from . import boxes, model
from .rebuild import rebuild
from .sidx import parse_all as parse_sidx_all
from .stbl import build_sample_table
from .validate import validate_references

TARGET_TYPES = frozenset({"sidx", "stsz", "stsc", "stco", "co64", "stts"})
# Boxes that only make sense inside stbl; anything else named sidx is an index.
STBL_LEAVES = frozenset({"stsz", "stsc", "stco", "co64", "stts"})


class InputError(Exception):
    """Raised when the input path cannot be opened (maps to exit code 2)."""


def _new_scan(strict: bool) -> boxes._BoxScan:
    return boxes._BoxScan(strict)


def analyze(path: str, *, strict: bool = False) -> model.AnalysisSummary:
    """Analyse one MP4 file and return a fully populated summary."""
    try:
        file_size = os.path.getsize(path)
    except OSError as exc:
        raise InputError(f"cannot stat input {path!r}: {exc}") from exc

    summary = model.AnalysisSummary(file_path=path, file_size=file_size)
    scan = _new_scan(strict)
    try:
        stream = open(path, "rb")
    except OSError as exc:
        raise InputError(f"cannot open input {path!r}: {exc}") from exc

    try:
        with stream:
            located, box_diags = boxes.scan_stream(
                stream, file_size, TARGET_TYPES, strict=strict
            )
        scan.diagnostics.extend(box_diags)

        sidx_boxes = [b for b in located if b.header.box_type == "sidx"]
        sidx_index = parse_sidx_all(sidx_boxes, scan)
        if sidx_index is None and not scan.diagnostics:
            scan.fail(
                model.SIDX_MISSING,
                "no readable sidx box found in the container",
                box_type="sidx",
            )

        leaves: dict[str, boxes.LocatedBox] = {}
        for item in located:
            name = item.header.box_type
            if name in STBL_LEAVES and name not in leaves:
                leaves[name] = item
        table = build_sample_table(leaves, scan)
        if table is None:
            scan.fail(
                model.SAMPLE_TABLE_INCOMPLETE,
                "no usable sample table (stbl) could be reconstructed",
                box_type="stbl",
            )

        summary.diagnostics = model.sort_diagnostics(scan.diagnostics)
        summary.fatal = any(
            d.severity == model.SEV_FATAL for d in summary.diagnostics
        )
        if table is None or sidx_index is None:
            # Structural failure: nothing further can be located safely.
            return summary

        summary.sidx = sidx_index
        summary.sample_table = table
        summary.sample_entries = table.total_samples
        summary.index_refs = len(sidx_index.references)

        check_scan = _new_scan(strict)
        segments, media_end = rebuild(table, sidx_index, file_size, check_scan)
        checks = validate_references(
            sidx_index, segments, file_size, media_end, check_scan
        )

        summary.rebuilt_segments = segments
        summary.offset_checks = checks
        summary.media_end = media_end
        summary.offset_check_total = len(checks)
        summary.offset_check_passed = sum(1 for c in checks if c.passed)
        summary.diagnostics = model.sort_diagnostics(
            scan.diagnostics + check_scan.diagnostics
        )
        summary.fatal = any(
            d.severity == model.SEV_FATAL for d in summary.diagnostics
        )
        return summary
    except model.CorruptContainerError as exc:
        scan.diagnostics.append(
            model.Diagnostic(
                model.SEV_FATAL,
                "strict_mode_failure",
                f"strict mode aborted: {exc}",
            )
        )
        summary.diagnostics = model.sort_diagnostics(scan.diagnostics)
        summary.fatal = True
        return summary
