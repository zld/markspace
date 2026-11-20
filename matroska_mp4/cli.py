"""Command-line interface.  Every run reports machine-readable JSON."""

from __future__ import annotations

import argparse
import json
import os
import sys

from . import __version__, fixture
from .ebml import EBMLError
from .export_preserve import PreserveError, run_preserve
from .export_rewrite import run_rewrite
from .matroska import LimitError, UnsupportedFormat, parse_matroska
from .mp4_audit import AuditError
from .timeline import TimebaseOverflow, TimelineError

EXIT_OK = 0
EXIT_ARGUMENTS = 2
EXIT_EBML = 3
EXIT_UNSUPPORTED = 4
EXIT_WRITE = 5


class _Parser(argparse.ArgumentParser):
    """Argparse that still emits JSON and uses exit code 2 for bad usage."""

    def error(self, message: str):  # noqa: D401 - argparse hook
        _emit({"ok": False, "kind": "arguments", "error": message,
               "exit_code": EXIT_ARGUMENTS}, stream=sys.stderr)
        raise SystemExit(EXIT_ARGUMENTS)


def _emit(payload: dict, stream=None) -> None:
    stream = stream or sys.stdout
    stream.write(json.dumps(payload, indent=2, sort_keys=False) + "\n")
    stream.flush()


def _positive_int(text: str) -> int:
    try:
        value = int(text, 10)
    except ValueError as exc:
        raise argparse.ArgumentTypeError(f"invalid integer: {text}") from exc
    if value <= 0:
        raise argparse.ArgumentTypeError("timescale must be positive")
    return value


def build_parser() -> argparse.ArgumentParser:
    parser = _Parser(prog="python -m matroska_mp4",
                     description="Remux H.264/AAC Matroska to MP4 without "
                                 "re-encoding.")
    parser.add_argument("--version", action="version", version=__version__)
    sub = parser.add_subparsers(dest="command", required=True)
    generate = sub.add_parser("fixture", help="generate deterministic .mkv fixtures")
    generate.add_argument("--out-dir", required=True,
                          help="directory that receives reordered.mkv/monotonic.mkv")
    remux = sub.add_parser("remux", help="remux Matroska into an MP4")
    remux.add_argument("--input", required=True, help="source .mkv")
    remux.add_argument("--output", required=True, help="target .mp4")
    remux.add_argument("--mode", choices=("preserve", "rewrite"), default="rewrite")
    remux.add_argument("--timescale", type=_positive_int, default=60000,
                       help="MP4 media timescale (ticks per second)")
    remux.add_argument("--report", choices=("json", "text"), default="json")
    return parser


def _error_kind(exc: BaseException) -> tuple[str, int]:
    if isinstance(exc, EBMLError):
        return "ebml", EXIT_EBML
    if isinstance(exc, LimitError):
        return "limit", EXIT_UNSUPPORTED
    if isinstance(exc, UnsupportedFormat):
        return "unsupported", EXIT_UNSUPPORTED
    if isinstance(exc, PreserveError):
        return "timeline", EXIT_UNSUPPORTED
    if isinstance(exc, (TimelineError, TimebaseOverflow)):
        return "timeline", EXIT_UNSUPPORTED
    if isinstance(exc, (AuditError, OSError)):
        return "write", EXIT_WRITE
    return "internal", EXIT_WRITE


def _run_remux(args) -> int:
    try:
        return _remux(args)
    except KeyboardInterrupt:
        _emit({"ok": False, "kind": "interrupted", "error": "interrupted",
               "exit_code": EXIT_WRITE}, stream=sys.stderr)
        return EXIT_WRITE


def _remux(args) -> int:
    if not os.path.isfile(args.input):
        _emit({"ok": False, "kind": "arguments",
               "error": f"input not found: {args.input}",
               "exit_code": EXIT_ARGUMENTS}, stream=sys.stderr)
        return EXIT_ARGUMENTS
    if os.path.abspath(args.input) == os.path.abspath(args.output):
        _emit({"ok": False, "kind": "arguments",
               "error": "input and output must be different files",
               "exit_code": EXIT_ARGUMENTS}, stream=sys.stderr)
        return EXIT_ARGUMENTS
    if not 1 <= args.timescale <= 1_000_000_000:
        _emit({"ok": False, "kind": "arguments",
               "error": "timescale must be between 1 and 1000000000",
               "exit_code": EXIT_ARGUMENTS}, stream=sys.stderr)
        return EXIT_ARGUMENTS
    try:
        mkv = parse_matroska(args.input)
        runner = run_preserve if args.mode == "preserve" else run_rewrite
        result = runner(mkv, args.timescale, args.output)
    except (EBMLError, LimitError, UnsupportedFormat, PreserveError,
            TimelineError, TimebaseOverflow, AuditError, OSError, ValueError) as exc:
        kind, code = _error_kind(exc)
        offset = getattr(exc, "offset", None)
        payload = {"ok": False, "kind": kind, "error": str(exc),
                   "exit_code": code}
        if offset is not None:
            payload["offset"] = offset
        _emit(payload, stream=sys.stderr)
        return code
    report = result.report()
    report["ok"] = True
    report["exit_code"] = EXIT_OK
    report["input"] = args.input
    if args.report == "text":
        print(f"mode={report['mode']} samples={report['sample_count']} "
              f"tracks={len(report['track_map'])} "
              f"ctts_tracks={report['ctts_tracks']} -> {args.output}")
    else:
        _emit(report)
    return EXIT_OK


def _run_fixture(args) -> int:
    try:
        paths = fixture.generate(args.out_dir)
    except OSError as exc:
        _emit({"ok": False, "kind": "write", "error": str(exc),
               "exit_code": EXIT_WRITE}, stream=sys.stderr)
        return EXIT_WRITE
    _emit({"ok": True, "exit_code": EXIT_OK, "fixtures": paths,
           "description": {
               "reordered": "unknown-size Segment/Cluster, B-frame BlockGroups, "
                            "all four lacing modes",
               "monotonic": "known sizes, nonzero start, EBML/fixed/Xiph lacing"}})
    return EXIT_OK


def main(argv: list[str] | None = None) -> int:
    parser = build_parser()
    args = parser.parse_args(argv)
    if args.command == "fixture":
        return _run_fixture(args)
    return _run_remux(args)


if __name__ == "__main__":
    raise SystemExit(main())
