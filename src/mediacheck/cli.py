"""Command line entry point: argument parsing, orchestration and exit codes.

Exit codes:
  0  scan completed, every file parsed and no consistency mismatch
  1  scan completed but at least one file failed or mismatch was found
  2  invalid arguments or unusable input path (nothing was scanned)
  3  database or JSON report could not be written
"""

from __future__ import annotations

import argparse
import datetime as _dt
import os
import sys

from . import __version__
from .compare import compare_results
from .container_probe import probe_many
from .report import ReportError, build_report, print_terminal, write_json
from .store import StorageError, connect, history, save_scan

EXIT_OK = 0
EXIT_FINDINGS = 1
EXIT_USAGE = 2
EXIT_IO = 3

MEDIA_EXTENSIONS = {".mp4", ".m4v", ".mov", ".mkv", ".webm", ".mk3d"}


def collect_inputs(inputs: list[str]) -> tuple[list[str], str | None]:
    """Expand directories to supported media files and validate paths."""
    files: list[str] = []
    for raw in inputs:
        if not os.path.exists(raw):
            return [], f"input path does not exist: {raw}"
        if os.path.isdir(raw):
            for name in sorted(os.listdir(raw)):
                candidate = os.path.join(raw, name)
                if (os.path.isfile(candidate)
                        and os.path.splitext(name)[1].lower()
                        in MEDIA_EXTENSIONS):
                    files.append(candidate)
        elif os.path.isfile(raw):
            files.append(raw)
        else:
            return [], f"input path is neither file nor directory: {raw}"
    if not files:
        return [], "no supported media files (.mp4/.mov/.mkv/.webm) found"
    return files, None


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="mediacheck",
        description="Offline MP4/Matroska metadata probe and track check")
    parser.add_argument("--version", action="version",
                        version=f"mediacheck {__version__}")
    sub = parser.add_subparsers(dest="command", required=True)

    scan = sub.add_parser("scan", help="probe and compare media files")
    scan.add_argument("--inputs", nargs="+", required=True,
                      help="media files or directories containing them")
    scan.add_argument("--reference",
                      help="delivery reference label stored with the scan")
    scan.add_argument("--duration-tolerance-ms", type=int, default=0,
                      help="duration deviation still considered consistent")
    scan.add_argument("--db", required=True, help="sqlite result database")
    scan.add_argument("--json", dest="json_path",
                      help="optional JSON report output path")

    hist = sub.add_parser("history", help="show previous scans")
    hist.add_argument("--db", required=True, help="sqlite result database")
    hist.add_argument("--limit", type=int, default=20)
    return parser


def _run_history(args: argparse.Namespace) -> int:
    try:
        conn = connect(args.db)
        rows = history(conn, args.limit)
    except StorageError as exc:
        print(f"error: {exc}", file=sys.stderr)
        return EXIT_IO
    for row in rows:
        print(
            f"scan={row['id']} at={row['started_at']} "
            f"reference={row['reference']} files={row['files_total']} "
            f"ok={row['files_ok']} failed={row['files_failed']} "
            f"tracks={row['track_count']} "
            f"max_delta_ms={row['max_duration_delta_ms']} "
            f"mismatches={row['mismatch_count']}")
    return EXIT_OK


def _run_scan(args: argparse.Namespace) -> int:
    if args.duration_tolerance_ms < 0:
        print("error: --duration-tolerance-ms must be >= 0", file=sys.stderr)
        return EXIT_USAGE
    files, error = collect_inputs(args.inputs)
    if error:
        print(f"error: {error}", file=sys.stderr)
        return EXIT_USAGE
    if args.json_path:
        # Validate the destination up front; terminal output still happens.
        try:
            from .report import ensure_writable
            ensure_writable(args.json_path, files)
        except ReportError as exc:
            print(f"error: {exc}", file=sys.stderr)
            return EXIT_IO

    results = probe_many(files)
    comparison = compare_results(results, args.duration_tolerance_ms)
    report = build_report(results, comparison,
                          args.duration_tolerance_ms)
    print_terminal(report)

    started_at = _dt.datetime.now(_dt.timezone.utc).isoformat()
    try:
        conn = connect(args.db)
        record = save_scan(
            conn, started_at=started_at, reference=args.reference,
            tolerance_ms=args.duration_tolerance_ms, results=results,
            max_duration_delta_ms=comparison.max_duration_delta_ms,
            mismatch_count=len(comparison.mismatches))
    except StorageError as exc:
        print(f"error: {exc}", file=sys.stderr)
        return EXIT_IO
    print(f"stored scan_id={record.scan_id} in {args.db}")

    if args.json_path:
        try:
            write_json(args.json_path, report, files)
        except ReportError as exc:
            print(f"error: {exc}", file=sys.stderr)
            return EXIT_IO
        print(f"json report written to {args.json_path}")

    has_findings = report["files_failed"] > 0 or report["mismatch_count"] > 0
    return EXIT_FINDINGS if has_findings else EXIT_OK


def main(argv: list[str] | None = None) -> int:
    parser = build_parser()
    args = parser.parse_args(argv)
    if args.command == "history":
        return _run_history(args)
    return _run_scan(args)


if __name__ == "__main__":
    raise SystemExit(main())
