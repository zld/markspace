"""Command-line interface: argument parsing and exit-code aggregation."""

from __future__ import annotations

import argparse
import sys

from .errors import EXIT_INVALID, MixforgeError
from .mixer import MixGraph
from .project import load_project, validate_block_frames
from .render import render_offline, render_stream
from .report import build_report, format_summary, write_report_atomic


def _cmd_render(args: argparse.Namespace) -> int:
    # All validation runs before any artifact is written, so an invalid
    # manifest can never leave a pseudo-successful report behind.
    project = load_project(args.project)
    block_frames = validate_block_frames(args.block_frames)
    graph = MixGraph(project)
    if args.mode == "offline":
        render_offline(graph, block_frames)
    else:
        render_stream(graph, block_frames)
    report = build_report(graph, args.mode, block_frames)
    write_report_atomic(args.report, report)
    print(format_summary(report))
    return 0


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="mixforge",
        description="Deterministic local timeline audio mixer.")
    subparsers = parser.add_subparsers(dest="command", required=True)
    render = subparsers.add_parser("render", help="render a project manifest")
    render.add_argument("--project", required=True, help="path to project.json")
    render.add_argument("--mode", choices=("offline", "stream"), required=True)
    render.add_argument("--block-frames", type=int, default=1024)
    render.add_argument("--report", required=True, help="path to the JSON report")
    return parser


def main(argv: list[str] | None = None) -> int:
    parser = build_parser()
    args = parser.parse_args(argv)
    try:
        if args.command == "render":
            return _cmd_render(args)
    except MixforgeError as exc:
        print(f"error: {exc}", file=sys.stderr)
        return exc.exit_code
    return EXIT_INVALID


if __name__ == "__main__":
    sys.exit(main())
