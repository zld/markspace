"""Command-line entry point orchestrating the frameaudit pipeline.

Exit codes follow the documented contract:

* ``0`` -- audit completed;
* ``1`` -- decoding or JSON output failure (including exceeded hard limits);
* ``2`` -- invalid arguments or unsupported input (missing file, no video
  stream, no decodable frames).
"""

from __future__ import annotations

import argparse
import sys
from fractions import Fraction
from typing import Optional, Sequence

from .decode import DEFAULT_MAX_FRAMES
from .errors import FrameAuditError
from .outputs import render_summary, write_json_report
from .pipeline import run_audit
from .reorder import DEFAULT_WINDOW_SIZE
from .timestamps import parse_fraction


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="frameaudit",
        description=(
            "Audit decoded video frame order, frame type distribution and "
            "timestamp consistency. Local-only, no GUI and no network use."
        ),
    )
    parser.add_argument("input", help="path to the local video file")
    parser.add_argument(
        "--fps",
        default=None,
        help="nominal frame rate for repaired intervals, e.g. 30000/1001",
    )
    parser.add_argument(
        "--frame-interval",
        default=None,
        help="explicit repair interval in seconds (mutually exclusive with --fps)",
    )
    parser.add_argument(
        "--jump-threshold",
        type=float,
        default=2.0,
        help="delta in seconds above which a PTS gap is flagged as a jump",
    )
    parser.add_argument(
        "--json-out",
        default=None,
        help="write the full JSON report to this path via atomic replace",
    )
    parser.add_argument(
        "--reorder-window",
        type=int,
        default=DEFAULT_WINDOW_SIZE,
        help="maximum frames held in the presentation reorder cache",
    )
    parser.add_argument(
        "--max-frames",
        type=int,
        default=DEFAULT_MAX_FRAMES,
        help="hard cap on decoded frame records and reorder cache entries",
    )
    return parser


def _parse_args(argv: Optional[Sequence[str]]) -> argparse.Namespace:
    parser = build_parser()
    args = parser.parse_args(argv)
    if args.jump_threshold <= 0:
        parser.error("--jump-threshold must be positive")
    if args.reorder_window <= 0:
        parser.error("--reorder-window must be positive")
    if args.max_frames <= 0:
        parser.error("--max-frames must be positive")
    if args.fps is not None and args.frame_interval is not None:
        parser.error("--fps and --frame-interval are mutually exclusive")
    return args


def main(argv: Optional[Sequence[str]] = None) -> int:
    try:
        args = _parse_args(argv)
    except SystemExit as exc:
        # argparse reports usage errors with exit code 2; preserve the code.
        return int(exc.code) if exc.code is not None else 2

    try:
        fps: Optional[Fraction] = (
            parse_fraction(args.fps) if args.fps is not None else None
        )
        interval: Optional[Fraction] = (
            parse_fraction(args.frame_interval)
            if args.frame_interval is not None
            else None
        )
        stats = run_audit(
            args.input,
            fps=fps,
            jump_threshold=args.jump_threshold,
            window_size=args.reorder_window,
            max_frames=args.max_frames,
            frame_interval=interval,
        )
        print(render_summary(stats))
        if args.json_out is not None:
            write_json_report(stats, args.json_out)
            print(f"json_report={args.json_out}")
    except FrameAuditError as exc:
        print(f"error: {exc}", file=sys.stderr)
        return exc.exit_code
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
