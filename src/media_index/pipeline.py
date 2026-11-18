"""End-to-end pipeline shared by the CLI subcommands.

The pipeline reads a file once into bytes, enforces the global size limit,
then runs the self-contained box/sidx/stbl parsers and the tolerant
validator. Parse-level failures are converted into :class:`PipelineError`
carrying the same offset/reason data, so the CLI never prints a traceback.
"""

from __future__ import annotations

from dataclasses import dataclass

from . import limits, sidx as sidx_mod, stbl_map
from .errors import LimitError, MediaIndexError
from .validate import (
    CHK_BOX_STRUCTURE,
    STATUS_FAIL,
    ValidationReport,
    validate_parsed,
)


class PipelineError(MediaIndexError):
    """Raised when a file cannot be parsed far enough to produce a report."""


@dataclass
class PipelineResult:
    path: str
    data: bytes
    tree: object
    tables: list
    report: ValidationReport


def read_file(path: str) -> bytes:
    """Read a file with the global size limit enforced before allocation."""

    import os

    try:
        stat = os.stat(path)
    except OSError as exc:
        raise PipelineError(f"cannot stat file: {exc}") from exc
    if not os.path.isfile(path):
        raise PipelineError(f"not a regular file: {path}")
    if stat.st_size > limits.MAX_FILE_SIZE:
        raise LimitError(
            f"file size {stat.st_size} exceeds limit {limits.MAX_FILE_SIZE}"
        )
    if stat.st_size == 0:
        raise PipelineError("file is empty")
    with open(path, "rb") as handle:
        return handle.read()


def run_pipeline(path: str, data: bytes | None = None) -> PipelineResult:
    """Parse and validate one in-memory file."""

    if data is None:
        data = read_file(path)
    if len(data) > limits.MAX_FILE_SIZE:
        raise LimitError(
            f"file size {len(data)} exceeds limit {limits.MAX_FILE_SIZE}"
        )
    view = memoryview(data)
    try:
        found = sidx_mod.find_top_level_sidx(view)
        if found is None:
            raise PipelineError("no top-level sidx box found")
        header, payload = found
        tree = sidx_mod.resolve_sidx(view, 0, header, payload)
        tables = [
            stbl_map.parse_stbl(stbl_header, stbl_payload)
            for stbl_header, stbl_payload in stbl_map.find_stbl_views(view)
        ]
        report = validate_parsed(path, view, tree, tables, len(data))
    except MediaIndexError as exc:
        # Re-surface as a structured failure with the same offset metadata.
        raise PipelineError(exc.reason, exc.offset) from exc
    return PipelineResult(path=path, data=data, tree=tree, tables=tables,
                          report=report)


def report_parse_failure(path: str, exc: MediaIndexError) -> dict:
    """Build the stable JSON body for a file that failed structural parsing."""

    report = ValidationReport(path=path)
    report.add(CHK_BOX_STRUCTURE, STATUS_FAIL, exc.offset, exc.reason)
    return report.to_dict()


def inspect_summary(result: PipelineResult) -> dict:
    """Stable summary used by the ``inspect`` subcommand."""

    tree = result.tree
    box = tree.box
    tables = result.tables[0] if result.tables else None
    return {
        "path": result.path,
        "file_size": len(result.data),
        "sidx": {
            "version": box.version,
            "reference_id": box.reference_id,
            "timescale": box.timescale,
            "earliest_presentation_time": box.earliest_presentation_time,
            "first_offset": box.first_offset,
            "source_offset": box.box_start,
            "reference_count": len(box.references),
            "nested_sidx_boxes": len(tree.walk()) - 1,
        },
        "stbl": None
        if tables is None
        else {
            "source_offset": tables.stbl_offset,
            "sample_count": tables.sample_count,
            "chunk_count": tables.chunk_count,
            "size_table_type": type(tables.size_table).__name__,
            "has_ctts": tables.ctts is not None,
            "has_stss": tables.stss is not None,
        },
        "segment_boundaries": result.report.to_dict()["segment_boundaries"],
    }
