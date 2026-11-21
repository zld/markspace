"""Command line interface: ``python -m audiomix render ...``.

Exit codes: 0 success, 2 argument/manifest error, 3 audio processing or
file error, 4 offline/streaming divergence above tolerance.
"""

from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path

from .graph import GraphError, MixEngine
from .io import AudioIOError, write_wav
from .metrics import (
    automation_samples,
    build_report,
    count_clipped,
    limit_master,
    max_abs_diff,
)
from .model import ManifestError, load_manifest
from .offline import render_offline
from .stream import render_stream

MAX_ABS_DIFF_TOLERANCE = 1e-6

EXIT_OK = 0
EXIT_USAGE = 2
EXIT_AUDIO = 3
EXIT_DIVERGED = 4


def _positive_int(text: str) -> int:
    try:
        value = int(text)
    except ValueError:
        raise argparse.ArgumentTypeError(f"{text!r} is not an integer")
    if value <= 0:
        raise argparse.ArgumentTypeError("value must be a positive integer")
    return value


def _sample_times(text: str) -> list[float]:
    if not text.strip():
        return []
    times = []
    for part in text.split(","):
        try:
            t = float(part)
        except ValueError:
            raise argparse.ArgumentTypeError(f"bad sample time {part!r}")
        if t != t or t in (float("inf"), float("-inf")):
            raise argparse.ArgumentTypeError(f"non-finite sample time {part!r}")
        times.append(t)
    return times


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(prog="audiomix")
    sub = parser.add_subparsers(dest="command", required=True)
    r = sub.add_parser("render", help="render a manifest to a mixed WAV")
    r.add_argument("--manifest", required=True, help="path to scene manifest JSON")
    r.add_argument("--rate", type=_positive_int, required=True,
                   help="output sample rate in Hz")
    r.add_argument("--channels", type=int, choices=(1, 2), required=True,
                   help="output channel count")
    r.add_argument("--block-size", type=_positive_int, default=1024,
                   help="streaming block size in frames")
    r.add_argument("--mode", choices=("offline", "stream", "both"), default="both")
    r.add_argument("--sample-times", type=_sample_times, default=[],
                   help="comma-separated times (s) to sample automation curves")
    r.add_argument("--out", required=True, help="output WAV path")
    r.add_argument("--report", required=True, help="output report JSON path")
    return parser


def _cleanup(*paths: str) -> None:
    for p in paths:
        try:
            Path(p).unlink(missing_ok=True)
        except OSError:
            pass


def _print_summary(report: dict) -> None:
    print(f"tracks: {report['tracks']}")
    print(f"frames: {report['frames']}")
    for tm in report["track_metrics"]:
        print(f"  track {tm['index']}: peak={tm['peak']:.6f} rms={tm['rms']:.6f}")
    print(f"automation_samples: {len(report['automation_samples'])}")
    for s in report["automation_samples"]:
        print(f"  track {s['track']} @ {s['time']}s: {s['db']:.3f} dB"
              f" (gain {s['gain']:.6f})")
    print(f"clipped_samples: {report['clipped_samples']}")
    print(f"max_abs_diff: {report['max_abs_diff']:.3e}")


def cmd_render(args) -> int:
    try:
        manifest = load_manifest(args.manifest)
    except ManifestError as exc:
        print(f"manifest error: {exc}", file=sys.stderr)
        return EXIT_USAGE
    try:
        engine = MixEngine(manifest, args.rate, args.channels)
    except GraphError as exc:
        print(f"routing error: {exc}", file=sys.stderr)
        return EXIT_USAGE
    except AudioIOError as exc:
        print(f"audio error: {exc}", file=sys.stderr)
        return EXIT_AUDIO

    try:
        offline_master = stream_master = None
        if args.mode in ("offline", "both"):
            offline_master = render_offline(engine)
        if args.mode in ("stream", "both"):
            stream_master = render_stream(engine, args.block_size)
        master = offline_master if offline_master is not None else stream_master
        if offline_master is not None and stream_master is not None:
            diff = max_abs_diff(offline_master, stream_master)
        else:
            diff = 0.0

        clipped = count_clipped(master)
        report = build_report(
            rate=args.rate,
            channels=args.channels,
            frames=engine.total_frames,
            track_stats=engine.track_stats(),
            automation=automation_samples(manifest, args.sample_times),
            clipped=clipped,
            diff=diff,
        )
        write_wav(args.out, limit_master(master), args.rate)
        Path(args.report).write_text(json.dumps(report, indent=2), encoding="utf-8")
    except AudioIOError as exc:
        _cleanup(args.out, args.report)
        print(f"audio error: {exc}", file=sys.stderr)
        return EXIT_AUDIO
    except OSError as exc:
        _cleanup(args.out, args.report)
        print(f"file error: {exc}", file=sys.stderr)
        return EXIT_AUDIO

    _print_summary(report)
    if diff > MAX_ABS_DIFF_TOLERANCE:
        print(f"offline/stream divergence {diff:.3e} exceeds "
              f"{MAX_ABS_DIFF_TOLERANCE:.0e}", file=sys.stderr)
        return EXIT_DIVERGED
    return EXIT_OK


def main(argv=None) -> int:
    args = build_parser().parse_args(argv)
    if args.command == "render":
        return cmd_render(args)
    return EXIT_USAGE
