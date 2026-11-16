"""Command line interface: argument validation, subcommands and exit codes.

Exit code contract:
    0 success
    2 usage / argument error
    3 corrupt input or unsupported codec
    4 file read / write failure
    5 output index validation failure

All errors are funnelled through :class:`~mkvmp4.errors.Mkvmp4Error`
subclasses so the mapping here stays a single table.
"""

import argparse
import os
import sys

from .errors import Mkvmp4Error, UsageError
from .pipeline import run_pipeline
from .report import (format_flat_lines, format_fragmented_lines,
                     render_report)

MODES = ("flat", "fragmented", "both")


def _build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="mkvmp4",
        description="Lossless Matroska -> MP4 remuxer (no transcoding).")
    sub = parser.add_subparsers(dest="command", required=True)

    remux = sub.add_parser(
        "remux", help="remux a Matroska file into flat/fragmented MP4")
    remux.add_argument("input", help="path to the source .mkv/.mka file")
    remux.add_argument("output", help="destination .mp4 path")
    remux.add_argument("--mode", choices=MODES, default="flat",
                       help="MP4 layout to emit (default: flat)")

    demo = sub.add_parser(
        "demo", help="generate a synthetic fixture and remux it both ways")
    demo.add_argument("--out-dir", required=True,
                      help="directory for the fixture and MP4 outputs")
    demo.add_argument("--mode", choices=MODES, default="both",
                      help="which exporter(s) to demonstrate (default: both)")
    return parser


def _check_input_path(path: str) -> None:
    if not os.path.exists(path):
        raise UsageError(f"input file does not exist: {path}")
    if not os.path.isfile(path):
        raise UsageError(f"input path is not a regular file: {path}")
    if not os.access(path, os.R_OK):
        raise UsageError(f"input file is not readable: {path}")


def _ensure_output_dir(path: str) -> None:
    directory = os.path.dirname(os.path.abspath(path)) or "."
    if not os.path.isdir(directory):
        raise UsageError(f"output directory does not exist: {directory}")
    if not os.access(directory, os.W_OK):
        raise UsageError(f"output directory is not writable: {directory}")


def _print_result(result, mode_lines: list[str]) -> None:
    report = render_report(
        result.tree, result.mkv, result.timeline, mode_lines)
    print(report)


def _run_remux(args: argparse.Namespace) -> int:
    _check_input_path(args.input)
    _ensure_output_dir(args.output)
    flat_path = args.output if args.mode in ("flat", "both") else None
    frag_path = args.output if args.mode == "fragmented" else None
    if args.mode == "both":
        # A single output path cannot hold two files; derive sibling names.
        base, ext = os.path.splitext(args.output)
        flat_path = f"{base}.flat{ext or '.mp4'}"
        frag_path = f"{base}.frag{ext or '.mp4'}"
    result = run_pipeline(
        args.input, args.mode,
        flat_path=flat_path, fragmented_path=frag_path)
    mode_lines: list[str] = []
    if result.flat is not None:
        mode_lines.extend(format_flat_lines(result.flat))
    if result.fragmented is not None:
        mode_lines.extend(format_fragmented_lines(result.fragmented))
    _print_result(result, mode_lines)
    return 0


def _run_demo(args: argparse.Namespace) -> int:
    # Imported lazily so the tooling module is only needed for the demo path.
    root = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
    tools_dir = os.path.join(os.path.dirname(root), "tools")
    if tools_dir not in sys.path:
        sys.path.insert(0, tools_dir)
    from make_fixture import write_fixture

    out_dir = os.path.abspath(args.out_dir)
    os.makedirs(out_dir, exist_ok=True)
    fixture_path = os.path.join(out_dir, "synthetic.mkv")
    write_fixture(fixture_path)
    flat_path = os.path.join(out_dir, "synthetic.flat.mp4")
    frag_path = os.path.join(out_dir, "synthetic.frag.mp4")
    result = run_pipeline(
        fixture_path, args.mode,
        flat_path=flat_path if args.mode in ("flat", "both") else None,
        fragmented_path=(frag_path if args.mode in ("fragmented", "both")
                         else None))
    mode_lines: list[str] = []
    if result.flat is not None:
        mode_lines.extend(format_flat_lines(result.flat))
    if result.fragmented is not None:
        mode_lines.extend(format_fragmented_lines(result.fragmented))
    _print_result(result, mode_lines)
    print(f"fixture={fixture_path}")
    return 0


def main(argv: list[str] | None = None) -> int:
    parser = _build_parser()
    args = parser.parse_args(argv)
    try:
        if args.command == "remux":
            return _run_remux(args)
        if args.command == "demo":
            return _run_demo(args)
        raise UsageError(f"unknown command {args.command!r}")
    except Mkvmp4Error as exc:
        print(f"error: {exc}", file=sys.stderr)
        return exc.exit_code
    except argparse.ArgumentError as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 2


if __name__ == "__main__":
    raise SystemExit(main())
