"""Output sinks: one terminal summary and one JSON Lines document.

Both sinks accept the same ``AuditResult`` sequence and read nothing but
``report.summary_fields`` / ``report.result_to_json``.  That is the invariant the
acceptance test checks: the numbers printed to the terminal and the numbers in
the JSON file come from one computation, not two.
"""

from __future__ import annotations

import json
import sys
from pathlib import Path
from typing import IO, Iterable, Sequence

from .errors import OutputError
from .model import AuditResult
from .report import REPORT_FIELDS, result_to_json, summary_fields

__all__ = ["render_table", "write_table", "write_json_lines", "write_json_stream"]

#: Numeric columns stay right-aligned; Truncated is a yes/no flag.
_TEXT_FIELDS = frozenset({"file"})
_BOOL_FIELDS = frozenset({"truncated"})


def _cell(value: object) -> str:
    if isinstance(value, bool):
        return "yes" if value else "no"
    if value is None:
        return "-"
    return str(value)


def render_table(results: Sequence[AuditResult]) -> str:
    """Render the terminal summary table plus a line per failure or warning."""
    rows = [[_cell(summary_fields(result)[field]) for field in REPORT_FIELDS] for result in results]
    headers = list(REPORT_FIELDS)

    widths = [len(header) for header in headers]
    for row in rows:
        for index, value in enumerate(row):
            widths[index] = max(widths[index], len(value))

    def format_row(values: Sequence[str]) -> str:
        cells = []
        for index, value in enumerate(values):
            if headers[index] in _TEXT_FIELDS:
                cells.append(value.ljust(widths[index]))
            else:
                cells.append(value.rjust(widths[index]))
        return "  ".join(cells).rstrip()

    lines = [format_row(headers), format_row(["-" * width for width in widths])]
    lines.extend(format_row(row) for row in rows)

    for result in results:
        if not result.ok:
            reason = f" ({result.error_reason})" if result.error_reason else ""
            lines.append(f"! {result.display_path}: {result.error}{reason}")
        for warning in result.warnings:
            lines.append(f"* {result.display_path}: {warning}")
        if result.skipped_reason:
            lines.append(f"- {result.display_path}: skipped ({result.skipped_reason})")

    return "\n".join(lines)


def write_table(results: Sequence[AuditResult], stream: IO[str] | None = None) -> None:
    """Print the terminal summary; a closed stdout is an output failure."""
    target = stream if stream is not None else sys.stdout
    try:
        target.write(render_table(results) + "\n")
        target.flush()
    except OSError as exc:
        raise OutputError(f"cannot write terminal summary: {exc}", reason="write_failed") from exc


def _json_line(result: AuditResult) -> str:
    try:
        return json.dumps(result_to_json(result), ensure_ascii=False, allow_nan=False)
    except (TypeError, ValueError) as exc:
        raise OutputError(
            f"cannot serialise result for {result.display_path}: {exc}",
            reason="serialize_failed",
        ) from exc


def write_json_lines(results: Iterable[AuditResult], destination: str | Path) -> None:
    """Write one JSON object per audited file, atomically.

    The document is built completely before the destination is touched, so a
    failure part-way through a batch cannot leave a truncated report behind.
    """
    lines = [_json_line(result) for result in results]
    payload = ("\n".join(lines) + "\n") if lines else ""

    target = Path(destination)
    try:
        target.parent.mkdir(parents=True, exist_ok=True)
        temporary = target.with_name(target.name + ".tmp")
        temporary.write_text(payload, encoding="utf-8")
        temporary.replace(target)
    except OSError as exc:
        raise OutputError(
            f"cannot write JSON output to {target}: {exc}", reason="write_failed"
        ) from exc


def write_json_stream(results: Iterable[AuditResult], stream: IO[str] | None = None) -> None:
    """Print the same JSON Lines document to a stream (``--json``)."""
    target = stream if stream is not None else sys.stdout
    try:
        for result in results:
            target.write(_json_line(result) + "\n")
        target.flush()
    except OSError as exc:
        raise OutputError(f"cannot write JSON output: {exc}", reason="write_failed") from exc
