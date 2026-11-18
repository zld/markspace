"""Command line interface for the audiomix toolkit.

Exit codes:
    0 - success
    2 - manifest or argument error (ManifestError / argparse errors)
    3 - input/output or render failure (RenderError)
"""
from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path

import numpy as np
import soundfile as sf

from .model import AudioMixError, ManifestError, RenderError, parse_manifest
from .render import BlockRenderer
from .report import StatsAccumulator, build_report, write_report

# Offline mode renders the timeline in one block.  The iterator code is
# identical to streaming; only the block length changes.
_OFFLINE_BLOCK = 10 ** 9


class _MixArgumentParser(argparse.ArgumentParser):
    """Argument parser that reports machine-readable errors with code 2."""

    def error(self, message: str) -> None:
        print(json.dumps({"error": message, "field": "arguments"},
                         sort_keys=True), file=sys.stderr)
        raise SystemExit(2)


def _positive_int(value: str) -> int:
    try:
        ivalue = int(value)
    except ValueError as exc:
        raise argparse.ArgumentTypeError(
            f"must be a positive integer, got {value!r}") from exc
    if ivalue <= 0:
        raise argparse.ArgumentTypeError(
            f"must be a positive integer, got {value!r}")
    return ivalue


def _parse_sample_times(value: str) -> list[float]:
    times: list[float] = []
    for part in value.split(","):
        part = part.strip()
        if not part:
            continue
        try:
            t = float(part)
        except ValueError as exc:
            raise argparse.ArgumentTypeError(
                f"invalid sample time {part!r}") from exc
        if t < 0.0 or not np.isfinite(t):
            raise argparse.ArgumentTypeError(
                f"sample time must be finite and >= 0, got {part!r}")
        times.append(t)
    if not times:
        raise argparse.ArgumentTypeError("--sample-at needs at least one time")
    return times


def build_parser() -> argparse.ArgumentParser:
    parser = _MixArgumentParser(
        prog="audiomix",
        description="Deterministic local multi-track WAV mixer.")
    sub = parser.add_subparsers(dest="command", required=True)

    p = sub.add_parser("render", help="render a manifest to WAV + report")
    p.add_argument("--manifest", required=True, type=Path,
                   help="path to session manifest JSON")
    p.add_argument("--out", required=True, type=Path,
                   help="output WAV path (float PCM)")
    p.add_argument("--rate", type=_positive_int, default=None,
                   help="override output sample rate")
    p.add_argument("--channels", type=_positive_int, default=None,
                   help="override output channel count")
    p.add_argument("--block-frames", type=_positive_int, default=1024,
                   help="streaming block length in frames (default 1024)")
    p.add_argument("--mode", choices=("stream", "offline"),
                   default="stream",
                   help="both modes share one renderer; offline uses one "
                        "timeline-spanning block")
    p.add_argument("--report", type=Path, default=None,
                   help="write statistics JSON to this path")
    p.add_argument("--sample-at", type=_parse_sample_times, default=None,
                   metavar="T[,T...]",
                   help="report automation gain at these seconds")
    p.add_argument("--limit", action="store_true",
                   help="hard-clip samples beyond [-1, 1] before writing")
    return parser


def _emit_error(err: AudioMixError) -> int:
    payload = err.as_dict() if hasattr(err, "as_dict") else \
        {"error": str(err)}
    print(json.dumps(payload, sort_keys=True), file=sys.stderr)
    return err.code


def run_render(args: argparse.Namespace) -> int:
    timeline = parse_manifest(args.manifest, output_rate=args.rate,
                              channels=args.channels)
    block_frames = (args.block_frames if args.mode == "stream"
                    else _OFFLINE_BLOCK)
    renderer = BlockRenderer(timeline, block_frames)
    total = renderer.total_frames

    track_stats = [StatsAccumulator(t.name) for t in timeline.tracks]
    bus_stats = StatsAccumulator("bus")

    out_path = args.out
    tmp_path = out_path.with_name(out_path.name + ".part")
    writer: sf.SoundFile | None = None
    try:
        out_path.parent.mkdir(parents=True, exist_ok=True)
        writer = sf.SoundFile(
            str(tmp_path), mode="w", samplerate=timeline.output_rate,
            channels=timeline.channels, subtype="FLOAT", format="WAV")
        for _start, block, track_blocks in renderer:
            for acc, node_block in zip(track_stats, track_blocks):
                acc.add(node_block)
            bus_stats.add(block)
            written = np.clip(block, -1.0, 1.0) if args.limit else block
            writer.write(written.astype(np.float32, copy=False))
        renderer.close()
        writer.close()
        writer = None
        tmp_path.replace(out_path)
    except (OSError, RuntimeError, AudioMixError) as exc:
        if writer is not None:
            writer.close()
        tmp_path.unlink(missing_ok=True)
        if isinstance(exc, AudioMixError):
            return _emit_error(exc)
        return _emit_error(RenderError(f"render failed: {exc}"))

    stats_by_name = {t.name: acc
                     for t, acc in zip(timeline.tracks, track_stats)}
    report = build_report(timeline, stats_by_name, bus_stats, total,
                          sample_times=args.sample_at or [],
                          limited=args.limit)
    if args.report is not None:
        try:
            write_report(report, args.report)
        except RenderError as exc:
            return _emit_error(exc)

    _print_summary(timeline, total, report)
    return 0


def _print_summary(timeline, total: int, report: dict) -> None:
    print(f"tracks: {len(timeline.tracks)}")
    print(f"output_frames: {total}")
    print(f"output_rate: {timeline.output_rate}")
    for name, stats in report["tracks"].items():
        print(f"track {name}: peak={stats['peak']:.6g} "
              f"rms={stats['rms']:.6g} "
              f"active_frames={stats['active_frames']} "
              f"clipped_samples={stats['clipped_samples']}")
    for sample in report["automation_samples"]:
        print(f"automation: track={sample['track']} "
              f"time_sec={sample['time_sec']} gain={sample['gain']:.6g}")


def main(argv: list[str] | None = None) -> int:
    parser = build_parser()
    args = parser.parse_args(argv)
    if args.command == "render":
        try:
            return run_render(args)
        except AudioMixError as exc:
            return _emit_error(exc)
    return 2


if __name__ == "__main__":
    sys.exit(main())
