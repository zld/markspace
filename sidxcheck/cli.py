"""Command line entry point for sidxcheck (demo / check / rebuild)."""

from __future__ import annotations

import argparse
import sys
from pathlib import Path

from .errors import SidxCheckError
from .fixtures import (
    DEFAULT_SAMPLE_DURATION,
    DEFAULT_SAMPLES,
    DEFAULT_SEGMENT_TICKS,
    DEFAULT_TIMESCALE,
    MovieSpec,
    write_demo,
)
from .report import render_check, render_rebuild
from .rewrite import rewrite_with_sidx
from .segments import plan_segments
from .verify import check_file

EXIT_OK = 0
EXIT_INVALID = 2
EXIT_IO = 3


def _positive_int(value: str) -> int:
    try:
        number = int(value)
    except ValueError:
        raise argparse.ArgumentTypeError(f"{value!r} is not an integer") from None
    if number <= 0:
        raise argparse.ArgumentTypeError("value must be positive")
    return number


def _non_negative_int(value: str) -> int:
    try:
        number = int(value)
    except ValueError:
        raise argparse.ArgumentTypeError(f"{value!r} is not an integer") from None
    if number < 0:
        raise argparse.ArgumentTypeError("value must be zero or greater")
    return number


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="sidxcheck",
        description="Rebuild and verify SIDX indexes for classic (non-fragmented) MP4.",
    )
    subparsers = parser.add_subparsers(dest="command", required=True)

    demo = subparsers.add_parser(
        "demo", help="generate fixtures, rebuild the index and verify it"
    )
    demo.add_argument("--dir", type=Path, default=Path(".tmp/sidx"), help="fixture directory")
    demo.add_argument("--samples", type=_positive_int, default=DEFAULT_SAMPLES)
    demo.add_argument("--timescale", type=_positive_int, default=DEFAULT_TIMESCALE)
    demo.add_argument("--sample-duration", type=_positive_int, default=DEFAULT_SAMPLE_DURATION)
    demo.add_argument("--segment-ticks", type=_positive_int, default=DEFAULT_SEGMENT_TICKS)
    demo.add_argument("--max-drift-ticks", type=_non_negative_int, default=0)
    demo.add_argument("--track-id", type=_positive_int, default=1)
    demo.add_argument("--json", action="store_true", help="emit JSON instead of text")

    check = subparsers.add_parser("check", help="validate an existing file and its SIDX")
    check.add_argument("path", type=Path)
    check.add_argument("--track-id", type=_positive_int, default=1)
    check.add_argument("--json", action="store_true")
    check.add_argument(
        "--allow-missing-index",
        action="store_true",
        help="do not fail when the file has no SIDX box",
    )

    rebuild = subparsers.add_parser("rebuild", help="write a copy with a fresh SIDX")
    rebuild.add_argument("path", type=Path)
    rebuild.add_argument("--output", "-o", type=Path, required=True)
    rebuild.add_argument("--track-id", type=_positive_int, default=1)
    rebuild.add_argument("--segment-ticks", type=_positive_int, required=True)
    rebuild.add_argument("--max-drift-ticks", type=_non_negative_int, default=0)
    rebuild.add_argument("--json", action="store_true")
    return parser


def _check_outcome(result) -> int:
    return EXIT_OK if result.ok else EXIT_INVALID


def run_demo(args: argparse.Namespace, stdout=sys.stdout, stderr=sys.stderr) -> int:
    directory: Path = args.dir
    spec = MovieSpec(
        sample_count=args.samples,
        timescale=args.timescale,
        sample_duration=args.sample_duration,
    )
    try:
        source, destination = write_demo(directory, spec)
    except OSError as exc:
        print(f"demo: cannot write fixtures under {directory}: {exc}", file=stderr)
        return EXIT_IO
    try:
        source_check = check_file(
            source, track_id=args.track_id, require_index=False
        )
    except SidxCheckError as exc:
        print(f"demo: generated fixture is invalid: {exc}", file=stderr)
        return exc.exit_code
    try:
        result = rewrite_with_sidx(
            source,
            destination,
            track_id=args.track_id,
            segment_ticks=args.segment_ticks,
            max_drift_ticks=args.max_drift_ticks,
        )
        rebuilt_check = check_file(destination, track_id=args.track_id)
    except SidxCheckError as exc:
        print(f"demo: {exc}", file=stderr)
        return exc.exit_code
    plan = plan_segments(
        source_check.sample_map,
        target_ticks=args.segment_ticks,
        max_drift_ticks=args.max_drift_ticks,
    )
    output = render_rebuild(
        result,
        plan=plan,
        source_check=source_check,
        rebuilt_check=rebuilt_check,
        as_json=args.json,
        demo=True,
    )
    print(output, file=stdout)
    if not rebuilt_check.ok:
        return EXIT_INVALID
    return EXIT_OK


def run_check(args: argparse.Namespace, stdout=sys.stdout, stderr=sys.stderr) -> int:
    try:
        result = check_file(
            args.path,
            track_id=args.track_id,
            require_index=not args.allow_missing_index,
        )
    except SidxCheckError as exc:
        if args.json:
            import json

            print(
                json.dumps(
                    {
                        "file": str(args.path),
                        "ok": False,
                        "error": str(exc),
                        "exit_code": exc.exit_code,
                    },
                    indent=2,
                ),
                file=stdout,
            )
        else:
            print(f"check failed: {exc}", file=stderr)
        return exc.exit_code
    print(render_check(result, as_json=args.json), file=stdout)
    return _check_outcome(result)


def run_rebuild(args: argparse.Namespace, stdout=sys.stdout, stderr=sys.stderr) -> int:
    try:
        source_check = check_file(
            args.path, track_id=args.track_id, require_index=False
        )
    except SidxCheckError as exc:
        print(f"rebuild: source rejected: {exc}", file=stderr)
        return exc.exit_code
    plan = plan_segments(
        source_check.sample_map,
        target_ticks=args.segment_ticks,
        max_drift_ticks=args.max_drift_ticks,
    )
    try:
        result = rewrite_with_sidx(
            args.path,
            args.output,
            track_id=args.track_id,
            segment_ticks=args.segment_ticks,
            max_drift_ticks=args.max_drift_ticks,
        )
    except SidxCheckError as exc:
        print(f"rebuild: {exc}", file=stderr)
        return exc.exit_code
    try:
        rebuilt_check = check_file(args.output, track_id=args.track_id)
    except SidxCheckError as exc:
        print(f"rebuild: rewritten file failed verification: {exc}", file=stderr)
        return exc.exit_code
    print(
        render_rebuild(
            result,
            plan=plan,
            source_check=source_check,
            rebuilt_check=rebuilt_check,
            as_json=args.json,
        ),
        file=stdout,
    )
    return EXIT_OK if rebuilt_check.ok else EXIT_INVALID


def main(argv: list[str] | None = None) -> int:
    parser = build_parser()
    args = parser.parse_args(argv)
    handlers = {"demo": run_demo, "check": run_check, "rebuild": run_rebuild}
    handler = handlers[args.command]
    try:
        return handler(args, stdout=sys.stdout, stderr=sys.stderr)
    except SidxCheckError as exc:
        print(f"error: {exc}", file=sys.stderr)
        return exc.exit_code
    except BrokenPipeError:  # pragma: no cover - pipe closed by caller
        return EXIT_IO


if __name__ == "__main__":  # pragma: no cover
    raise SystemExit(main())
