"""Terminal front end for the timeline composer.

Exit codes are part of the contract:

* ``0`` success
* ``2`` argument or timeline (schema) error
* ``3`` asset or composition failure
* ``4`` summary mismatch or summary I/O failure

All output is plain text on stdout/stderr; there are no GUI dependencies and
no browser, camera, or microphone access anywhere in the package.
"""

from __future__ import annotations

import argparse
import json
import sys
import tempfile
from pathlib import Path
from typing import Any, Sequence

from . import __version__
from .assets import AssetError, AssetStore
from .audio_sync import align_events
from .compositor import CompositorError, FrameCompositor
from .exporter import ExportError, export_summary, read_summary
from .fixtures import build_demo
from .schema import MAX_FRAMES, SchemaError, parse_timeline

EXIT_OK = 0
EXIT_ARGUMENT = 2
EXIT_ASSET = 3
EXIT_SUMMARY = 4


class _ArgumentParser(argparse.ArgumentParser):
    """ArgumentParser that raises instead of calling ``sys.exit``."""

    def error(self, message: str) -> Any:  # pragma: no cover - exercised via CLI
        raise _UsageError(message)


class _UsageError(ValueError):
    pass


def build_parser() -> argparse.ArgumentParser:
    parser = _ArgumentParser(
        prog="timeline-composer",
        description="Compose a timeline offscreen and emit per-frame SHA-256 summaries.",
    )
    parser.add_argument("--version", action="version", version=f"%(prog)s {__version__}")
    sub = parser.add_subparsers(dest="command", required=True)

    demo = sub.add_parser("demo", help="generate fixtures, compose frames, write a summary")
    _add_grid_options(demo)
    demo.add_argument("--summary", required=True, type=Path, help="destination JSONL path")
    demo.add_argument("--workers", type=int, default=1, help="bounded worker count (default 1)")

    verify = sub.add_parser("verify", help="rebuild the demo and compare against a summary")
    _add_grid_options(verify)
    verify.add_argument("--summary", required=True, type=Path, help="summary to verify")
    verify.add_argument("--workers", type=int, default=1, help="bounded worker count (default 1)")
    verify.add_argument("--demo", action="store_true", help="rebuild deterministic demo fixtures")

    timeline = sub.add_parser("compose", help="compose a timeline JSON file")
    timeline.add_argument("--timeline", required=True, type=Path, help="timeline JSON path")
    timeline.add_argument(
        "--assets", required=True, type=Path, help="root directory for PNG assets"
    )
    timeline.add_argument("--summary", required=True, type=Path, help="destination JSONL path")
    timeline.add_argument("--workers", type=int, default=1, help="bounded worker count (default 1)")

    return parser


def _add_grid_options(parser: argparse.ArgumentParser) -> None:
    parser.add_argument("--fps", default="30000/1001", help="rational frame rate (default 30000/1001)")
    parser.add_argument("--frames", type=int, default=120, help="frame count (default 120)")
    parser.add_argument("--canvas", default="320x180", metavar="WxH", help="canvas size (default 320x180)")


def parse_canvas(text: str) -> tuple[int, int]:
    try:
        width_text, height_text = text.lower().split("x", 1)
        width = int(width_text)
        height = int(height_text)
    except (ValueError, AttributeError) as exc:
        raise _UsageError(f"--canvas must look like WIDTHxHEIGHT, got {text!r}") from exc
    if width <= 0 or height <= 0:
        raise _UsageError(f"--canvas dimensions must be positive, got {text!r}")
    return width, height


def main(argv: Sequence[str] | None = None) -> int:
    parser = build_parser()
    try:
        args = parser.parse_args(argv)
    except _UsageError as exc:
        print(f"error: {exc}", file=sys.stderr)
        return EXIT_ARGUMENT

    try:
        if args.command == "demo":
            return _run_demo(args)
        if args.command == "verify":
            return _run_verify(args)
        if args.command == "compose":
            return _run_compose(args)
    except SchemaError as exc:
        print(f"timeline error: {exc}", file=sys.stderr)
        return EXIT_ARGUMENT
    except _UsageError as exc:
        print(f"error: {exc}", file=sys.stderr)
        return EXIT_ARGUMENT
    except (AssetError, CompositorError) as exc:
        print(f"composition error: {exc}", file=sys.stderr)
        return EXIT_ASSET
    except ExportError as exc:
        print(f"summary error: {exc}", file=sys.stderr)
        return EXIT_SUMMARY
    except KeyboardInterrupt:
        print("interrupted", file=sys.stderr)
        return EXIT_SUMMARY
    parser.print_help()
    return EXIT_ARGUMENT


def _run_demo(args: argparse.Namespace) -> int:
    width, height = parse_canvas(args.canvas)
    _check_worker_count(args.workers)
    with tempfile.TemporaryDirectory(prefix="timeline-demo-") as workspace:
        timeline, root = build_demo(workspace, args.frames, args.fps, width, height)
        composer = FrameCompositor(timeline, AssetStore(root))
        result = export_summary(composer, args.summary, workers=args.workers)
    print(
        f"frame_count={result.frame_count} "
        f"audio_events={len(timeline.audio_events)} "
        f"av_max_abs_error_ms={result.max_abs_error_ms:.2f}"
    )
    print(f"summary={result.path}")
    return EXIT_OK


def _run_verify(args: argparse.Namespace) -> int:
    if not args.demo:
        raise _UsageError("verify currently requires --demo")
    width, height = parse_canvas(args.canvas)
    _check_worker_count(args.workers)
    expected_header, expected_frames = read_summary(args.summary)
    with tempfile.TemporaryDirectory(prefix="timeline-verify-") as workspace:
        timeline, root = build_demo(workspace, args.frames, args.fps, width, height)
        composer = FrameCompositor(timeline, AssetStore(root))
        with tempfile.TemporaryDirectory(prefix="timeline-verify-out-") as out_dir:
            rebuilt_path = Path(out_dir) / "rebuilt.jsonl"
            export_summary(composer, rebuilt_path, workers=args.workers)
            rebuilt_header, rebuilt_frames = read_summary(rebuilt_path)
    mismatches = _compare(expected_header, expected_frames, rebuilt_header, rebuilt_frames)
    if mismatches:
        for mismatch in mismatches[:10]:
            print(f"MISMATCH {mismatch}", file=sys.stderr)
        if len(mismatches) > 10:
            print(f"... {len(mismatches) - 10} more mismatches", file=sys.stderr)
        return EXIT_SUMMARY
    print(f"MATCH frames={len(rebuilt_frames)} summary={args.summary}")
    return EXIT_OK


def _run_compose(args: argparse.Namespace) -> int:
    _check_worker_count(args.workers)
    timeline_path = args.timeline.expanduser()
    try:
        document = json.loads(timeline_path.read_text(encoding="utf-8"))
    except OSError as exc:
        raise _UsageError(f"cannot read timeline {timeline_path}: {exc}") from exc
    except json.JSONDecodeError as exc:
        raise SchemaError(f"timeline is not valid JSON: {exc}") from exc
    timeline = parse_timeline(document)
    if timeline.frame_count > MAX_FRAMES:  # defensive: parser already enforces
        raise SchemaError(f"frame count {timeline.frame_count} exceeds {MAX_FRAMES}")
    composer = FrameCompositor(timeline, AssetStore(args.assets))
    result = export_summary(composer, args.summary, workers=args.workers)
    _ = align_events(timeline.clock, timeline.audio_events)
    print(
        f"frame_count={result.frame_count} "
        f"audio_events={len(timeline.audio_events)} "
        f"av_max_abs_error_ms={result.max_abs_error_ms:.2f}"
    )
    print(f"summary={result.path}")
    return EXIT_OK


def _check_worker_count(workers: int) -> None:
    if workers <= 0:
        raise _UsageError("--workers must be a positive integer")


def _compare(
    expected_header: dict[str, Any],
    expected_frames: list[dict[str, Any]],
    rebuilt_header: dict[str, Any],
    rebuilt_frames: list[dict[str, Any]],
) -> list[str]:
    mismatches: list[str] = []
    for key in sorted(set(expected_header) | set(rebuilt_header)):
        if expected_header.get(key) != rebuilt_header.get(key):
            mismatches.append(f"header.{key}: expected {expected_header.get(key)!r} rebuilt {rebuilt_header.get(key)!r}")
    if len(expected_frames) != len(rebuilt_frames):
        mismatches.append(f"frame rows: expected {len(expected_frames)} rebuilt {len(rebuilt_frames)}")
    for index, (expected, rebuilt) in enumerate(zip(expected_frames, rebuilt_frames)):
        if expected.get("frame") != index:
            mismatches.append(f"row {index}: frame index {expected.get('frame')!r}")
        for key in ("pts", "pts_seconds", "pixel_sha256", "audio_errors_ms"):
            if expected.get(key) != rebuilt.get(key):
                mismatches.append(
                    f"frame {index}.{key}: expected {expected.get(key)!r} rebuilt {rebuilt.get(key)!r}"
                )
    return mismatches


if __name__ == "__main__":  # pragma: no cover
    sys.exit(main())
