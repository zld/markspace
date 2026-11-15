"""Terminal interface: demo / validate / resolve subcommands.

Exit codes: 0 success, 2 input/format error, 3 resource limit,
4 OTIO export failure. Errors are printed to stderr as
``error[<code>]: <message>`` so scripts can recognize them.
"""
from __future__ import annotations

import argparse
import json
import sys

from . import exchange, fixtures
from .detect import find_conflicts
from .errors import EXIT_OK, ClipweaveError, FormatError
from .model import DEFAULT_MAX_CLIPS
from .otio_render import write_otio
from .pipeline import conflict_entries, run_pipeline


def _parse_versions(raw: str) -> list[int]:
    versions = []
    for token in raw.split(","):
        token = token.strip().lower()
        if not token:
            continue
        if token.startswith("v"):
            token = token[1:]
        if token not in ("1", "2"):
            raise FormatError(f"unknown exchange version: {token!r}")
        versions.append(int(token))
    return versions


def _emit_report(report: dict, dest: str | None) -> None:
    if dest is None:
        return
    text = json.dumps(report, indent=2, ensure_ascii=False)
    if dest == "-":
        print(text)
    else:
        try:
            with open(dest, "w", encoding="utf-8") as fh:
                fh.write(text + "\n")
        except OSError as exc:
            raise FormatError(f"cannot write report to {dest}: {exc}")


def _print_summary(report: dict, stream) -> None:
    lines = [
        f"timeline={report['timeline']} fps={report['fps']} "
        f"clips={report['clip_count']}",
        f"conflict_count={report['conflict_count']}",
        f"resolved_intervals=[{','.join(report['resolved_intervals'])}]",
        f"total_duration_before_frames={report['total_duration_before_frames']}"
        f" total_duration_after_frames={report['total_duration_after_frames']}",
        f"duration_delta_frames={report['duration_delta_frames']}"
        f" duration_deviation_frames={report['duration_deviation_frames']}",
        f"duration_deviation_seconds={report['duration_deviation_seconds']}",
    ]
    for line in lines:
        print(line, file=stream)


def _finish(report: dict, args) -> int:
    _emit_report(report, args.report_json)
    summary_stream = sys.stderr if args.report_json == "-" else sys.stdout
    _print_summary(report, summary_stream)
    return EXIT_OK


def _cmd_demo(args) -> int:
    timeline = fixtures.demo_timeline()
    roundtrips = {}
    for version in _parse_versions(args.roundtrip):
        restored = exchange.roundtrip(timeline, version)
        if restored != timeline:
            raise FormatError(f"roundtrip v{version} changed the model")
        roundtrips[f"v{version}"] = "ok"
    result = run_pipeline(timeline, max_clips=args.max_clips)
    result.report["roundtrip"] = roundtrips
    if args.otio_out:
        write_otio(result.resolved, args.otio_out)
        result.report["otio_out"] = args.otio_out
    return _finish(result.report, args)


def _cmd_validate(args) -> int:
    timeline = exchange.load_file(args.input, max_clips=args.max_clips)
    conflicts = find_conflicts(timeline)
    report = {
        "timeline": timeline.name,
        "input": args.input,
        "valid": True,
        "fps": str(timeline.fps),
        "clip_count": timeline.clip_count,
        "conflict_count": len(conflicts),
        "conflicts": conflict_entries(conflicts, timeline.fps),
    }
    _emit_report(report, args.report_json)
    stream = sys.stderr if args.report_json == "-" else sys.stdout
    print(f"valid=true clips={timeline.clip_count}", file=stream)
    print(f"conflict_count={len(conflicts)}", file=stream)
    return EXIT_OK


def _cmd_resolve(args) -> int:
    timeline = exchange.load_file(args.input, max_clips=args.max_clips)
    result = run_pipeline(timeline, max_clips=args.max_clips)
    if args.output:
        version = _parse_versions(args.output_version)
        exchange.dump_file(result.resolved, args.output, version[0])
        result.report["output"] = args.output
    if args.otio_out:
        write_otio(result.resolved, args.otio_out)
        result.report["otio_out"] = args.otio_out
    return _finish(result.report, args)


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="clipweave",
        description="Timeline conflict detection, deterministic resolution "
        "and OTIO export.",
    )
    sub = parser.add_subparsers(dest="command", required=True)

    demo = sub.add_parser("demo", help="run the built-in three-clip sample")
    demo.add_argument(
        "--roundtrip",
        default="",
        help="comma-separated exchange versions to round-trip, e.g. v1,v2",
    )
    demo.add_argument("--otio-out", help="write the resolved timeline as OTIO")
    demo.add_argument(
        "--report-json", help="JSON report destination ('-' for stdout)"
    )
    demo.add_argument("--max-clips", type=int, default=DEFAULT_MAX_CLIPS)
    demo.set_defaults(func=_cmd_demo)

    validate = sub.add_parser(
        "validate", help="validate an exchange document and count conflicts"
    )
    validate.add_argument("--input", required=True, help="exchange file")
    validate.add_argument(
        "--report-json", help="JSON report destination ('-' for stdout)"
    )
    validate.add_argument("--max-clips", type=int, default=DEFAULT_MAX_CLIPS)
    validate.set_defaults(func=_cmd_validate)

    resolve = sub.add_parser(
        "resolve", help="validate, resolve conflicts and export"
    )
    resolve.add_argument("--input", required=True, help="exchange file")
    resolve.add_argument("--output", help="write the resolved timeline")
    resolve.add_argument(
        "--output-version", default="v2", help="exchange version for --output"
    )
    resolve.add_argument("--otio-out", help="write the resolved timeline as OTIO")
    resolve.add_argument(
        "--report-json", help="JSON report destination ('-' for stdout)"
    )
    resolve.add_argument("--max-clips", type=int, default=DEFAULT_MAX_CLIPS)
    resolve.set_defaults(func=_cmd_resolve)
    return parser


def main(argv=None) -> int:
    args = build_parser().parse_args(argv)
    try:
        return args.func(args)
    except ClipweaveError as exc:
        print(f"error[{exc.code}]: {exc}", file=sys.stderr)
        return exc.exit_code
    except BrokenPipeError:
        return EXIT_OK
