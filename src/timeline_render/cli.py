"""Terminal interface.

Exit codes: 0 = full export succeeded, 2 = input/argument error,
3 = composition failure (no partial output is produced in that case).
"""

from __future__ import annotations

import argparse
import json
import sys

from .model import ComposeError, InputError
from .export import render_summary
from .validate import load_timeline


def _build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="timeline_render",
        description="Compose a timeline offscreen and print verifiable "
        "per-frame SHA-256 digests. No image or video files are written.",
    )
    sub = parser.add_subparsers(dest="command", required=True)
    render = sub.add_parser("render", help="compose frames and print digests")
    render.add_argument("--input", required=True, help="timeline JSON path")
    render.add_argument("--width", required=True, type=int, help="canvas width")
    render.add_argument("--height", required=True, type=int, help="canvas height")
    render.add_argument("--json", action="store_true",
                        help="emit machine-readable JSON (default: text lines)")
    return parser


def main(argv: list[str] | None = None) -> int:
    args = _build_parser().parse_args(argv)
    try:
        timeline = load_timeline(args.input, args.width, args.height)
    except InputError as exc:
        print(f"input error: {exc}", file=sys.stderr)
        return 2

    try:
        summary = render_summary(timeline, args.width, args.height)
    except InputError as exc:
        print(f"input error: {exc}", file=sys.stderr)
        return 2
    except ComposeError as exc:
        print(f"compose error: {exc}", file=sys.stderr)
        return 3
    except Exception as exc:  # unexpected compositor failure: no partial output
        print(f"compose error: unexpected failure: {exc}", file=sys.stderr)
        return 3

    if args.json:
        print(json.dumps(summary, indent=2))
    else:
        print(f"frame_count: {summary['frame_count']}")
        for entry in summary["frames"]:
            print(
                f"frame {entry['frame']} "
                f"sha256={entry['pixel_sha256']} "
                f"audio_alignment_error_samples="
                f"{entry['audio_alignment_error_samples']}"
            )
    return 0


if __name__ == "__main__":
    sys.exit(main())
