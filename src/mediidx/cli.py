"""Command line entry point: fixture, rebuild and validate.

Exit codes are part of the contract:

* ``0`` success
* ``1`` bad arguments or a local file read/write error
* ``2`` input structure or consistency error

stdout always carries one JSON document for machine use; human diagnostics go
to stderr.  A failed rebuild never leaves an output file behind.
"""

from __future__ import annotations

import argparse
import json
import os
import sys

from .errors import MediidxError, UsageError
from .fixture import write_fixture
from .rebuild import rebuild
from .validator import validate_bytes

EXIT_OK = 0
EXIT_USAGE = 1
EXIT_INVALID = 2


class _Parser(argparse.ArgumentParser):
    """ArgumentParser that reports usage problems as exit code 1, not 2."""

    def error(self, message: str) -> None:  # type: ignore[override]
        self.print_usage(sys.stderr)
        raise UsageError(message)


def build_parser() -> argparse.ArgumentParser:
    parser = _Parser(
        prog="mediidx",
        description=(
            "Rebuild an MP4 sidx from its sample table and validate the result "
            "with an independent parser."
        ),
    )
    sub = parser.add_subparsers(dest="command", required=True)

    fixture = sub.add_parser("fixture", help="write the deterministic sample MP4")
    fixture.add_argument("--out", required=True, help="output MP4 path")
    fixture.add_argument(
        "--json", action="store_true", help="accepted for symmetry; output is JSON"
    )

    reb = sub.add_parser("rebuild", help="derive segments and rewrite the sidx")
    reb.add_argument("input", help="source MP4 (never modified)")
    reb.add_argument("--out", required=True, help="new MP4 path")
    reb.add_argument(
        "--target-duration",
        required=True,
        help="segment packing threshold in sidx timescale ticks",
    )
    reb.add_argument(
        "--timescale",
        type=int,
        default=None,
        help="sidx timescale; required when the input has no readable sidx",
    )
    reb.add_argument(
        "--earliest-presentation-time",
        type=int,
        default=None,
        help="preserved when inferable; otherwise supply it explicitly",
    )
    reb.add_argument(
        "--sidx-version",
        type=int,
        choices=(0, 1),
        default=None,
        help="sidx version; defaults to the existing index version",
    )
    reb.add_argument(
        "--json", action="store_true", help="accepted for symmetry; output is JSON"
    )

    val = sub.add_parser("validate", help="validate an existing sidx against stbl")
    val.add_argument("input", help="MP4 to validate")
    val.add_argument(
        "--json", action="store_true", help="accepted for symmetry; output is JSON"
    )
    val.add_argument(
        "--strict",
        action="store_true",
        help="treat offset mismatches as a failure (default behaviour)",
    )
    return parser


def _emit(payload: dict) -> None:
    json.dump(payload, sys.stdout, indent=2, sort_keys=True)
    sys.stdout.write("\n")


def _diagnose(message: str) -> None:
    print(f"mediidx: {message}", file=sys.stderr)


def _read_input(path: str) -> bytes:
    if not os.path.exists(path):
        raise UsageError(f"input file does not exist: {path}")
    if not os.path.isfile(path):
        raise UsageError(f"input path is not a regular file: {path}")
    try:
        with open(path, "rb") as handle:
            return handle.read()
    except OSError as exc:
        raise UsageError(f"cannot read {path}: {exc}") from exc


def run_fixture(args: argparse.Namespace) -> int:
    parent = os.path.dirname(os.path.abspath(args.out))
    if not os.path.isdir(parent):
        raise UsageError(f"output directory does not exist: {parent}")
    try:
        size = write_fixture(args.out)
    except OSError as exc:
        raise UsageError(f"cannot write {args.out}: {exc}") from exc
    _emit(
        {
            "command": "fixture",
            "output": args.out,
            "bytes": size,
            "sample_entries": 6,
            "timescale": 1,
        }
    )
    return EXIT_OK


def run_rebuild(args: argparse.Namespace) -> int:
    result = rebuild(
        args.input,
        args.out,
        target_duration=args.target_duration,
        timescale=args.timescale,
        earliest_presentation_time=args.earliest_presentation_time,
        version=args.sidx_version,
    )
    _emit(result.to_dict())
    for warning in result.warnings:
        _diagnose(f"warning: {warning}")
    return EXIT_OK


def run_validate(args: argparse.Namespace) -> int:
    data = _read_input(args.input)
    report = validate_bytes(data, source=args.input)
    _emit(report.to_dict())
    for error in report.errors:
        _diagnose(error)
    return EXIT_OK if report.ok else EXIT_INVALID


_HANDLERS = {
    "fixture": run_fixture,
    "rebuild": run_rebuild,
    "validate": run_validate,
}


def main(argv: list[str] | None = None) -> int:
    parser = build_parser()
    try:
        args = parser.parse_args(argv)
    except UsageError as exc:
        _diagnose(exc.describe())
        return EXIT_USAGE
    except SystemExit as exc:  # --help, --version
        code = exc.code if isinstance(exc.code, int) else EXIT_USAGE
        return code
    try:
        return _HANDLERS[args.command](args)
    except UsageError as exc:
        _diagnose(exc.describe())
        return EXIT_USAGE
    except MediidxError as exc:
        _diagnose(exc.describe())
        return EXIT_INVALID
    except OSError as exc:
        _diagnose(f"file operation failed: {exc}")
        return EXIT_USAGE
    except BrokenPipeError:
        return EXIT_USAGE


if __name__ == "__main__":  # pragma: no cover
    raise SystemExit(main())
