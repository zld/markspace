"""Command-line entry point.

Exit codes: 0 success, 2 invalid arguments or timeline, 3 render or
summary-write failure.
"""

from __future__ import annotations

import argparse
import json
import sys
import tempfile
from fractions import Fraction
from pathlib import Path

from .assets import AssetError, generate_demo_assets, load_png
from .audio import max_alignment_error_ms, synth_beeps
from .exporter import ExportError, SummaryWriter, frame_sha256
from .model import Timeline, TimelineError, parse_timeline
from .renderer import RenderError, render_all
from .timebase import format_fps, parse_fps

EXIT_OK = 0
EXIT_INVALID = 2
EXIT_FAILED = 3


def _keys(points):
    """Build a keyframe list, keeping the last entry when frames collide."""
    merged = {}
    for frame, value, interp in points:
        merged[frame] = (frame, value, interp)
    return [{"frame": f, "value": v, "interp": i}
            for f, v, i in (merged[k] for k in sorted(merged))]


def _build_demo_timeline(width: int, height: int, fps: str, frames: int) -> dict:
    rate = parse_fps(fps)  # validated here so bad input maps to exit 2
    duration = Fraction(frames) / rate
    layers = [
        {
            "id": "base",
            "asset": "base.png",
            "start": 0,
            "end": str(duration),
            "position": [(width - 256) // 2, (height - 144) // 2],
            "scale": 1.0,
            "blend": "normal",
            "opacity": 1.0,
        },
        {
            "id": "glow",
            "asset": "glow.png",
            "start": 0,
            "end": str(duration),
            "position": [(width - 128) // 2, (height - 128) // 2],
            "scale": 1.5,
            "blend": "screen",
            "opacity": _keys([
                (0, 0.0, "linear"),
                (max(frames // 2, 1), 0.9, "hold"),
                (max(frames - 1, 1), 0.9, "linear"),
            ]),
        },
        {
            "id": "particles",
            "asset": "particles.png",
            "start": 0,
            "end": str(duration),
            "position": [(width - 96) // 2, (height - 96) // 2],
            "scale": 2.0,
            "blend": "add",
            "opacity": _keys([
                (0, 0.0, "linear"),
                (max(frames // 3, 1), 0.8, "linear"),
                (max(frames - 1, 1), 0.2, "linear"),
            ]),
        },
    ]
    beeps = []
    if frames > 0:
        frame_dur = Fraction(1) / rate
        beeps = [
            {"time": str(duration * f + frame_dur * Fraction(2, 5)),
             "freq": freq, "duration": 0.08}
            for f, freq in ((Fraction(1, 4), 660.0), (Fraction(1, 2), 880.0),
                            (Fraction(3, 4), 990.0))
        ]
    return {
        "fps": fps,
        "width": width,
        "height": height,
        "duration": str(duration),
        "layers": layers,
        "audio": {"sample_rate": 48000, "beeps": beeps},
    }


def _run(timeline: Timeline, assets: dict, workers: int, summary_path: str,
         print_hashes: bool) -> int:
    frames_meta = []

    def on_frame(index: int, rgba: bytes) -> None:
        digest = frame_sha256(rgba)
        frames_meta.append({"frame_index": index, "sha256": digest})
        if print_hashes:
            print(f"frame_index={index} sha256={digest}")

    writer = SummaryWriter(summary_path)
    try:
        count = render_all(timeline, assets, workers, on_frame)
        synth_beeps(timeline)  # PCM kept in memory; validates the audio path
        summary = {
            "fps": format_fps(timeline.fps),
            "width": timeline.width,
            "height": timeline.height,
            "frame_count": count,
            "av_sync": {
                "sample_rate": timeline.sample_rate,
                "max_abs_error_ms": max_alignment_error_ms(timeline),
            },
            "frames": frames_meta,
        }
        writer.write(summary)
    except (RenderError, ExportError) as exc:
        writer.abort()
        print(f"error: {exc}", file=sys.stderr)
        return EXIT_FAILED
    print(f"frame_count={count}")
    print(f"fps={format_fps(timeline.fps)}")
    print(f"av_sync.max_abs_error_ms={summary['av_sync']['max_abs_error_ms']:.6f}")
    print(f"summary={summary_path}")
    return EXIT_OK


def _load_assets(timeline: Timeline, asset_dir) -> dict:
    assets = {}
    for layer in timeline.layers:
        if layer.asset not in assets:
            assets[layer.asset] = load_png(asset_dir, layer.asset)
    return assets


def _cmd_demo(args) -> int:
    try:
        spec = _build_demo_timeline(args.width, args.height, args.fps, args.frames)
        timeline = parse_timeline(spec)
    except (TimelineError, ValueError) as exc:
        print(f"error: {exc}", file=sys.stderr)
        return EXIT_INVALID
    with tempfile.TemporaryDirectory(prefix="timeline-audit-") as tmp:
        try:
            generate_demo_assets(tmp)
            assets = _load_assets(timeline, tmp)
        except AssetError as exc:
            print(f"error: {exc}", file=sys.stderr)
            return EXIT_FAILED
        return _run(timeline, assets, args.workers, args.summary, args.print_hashes)


def _cmd_run(args) -> int:
    try:
        data = json.loads(Path(args.timeline).read_text(encoding="utf-8"))
        timeline = parse_timeline(data)
    except OSError as exc:
        print(f"error: cannot read timeline: {exc}", file=sys.stderr)
        return EXIT_INVALID
    except (json.JSONDecodeError, TimelineError, ValueError) as exc:
        print(f"error: {exc}", file=sys.stderr)
        return EXIT_INVALID
    try:
        assets = _load_assets(timeline, args.assets)
    except AssetError as exc:
        print(f"error: {exc}", file=sys.stderr)
        return EXIT_INVALID
    return _run(timeline, assets, args.workers, args.summary, args.print_hashes)


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="timeline_audit",
        description="Off-screen timeline compositing audit tool",
    )
    sub = parser.add_subparsers(dest="command", required=True)

    def add_common(p):
        p.add_argument("--workers", type=int, default=1,
                       help="max parallel frame renderers (default 1)")
        p.add_argument("--summary", required=True, help="summary JSON output path")
        p.add_argument("--print-hashes", action="store_true",
                       help="print per-frame sha256 lines")

    demo = sub.add_parser("demo", help="generate sample assets and render")
    demo.add_argument("--width", type=int, default=320)
    demo.add_argument("--height", type=int, default=180)
    demo.add_argument("--fps", default="30000/1001")
    demo.add_argument("--frames", type=int, default=60)
    add_common(demo)
    demo.set_defaults(func=_cmd_demo)

    run = sub.add_parser("run", help="render a timeline JSON file")
    run.add_argument("--timeline", required=True, help="timeline JSON path")
    run.add_argument("--assets", required=True, help="asset directory")
    add_common(run)
    run.set_defaults(func=_cmd_run)
    return parser


def main(argv=None) -> int:
    parser = build_parser()
    args = parser.parse_args(argv)
    if args.workers < 1:
        print("error: --workers must be >= 1", file=sys.stderr)
        return EXIT_INVALID
    if args.command == "demo" and (args.width <= 0 or args.height <= 0
                                   or args.frames < 0):
        print("error: --width/--height must be > 0 and --frames >= 0",
              file=sys.stderr)
        return EXIT_INVALID
    return args.func(args)


if __name__ == "__main__":
    sys.exit(main())
