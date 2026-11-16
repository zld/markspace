"""Terminal entry point for the timeline summary pipeline.

Exit codes: 0 success, 2 invalid parameters or timeline, 3 compositing or
summary validation failure, 4 summary write failure.
"""
from __future__ import annotations

import argparse
import sys

from .audio_sync import AudioCueConfig, AudioSyncError, measure_alignment
from .clock import MAX_FPS, MIN_FPS, ClockError, FrameClock
from .compositor import CompositeError
from .exporter import (
    SummaryValidationError,
    SummaryWriteError,
    export_summary,
)
from .fixture import build_demo_timeline
from .model import (
    DEFAULT_MAX_DIMENSION,
    DEFAULT_MAX_FRAMES,
    TimelineError,
)

EXIT_OK = 0
EXIT_INVALID = 2
EXIT_COMPOSITE = 3
EXIT_WRITE = 4


def _parse_size(text: str) -> tuple[int, int]:
    try:
        raw_w, raw_h = text.lower().split("x", 1)
        width, height = int(raw_w), int(raw_h)
    except ValueError:
        raise argparse.ArgumentTypeError(
            f"size must look like WxH (e.g. 320x180), got {text!r}"
        ) from None
    if width < 1 or height < 1:
        raise argparse.ArgumentTypeError(f"size dimensions must be >= 1, got {text!r}")
    return width, height


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="timeline.cli",
        description="Reproducible cutscene timeline summary pipeline.",
    )
    sub = parser.add_subparsers(dest="command", required=True)
    demo = sub.add_parser("demo", help="render the deterministic demo timeline")
    demo.add_argument("--fps", type=int, required=True,
                      help=f"frames per second [{MIN_FPS}, {MAX_FPS}]")
    demo.add_argument("--duration", type=float, required=True,
                      help="timeline duration in seconds")
    demo.add_argument("--size", type=_parse_size, required=True,
                      help="canvas size as WxH, e.g. 320x180")
    demo.add_argument("--sample-rate", type=int, default=48000,
                      help="audio cue sample rate (default 48000)")
    demo.add_argument("--offset-ms", type=float, default=0.0,
                      help="audio cue start offset in milliseconds")
    demo.add_argument("--drift-ppm", type=float, default=0.0,
                      help="audio clock drift in parts per million")
    demo.add_argument("--summary", default=None,
                      help="path of the summary JSON to write")
    demo.set_defaults(handler=_cmd_demo)
    return parser


def _cmd_demo(args: argparse.Namespace) -> int:
    try:
        clock = FrameClock(args.fps)
        frame_count = clock.frames_for_duration(args.duration)
        if frame_count < 1:
            raise TimelineError(
                f"duration {args.duration}s at {args.fps} fps yields zero frames"
            )
        width, height = args.size
        timeline = build_demo_timeline(args.fps, frame_count, width, height)
        timeline.validate(DEFAULT_MAX_DIMENSION, DEFAULT_MAX_FRAMES)
        cue_config = AudioCueConfig(args.sample_rate, args.offset_ms, args.drift_ppm)
    except (ClockError, TimelineError, AudioSyncError) as exc:
        print(f"error: invalid parameters: {exc}", file=sys.stderr)
        return EXIT_INVALID

    try:
        stats = measure_alignment(clock, frame_count, cue_config)
    except AudioSyncError as exc:
        print(f"error: audio alignment failed: {exc}", file=sys.stderr)
        return EXIT_COMPOSITE

    def _report(index: int, digest: str) -> None:
        print(f"frame_index={index} sha256={digest}")

    try:
        summary = export_summary(timeline, stats, args.summary, on_frame=_report)
    except SummaryWriteError as exc:
        print(f"error: {exc}", file=sys.stderr)
        return EXIT_WRITE
    except (CompositeError, SummaryValidationError) as exc:
        print(f"error: {exc}", file=sys.stderr)
        return EXIT_COMPOSITE

    print(
        "summary: "
        f"frame_count={summary['frame_count']} "
        f"frame_sha256_count={summary['frame_sha256_count']} "
        f"av_max_error_ms={summary['audio']['av_max_error_ms']}"
    )
    return EXIT_OK


def main(argv: list[str] | None = None) -> int:
    args = build_parser().parse_args(argv)
    return args.handler(args)


if __name__ == "__main__":
    sys.exit(main())
