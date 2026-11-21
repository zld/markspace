"""Command line interface.

Exit codes::

    0  success
    2  invalid arguments or timeline document
    3  asset loading, compositing or export failure
"""

from __future__ import annotations

import argparse
import json
import sys
from fractions import Fraction
from pathlib import Path

from . import __version__
from .assets import AssetError, load_asset
from .exporter import MAX_WORKERS, MIN_WORKERS, ExportError, export_frames
from .model import TimelineError, load_timeline, parse_timeline
from .timebase import build_clock
from .compositor import CompositorError, preload_assets

EXIT_OK = 0
EXIT_USAGE = 2
EXIT_RUNTIME = 3


def _side(value: str) -> int:
    try:
        n = int(value)
    except ValueError as e:
        raise argparse.ArgumentTypeError(f"canvas side must be an integer: {value!r}") from e
    if not 1 <= n <= 4096:
        raise argparse.ArgumentTypeError("canvas sides must be in 1..4096")
    return n


def _positive_float(value: str) -> float:
    try:
        n = float(value)
    except ValueError as e:
        raise argparse.ArgumentTypeError(f"expected a number: {value!r}") from e
    if not n > 0:
        raise argparse.ArgumentTypeError("value must be > 0")
    return n


def _fps(value: str) -> Fraction:
    try:
        if "/" in value:
            num, den = value.split("/", 1)
            frac = Fraction(int(num), int(den))
        else:
            frac = Fraction(int(value), 1)
    except (ValueError, ZeroDivisionError) as e:
        raise argparse.ArgumentTypeError(f"invalid frame rate {value!r}") from e
    if frac <= 0:
        raise argparse.ArgumentTypeError("frame rate must be > 0")
    return frac


def _workers(value: str) -> int:
    try:
        n = int(value)
    except ValueError as e:
        raise argparse.ArgumentTypeError("workers must be an integer") from e
    if not MIN_WORKERS <= n <= MAX_WORKERS:
        raise argparse.ArgumentTypeError(f"workers must be in {MIN_WORKERS}..{MAX_WORKERS}")
    return n


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(prog="shotline", description=__doc__)
    parser.add_argument("--version", action="version", version=f"shotline {__version__}")
    sub = parser.add_subparsers(dest="command", required=True)

    common = argparse.ArgumentParser(add_help=False)
    common.add_argument("--workers", type=_workers, default=1,
                        help=f"render threads ({MIN_WORKERS}..{MAX_WORKERS})")
    common.add_argument("--output", required=True, type=Path,
                        help="destination JSONL frame-summary path")

    demo = sub.add_parser("demo", parents=[common],
                          help="render a fully deterministic generated timeline")
    demo.add_argument("--width", type=_side, required=True)
    demo.add_argument("--height", type=_side, required=True)
    demo.add_argument("--fps", type=_fps, required=True, help="e.g. 24 or 24000/1001")
    demo.add_argument("--duration", type=_positive_float, required=True,
                      help="timeline length in seconds")
    demo.add_argument("--seed", type=int, default=1)

    run = sub.add_parser("run", parents=[common],
                         help="render a timeline JSON file from disk")
    run.add_argument("--timeline", required=True, type=Path)
    return parser


def build_demo_document(width: int, height: int, fps: Fraction,
                        duration: float, seed: int) -> dict:
    """Construct the deterministic demo timeline (3 layers, 2 audio cues)."""
    return {
        "width": width,
        "height": height,
        "fps": {"numerator": fps.numerator, "denominator": fps.denominator},
        "duration": duration,
        "background": [18, 22, 30, 255],
        "layers": [
            {
                "id": "background-gradient",
                "source": "demo:gradient",
                "z": 0,
                "blend": "normal",
                "keyframes": [
                    {"t": 0.0, "x": 0, "y": 0, "scale": 1.0, "opacity": 1.0},
                    {"t": duration, "x": width * 0.15, "y": height * 0.1,
                     "scale": 1.6, "opacity": 0.85},
                ],
            },
            {
                "id": "moving-badge",
                "source": "demo:badge",
                "z": 1,
                "blend": "screen",
                "keyframes": [
                    {"t": 0.0, "x": -width * 0.2, "y": height * 0.25,
                     "scale": 1.2, "opacity": 0.9},
                    {"t": duration * 0.5, "x": width * 0.35, "y": height * 0.15,
                     "scale": 1.8, "opacity": 1.0},
                    {"t": duration, "x": width * 0.75, "y": height * 0.35,
                     "scale": 1.1, "opacity": 0.7},
                ],
            },
            {
                "id": "alpha-stripes",
                "source": "demo:stripe",
                "z": 2,
                "blend": "multiply",
                "keyframes": [
                    {"t": 0.0, "x": width * 0.1, "y": -height * 0.1,
                     "scale": 2.0, "opacity": 0.6,
                     "crop": [0.0, 0.0, 1.0, 1.0]},
                    {"t": duration, "x": width * 0.4, "y": height * 0.45,
                     "scale": 2.4, "opacity": 0.45,
                     "crop": [0.1, 0.1, 0.9, 0.9]},
                ],
            },
        ],
        "cues": [
            {"sample_rate": 48000, "sample_index": 24000, "layer_id": "moving-badge"},
            {"sample_rate": 48000, "sample_index": 72000, "layer_id": "moving-badge"},
        ],
    }


def _execute(timeline, loader, output: Path, workers: int) -> int:
    try:
        clock = build_clock(timeline)
        assets = preload_assets(timeline, loader)
        records, summary = export_frames(timeline, assets, clock, output, workers)
    except (AssetError, CompositorError, ExportError, OSError) as e:
        print(f"shotline: rendering failed: {e}", file=sys.stderr)
        return EXIT_RUNTIME
    for rec in records:
        print(json.dumps(rec.to_json(), separators=(",", ":")))
    print(json.dumps(summary, separators=(",", ":")))
    print(
        f"# wrote {summary['frame_count']} frames, {summary['cue_count']} cues, "
        f"max A/V error {summary['max_abs_av_error_ms']:.3f} ms -> {output}",
        file=sys.stderr,
    )
    return EXIT_OK


def main(argv: list[str] | None = None) -> int:
    parser = build_parser()
    try:
        args = parser.parse_args(argv)
    except SystemExit as e:
        # argparse exits on bad flags; normalise that to the usage exit code.
        return EXIT_USAGE if e.code in (None, 2) else int(e.code)
    try:
        if args.command == "demo":
            doc = build_demo_document(args.width, args.height, args.fps,
                                      args.duration, args.seed)
            timeline = parse_timeline(doc, base_dir=Path.cwd())
            loader = lambda layer: load_asset(layer.source, timeline, seed=args.seed)  # noqa: E731
        else:
            timeline = load_timeline(args.timeline)
            loader = lambda layer: load_asset(layer.source, timeline)  # noqa: E731
    except TimelineError as e:
        print(f"shotline: invalid timeline: {e}", file=sys.stderr)
        return EXIT_USAGE
    return _execute(timeline, loader, args.output, args.workers)


if __name__ == "__main__":
    raise SystemExit(main())
