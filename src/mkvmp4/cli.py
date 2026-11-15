"""Command line interface: inspect, remux and verify subcommands.

Exit code contract:
    0  success
    2  invalid arguments or malformed/unsupported input
    3  conversion or verification failure
    1  unexpected internal error

Run as ``python -m mkvmp4.cli ...`` or via the installed ``mkvmp4`` script.
"""

from __future__ import annotations

import argparse
import json
import os
import sys

from .ebml import Limits
from .errors import InputFormatError, MkvMp4Error
from .export import remux, verify_file
from .matroska import parse_matrouska
from .report import (element_tree, inspect_summary, render_remux_text,
                     render_text, render_verify_text)


def _build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="mkvmp4",
        description="Offline, auditable Matroska -> MP4 remuxer")
    parser.add_argument("--max-bytes", type=int,
                        default=Limits.max_bytes,
                        help="maximum accepted input file size")
    parser.add_argument("--max-elements", type=int,
                        default=Limits.max_elements,
                        help="maximum accepted EBML element count")
    parser.add_argument("--max-sample-bytes", type=int,
                        default=Limits.max_sample_bytes,
                        help="maximum accepted single sample/block size")
    sub = parser.add_subparsers(dest="command", required=True)

    p_inspect = sub.add_parser("inspect", help="summarize a Matroska file")
    p_inspect.add_argument("--input", required=True)
    p_inspect.add_argument("--json", action="store_true",
                           dest="as_json")

    p_remux = sub.add_parser("remux", help="remux Matroska into MP4")
    p_remux.add_argument("--input", required=True)
    p_remux.add_argument("--output", required=True)
    p_remux.add_argument("--mode", choices=("single", "segmented"),
                         default="single")
    p_remux.add_argument("--tracks", default=None,
                         help="comma-separated source track numbers")
    p_remux.add_argument("--json", action="store_true", dest="as_json")

    p_verify = sub.add_parser("verify",
                              help="read back and verify an MP4 index")
    p_verify.add_argument("--input", required=True)
    p_verify.add_argument("--json", action="store_true", dest="as_json")
    return parser


def _limits(args: argparse.Namespace) -> Limits:
    values = (args.max_bytes, args.max_elements, args.max_sample_bytes)
    if any(v is not None and v <= 0 for v in values):
        raise InputFormatError(
            "limit arguments must be positive integers")
    return Limits(max_bytes=args.max_bytes,
                  max_elements=args.max_elements,
                  max_sample_bytes=args.max_sample_bytes)


def _read_input(path: str, limits: Limits) -> bytes:
    if not os.path.isfile(path):
        raise InputFormatError(f"input file does not exist: {path}")
    size = os.path.getsize(path)
    if size > limits.max_bytes:
        raise InputFormatError(
            f"input size {size} exceeds limit of {limits.max_bytes} bytes")
    with open(path, "rb") as handle:
        return handle.read()


def _parse_tracks(raw: str | None) -> list[int] | None:
    if raw is None:
        return None
    parts = [piece.strip() for piece in raw.split(",") if piece.strip()]
    if not parts:
        raise InputFormatError("track selection is empty")
    try:
        numbers = [int(piece) for piece in parts]
    except ValueError:
        raise InputFormatError(f"invalid track number list: {raw!r}")
    if any(n <= 0 for n in numbers):
        raise InputFormatError("track numbers must be positive integers")
    return numbers


def _cmd_inspect(args: argparse.Namespace, limits: Limits) -> dict | str:
    data = _read_input(args.input, limits)
    mkv = parse_matrouska(data, args.input, limits)
    tree_info = element_tree(data, limits)
    summary = inspect_summary(mkv, tree_info)
    if args.as_json:
        return json.dumps(summary, indent=2, ensure_ascii=False)
    return render_text(summary)


def _cmd_remux(args: argparse.Namespace, limits: Limits) -> dict | str:
    selection = _parse_tracks(args.tracks)
    result = remux(args.input, args.output, mode=args.mode,
                   limits=limits, track_selection=selection)
    if args.as_json:
        return json.dumps(result, indent=2, ensure_ascii=False)
    return render_remux_text(result)


def _cmd_verify(args: argparse.Namespace, limits: Limits) -> dict | str:
    result = verify_file(args.input, limits)
    if args.as_json:
        return json.dumps(result, indent=2, ensure_ascii=False)
    return render_verify_text(result)


_DISPATCH = {
    "inspect": _cmd_inspect,
    "remux": _cmd_remux,
    "verify": _cmd_verify,
}


def main(argv: list[str] | None = None) -> int:
    parser = _build_parser()
    args = parser.parse_args(argv)
    try:
        limits = _limits(args)
        output = _DISPATCH[args.command](args, limits)
    except MkvMp4Error as exc:
        print(f"error: {exc.describe()}", file=sys.stderr)
        return exc.exit_code
    except (OSError, ValueError) as exc:
        print(f"error: io_or_value: {exc}", file=sys.stderr)
        return 2
    print(output)
    return 0


if __name__ == "__main__":
    sys.exit(main())
