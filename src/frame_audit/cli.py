"""Command-line entry point: argument parsing, batching and exit codes.

Exit codes are the tool's contract with build scripts:

* ``0`` the scan completed for every requested input;
* ``1`` an input was missing, unreadable, had no video stream or could not be
  decoded;
* ``2`` the command line was invalid;
* ``3`` the requested output file could not be written.
"""

from __future__ import annotations

import argparse
import sys
from pathlib import Path
from typing import Sequence

from . import __version__, audit
from .audit import audit_file
from .errors import (
    EXIT_INPUT,
    EXIT_OK,
    EXIT_OUTPUT,
    EXIT_USAGE,
    AuditError,
)
from .model import AuditResult, BatchReport, Policy, RepairKind
from .probe import VIDEO_EXTENSIONS, is_probable_video
from .sinks import write_json_lines, write_json_stream, write_table

__all__ = ["main", "build_parser"]


#: Non-video suffixes that appear inside media folders and are always skipped
#: with a recorded reason rather than a decode attempt.
_SKIP_SUFFIXES: dict[str, str] = {
    ".txt": "not a video file",
    ".json": "not a video file",
    ".jsonl": "not a video file",
    ".csv": "not a video file",
    ".md": "not a video file",
    ".xml": "not a video file",
    ".srt": "not a video file",
    ".vtt": "not a video file",
    ".log": "not a video file",
    ".py": "not a video file",
    ".jpg": "still image, not a video",
    ".jpeg": "still image, not a video",
    ".png": "still image, not a video",
    ".gif": "still image, not a video",
    ".wav": "audio-only file",
    ".mp3": "audio-only file",
    ".aac": "audio-only file",
    ".flac": "audio-only file",
    ".m4a": "audio-only file",
}


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="frame-audit",
        description=(
            "Audit the decoded frame timeline of video files: reordering, "
            "timestamp repair and per-file JSON reports."
        ),
    )
    parser.add_argument(
        "--version", action="version", version=f"frame-audit {__version__}"
    )
    sub = parser.add_subparsers(dest="command", metavar="COMMAND")

    audit_cmd = sub.add_parser(
        "audit", help="audit one file or every video in a directory"
    )
    audit_cmd.add_argument(
        "paths", nargs="+", metavar="PATH", help="video file(s) or directories"
    )
    audit_cmd.add_argument(
        "--json-out",
        metavar="FILE",
        help="write one JSON object per file to FILE (JSON Lines)",
    )
    audit_cmd.add_argument(
        "--json",
        action="store_true",
        help="print the JSON Lines document to stdout instead of the table",
    )
    audit_cmd.add_argument(
        "--max-frames",
        type=int,
        metavar="N",
        help="decode at most N frames per file; results are marked truncated",
    )
    audit_cmd.add_argument(
        "--on-timestamp-error",
        choices=[policy.value for policy in Policy],
        default=Policy.REPAIR.value,
        metavar="{repair,report}",
        help="repair anomalous timestamps (default) or only report them",
    )
    audit_cmd.add_argument(
        "--quiet",
        action="store_true",
        help="suppress the terminal table (useful with --json-out)",
    )
    return parser


def _usage_error(parser: argparse.ArgumentParser, message: str) -> int:
    parser.error(message)
    return EXIT_USAGE  # pragma: no cover - argparse exits


def _collect_targets(
    inputs: Sequence[str],
) -> tuple[list[Path], list[AuditResult]]:
    """Expand files and directories into audit targets.

    Directories are walked recursively and sorted by relative path so runs are
    reproducible.  Files with a known non-video suffix are turned into skipped
    results that carry the reason, never silently dropped.
    """
    targets: list[Path] = []
    skipped: list[AuditResult] = []

    for raw in inputs:
        path = Path(raw)
        if path.is_dir():
            discovered = sorted(
                (entry for entry in path.rglob("*") if entry.is_file()),
                key=lambda entry: entry.relative_to(path).as_posix(),
            )
            for entry in discovered:
                relative = entry.relative_to(path).as_posix()
                reason = _SKIP_SUFFIXES.get(entry.suffix.lower())
                if reason is None and not is_probable_video(entry):
                    reason = (
                        f"unsupported extension '{entry.suffix or '(none)'}' "
                        f"(accepted: {', '.join(sorted(VIDEO_EXTENSIONS))})"
                    )
                if reason is not None:
                    skipped.append(
                        AuditResult(
                            path=str(entry),
                            display_path=relative,
                            stream=None,
                            skipped_reason=reason,
                        )
                    )
                    continue
                targets.append(entry)
            audit.DISPLAY_ROOT = path
        else:
            targets.append(path)

    return targets, skipped


def _display_name(path: Path, root: Path | None) -> str:
    if root is not None:
        try:
            return path.resolve().relative_to(root.resolve()).as_posix()
        except ValueError:
            pass
    return str(path)


def _run_audit(args: argparse.Namespace, parser: argparse.ArgumentParser) -> int:
    if args.max_frames is not None and args.max_frames <= 0:
        _usage_error(parser, "--max-frames must be a positive integer")

    targets, skipped = _collect_targets(args.paths)
    policy = Policy(args.on_timestamp_error)

    results: list[AuditResult] = []
    for path in targets:
        results.append(audit_file(path, max_frames=args.max_frames, policy=policy))
    results.extend(skipped)

    audited = tuple(results)
    report = BatchReport(results=audited)

    if args.json or args.json_out:
        if args.json:
            try:
                write_json_stream(audited, sys.stdout)
            except AuditError as exc:
                _report_error(exc)
                return exc.exit_code
    if not args.quiet and not args.json:
        try:
            write_table(audited, sys.stdout)
        except AuditError as exc:
            _report_error(exc)
            return exc.exit_code

    if args.json_out:
        try:
            write_json_lines(audited, args.json_out)
        except AuditError as exc:
            _report_error(exc)
            return exc.exit_code

    if report.failures:
        return EXIT_INPUT
    return EXIT_OK


def _report_error(error: AuditError) -> None:
    sys.stderr.write(f"frame-audit: {error.message}\n")


def main(argv: Sequence[str] | None = None) -> int:
    """Parse ``argv`` and run the requested command, returning an exit code."""
    parser = build_parser()
    try:
        args = parser.parse_args(argv)
    except SystemExit as exc:  # argparse exits on --help/--version and on errors
        code = exc.code if isinstance(exc.code, int) else EXIT_USAGE
        return code

    if args.command is None:
        parser.print_help(sys.stderr)
        return EXIT_USAGE

    if args.command == "audit":
        try:
            return _run_audit(args, parser)
        except AuditError as exc:
            _report_error(exc)
            return exc.exit_code
        except KeyboardInterrupt:
            sys.stderr.write("frame-audit: interrupted\n")
            return EXIT_INPUT

    return EXIT_USAGE


if __name__ == "__main__":  # pragma: no cover
    raise SystemExit(main())
