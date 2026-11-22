"""Terminal entry point: demo, check and rebuild subcommands.

Everything runs in the terminal: arguments in, JSON evidence on stdout
(optionally also written to a file). Exit codes: 0 success, 2 input
validation failure, 3 argument or I/O error.
"""
from __future__ import annotations

import argparse
import json
import os
import sys

from . import fixture
from .model import ValidationReport
from .rebuild import rebuild_file
from .validate import validate_file

EXIT_OK = 0
EXIT_INVALID = 2
EXIT_USAGE = 3


class _Parser(argparse.ArgumentParser):
    def error(self, message: str) -> None:  # argparse default is exit 2
        self.print_usage(sys.stderr)
        print(f"mediapack: error: {message}", file=sys.stderr)
        raise SystemExit(EXIT_USAGE)


def build_parser() -> argparse.ArgumentParser:
    parser = _Parser(
        prog="mediapack",
        description="MP4 fragment index rebuild and sample-table validation",
    )
    sub = parser.add_subparsers(dest="command", required=True)
    demo = sub.add_parser("demo", help="generate a fixture and run the pipeline")
    demo.add_argument("--workdir", required=True)
    demo.add_argument("--target-seconds", type=float, default=2.0)
    demo.add_argument("--stbl-offset-delta", type=int, default=0)
    demo.add_argument("--json", help="also write the JSON report to this path")
    check = sub.add_parser("check", help="validate an existing file")
    check.add_argument("file")
    check.add_argument("--json", help="also write the JSON report to this path")
    rebuild = sub.add_parser("rebuild", help="rebuild the fragment index in place")
    rebuild.add_argument("file")
    rebuild.add_argument("--target-seconds", type=float, default=2.0)
    rebuild.add_argument("--json", help="also write the JSON report to this path")
    return parser


def _seconds(units: int, timescale: int) -> int | float | None:
    if not timescale:
        return None
    return units // timescale if units % timescale == 0 else units / timescale


def _report_json(report: ValidationReport) -> dict:
    return {
        "file": report.path,
        "validation": "PASS" if report.ok else "FAIL",
        "sample_count": report.sample_count,
        "timescale": report.timescale,
        "index_offset_check": report.index_offset_check,
        "diagnostics": [
            {"severity": d.severity, "code": d.code, "message": d.message}
            for d in report.diagnostics
        ],
    }


def _emit(out: dict, json_path: str | None) -> None:
    text = json.dumps(out, indent=2)
    print(text)
    if json_path:
        with open(json_path, "w", encoding="utf-8") as fh:
            fh.write(text + "\n")


def _exit_for(report: ValidationReport) -> int:
    if any(d.code == "io-error" for d in report.diagnostics):
        return EXIT_USAGE
    return EXIT_OK if report.ok else EXIT_INVALID


def _missing(path: str, json_path: str | None) -> int:
    _emit({"file": path, "validation": "FAIL",
           "error": f"no such file: {path}"}, json_path)
    return EXIT_USAGE


def _cmd_check(args: argparse.Namespace) -> int:
    if not os.path.isfile(args.file):
        return _missing(args.file, args.json)
    report = validate_file(args.file)
    _emit(_report_json(report), args.json)
    return _exit_for(report)


def _rebuild_json(result, timescale: int) -> dict:
    out = _report_json(result.report)
    out.update(
        {
            "rebuilt": result.ok,
            "rebuilt_boundaries_samples": [f.end_sample for f in result.fragments],
            "boundary_durations_seconds": [
                _seconds(f.duration, timescale) for f in result.fragments
            ],
            "boundary_delta_seconds": [
                _seconds(f.delta, timescale) for f in result.fragments
            ],
            "reference_count": result.reference_count,
            "message": result.message,
        }
    )
    return out


def _cmd_rebuild(args: argparse.Namespace) -> int:
    if not os.path.isfile(args.file):
        return _missing(args.file, args.json)
    result = rebuild_file(args.file, args.target_seconds)
    _emit(_rebuild_json(result, result.report.timescale), args.json)
    return EXIT_OK if result.ok else _exit_for(result.report)


def _cmd_demo(args: argparse.Namespace) -> int:
    os.makedirs(args.workdir, exist_ok=True)
    target = os.path.join(args.workdir, "input.mp4")
    fixture.build_fixture(target, stbl_offset_delta=args.stbl_offset_delta)
    first = validate_file(target)
    if not first.ok:
        out = _report_json(first)
        out["workdir"] = args.workdir
        _emit(out, args.json)
        return _exit_for(first)
    result = rebuild_file(target, args.target_seconds)
    out = _rebuild_json(result, result.report.timescale)
    out["workdir"] = args.workdir
    _emit(out, args.json)
    return EXIT_OK if result.ok else _exit_for(result.report)


def main(argv: list[str] | None = None) -> int:
    args = build_parser().parse_args(argv)
    try:
        if args.command == "demo":
            return _cmd_demo(args)
        if args.command == "check":
            return _cmd_check(args)
        if args.command == "rebuild":
            return _cmd_rebuild(args)
    except OSError as exc:
        print(json.dumps({"error": str(exc)}))
        return EXIT_USAGE
    return EXIT_USAGE


if __name__ == "__main__":
    raise SystemExit(main())
