"""Command line interface: arguments, deterministic output and exit codes.

Exit codes:
  0 - structural and index checks passed (lenient mode tolerates diagnostics)
  2 - argument or file access error
  3 - container corrupt/unparseable, or any error under ``--strict``
"""

from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path

from . import model
from .pipeline import InputError, analyze

EXIT_OK = 0
EXIT_USAGE = 2
EXIT_CORRUPT = 3


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="sidxcheck",
        description="Verify ISO BMFF sidx references against stbl sample tables.",
    )
    source = parser.add_mutually_exclusive_group(required=True)
    source.add_argument("--input", help="MP4 container to analyse")
    source.add_argument(
        "--make-sample",
        metavar="PATH",
        help="generate a deterministic valid sample container at PATH",
    )
    parser.add_argument(
        "--strict",
        action="store_true",
        help="fail on any structural error instead of recovering",
    )
    parser.add_argument(
        "--json",
        dest="json_output",
        action="store_true",
        help="emit a machine-readable JSON summary",
    )
    return parser


def _load_fixture_builder():
    # tests/ is not packaged; the demo chain uses it only for --make-sample.
    root = Path(__file__).resolve().parents[2]
    tests_dir = root / "tests"
    if str(tests_dir) not in sys.path:
        sys.path.insert(0, str(tests_dir))
    from fixtures import build_container  # noqa: WPS433 (intentional lazy import)

    return build_container


def make_sample(path: str) -> int:
    try:
        build_container = _load_fixture_builder()
        data = build_container()
        target = Path(path)
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_bytes(data)
    except OSError as exc:
        print(f"sidxcheck: cannot write sample file {path!r}: {exc}",
              file=sys.stderr)
        return EXIT_USAGE
    except Exception as exc:  # fixture generation failure is a tool fault
        print(f"sidxcheck: sample generation failed: {exc}", file=sys.stderr)
        return EXIT_CORRUPT
    print(f"wrote sample container ({len(data)} bytes) to {path}")
    return EXIT_OK


def _render_text(summary: model.AnalysisSummary) -> str:
    lines = [
        f"file: {summary.file_path}",
        f"file_size: {summary.file_size}",
        f"sample_entries: {summary.sample_entries}",
        f"index_refs: {summary.index_refs}",
        f"offset_checks: {summary.offset_check_passed}/"
        f"{summary.offset_check_total} passed",
        f"rebuilt_segments: {len(summary.rebuilt_segments)}",
    ]
    for seg in summary.rebuilt_segments:
        lines.append(
            f"  ref {seg.reference_index}: bytes [{seg.start}, {seg.end}) "
            f"samples {seg.first_sample}..{seg.last_sample}"
        )
    lines.append(f"diagnostics: {len(summary.diagnostics)}")
    for diag in summary.diagnostics:
        where = (
            f" @{diag.box_offset}" if diag.box_offset is not None else ""
        )
        entry = (
            f" entry {diag.entry_index}"
            if diag.entry_index is not None else ""
        )
        lines.append(f"  [{diag.severity}] {diag.code}{where}{entry}: "
                     f"{diag.message}")
    return "\n".join(lines)


def run(argv: list[str] | None = None) -> int:
    parser = build_parser()
    args = parser.parse_args(argv)

    if args.make_sample is not None:
        return make_sample(args.make_sample)

    try:
        summary = analyze(args.input, strict=args.strict)
    except InputError as exc:
        print(f"sidxcheck: {exc}", file=sys.stderr)
        return EXIT_USAGE
    except model.CorruptContainerError as exc:
        print(f"sidxcheck: strict mode aborted: {exc}", file=sys.stderr)
        return EXIT_CORRUPT

    if args.json_output:
        print(json.dumps(summary.to_dict(), indent=2, sort_keys=True))
    else:
        print(_render_text(summary))

    if summary.fatal or not summary.sidx or not summary.sample_table:
        return EXIT_CORRUPT
    if args.strict and summary.has_errors:
        return EXIT_CORRUPT
    return EXIT_OK


def main() -> None:
    sys.exit(run())


if __name__ == "__main__":
    main()
