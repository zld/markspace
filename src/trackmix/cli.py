"""Command line interface: argument parsing, reporting, exit codes.

Subcommands
-----------
``demo``  generate deterministic WAV fixtures + manifest, render offline,
          optionally verify the streaming path, and print a JSON report.
``mix``   render a user-supplied manifest the same way.

Exit codes: 0 ok, 2 manifest/parameter, 3 audio/resample, 4 write,
5 offline/streaming verification; anything unexpected is 1.
"""

from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path

import numpy as np

from . import samples
from .automation import GainCurve
from .errors import ManifestError, TrackMixError
from .graph import build_graph
from .manifest import Manifest, Track, load_manifest
from .streaming import verify_against_offline

DEFAULT_RATES = (48000, 44100, 32000, 22050)
DEFAULT_CHANNELS = (2, 1, 2, 1)


class _Parser(argparse.ArgumentParser):
    """Argument parser whose usage errors map to the spec's exit code 2."""

    def error(self, message: str) -> None:
        raise ManifestError(f"parameter error: {message}")


# ---------------------------------------------------------------------- #
# Demo fixture construction
# ---------------------------------------------------------------------- #
def _build_demo_manifest(
    args: argparse.Namespace, work_dir: Path
) -> Manifest:
    src_dir = work_dir / "sources"
    generated: list[samples.GeneratedTrack] = []
    track_entries: list[Track] = []
    for ordinal in range(args.tracks):
        flavour = samples.FLAVOURS[ordinal % len(samples.FLAVOURS)]
        rate = DEFAULT_RATES[ordinal % len(DEFAULT_RATES)]
        channels = DEFAULT_CHANNELS[ordinal % len(DEFAULT_CHANNELS)]
        # Spread sources across the timeline, leaving headroom at the end.
        span = max(1, args.frames // args.tracks)
        start = (ordinal * span) // 2
        src_frames = max(
            8, int(args.frames * 0.55 * rate / args.rate)
        )
        gen = samples.generate_track(
            src_dir,
            args.seed,
            ordinal,
            rate,
            src_frames,
            channels,
            flavour,
            args.stress_clip,
        )
        generated.append(gen)
        matrix = _demo_matrix(channels, args.channels)
        points = _demo_automation(ordinal, args)
        track_entries.append(
            Track(
                id=f"trk{ordinal:02d}",
                file=gen.path,
                start_frame=start,
                source_frames=src_frames,
                source_rate=rate,
                source_channels=channels,
                matrix=matrix,
                curve=GainCurve(points),
                samples=_load_generated(gen),
            )
        )
    return Manifest(
        target_rate=args.rate,
        total_frames=args.frames,
        channels=args.channels,
        tracks=tuple(track_entries),
        root=work_dir,
    )


def _load_generated(gen: samples.GeneratedTrack) -> np.ndarray:
    import soundfile as sf

    data, _ = sf.read(str(gen.path), dtype="float64", always_2d=True)
    return data


def _demo_matrix(source_channels: int, bus_channels: int) -> np.ndarray:
    if source_channels == bus_channels:
        return np.eye(source_channels, dtype=np.float64)
    if source_channels == 1:  # mono -> stereo: identical legs
        return np.ones((1, bus_channels), dtype=np.float64)
    # stereo -> mono: fixed equal fold, never an implicit guess
    return np.full((source_channels, 1), 1.0 / source_channels,
                   dtype=np.float64)


def _demo_automation(ordinal: int, args: argparse.Namespace):
    points = []
    frame = 0
    level = -6.0 - (ordinal % 3) * 2.0
    step = max(1, args.automation_step)
    while frame <= args.frames:
        points.append((frame, level))
        level = -24.0 if level > -12.0 else -3.0
        frame += step
    return points


# ---------------------------------------------------------------------- #
# Reporting
# ---------------------------------------------------------------------- #
def _automation_samples(manifest: Manifest) -> list[dict]:
    rows: list[dict] = []
    for track in manifest.tracks:
        for frame, gain_db in track.curve.points:
            rows.append(
                {"track_id": track.id, "frame": int(frame),
                 "gain_db": float(gain_db)}
            )
    return rows


def _execute(manifest: Manifest, args: argparse.Namespace) -> dict:
    from .io_utils import write_json, write_wav
    from .offline import evaluate_offline

    graph = build_graph(manifest)
    # Evaluate and verify before any file is written, so the persisted
    # report always contains the streaming check when requested.
    master, report = evaluate_offline(
        graph, manifest.total_frames, manifest.target_rate
    )
    report["automation_samples"] = _automation_samples(manifest)
    if args.verify_stream:
        report["stream_check"] = verify_against_offline(
            master, graph, manifest.total_frames, args.block_frames
        )
    output_dir = Path(args.output_dir)
    write_wav(
        output_dir / f"{args.stem}.wav",
        master,
        manifest.target_rate,
        args.force,
    )
    write_json(output_dir / f"{args.stem}.json", report, args.force)
    return report


# ---------------------------------------------------------------------- #
# Argument parsing
# ---------------------------------------------------------------------- #
def build_parser() -> argparse.ArgumentParser:
    parser = _Parser(
        prog="trackmix",
        description="Deterministic local multi-rate audio mixing tool",
    )
    sub = parser.add_subparsers(dest="command", required=True,
                                parser_class=_Parser)

    demo = sub.add_parser("demo", help="generate fixtures and run a demo mix")
    demo.add_argument("--seed", type=int, default=23)
    demo.add_argument("--tracks", type=int, default=4)
    demo.add_argument("--frames", type=int, default=96000)
    demo.add_argument("--rate", type=int, default=48000)
    demo.add_argument("--channels", type=int, default=2)
    demo.add_argument("--block-frames", type=int, default=257)
    demo.add_argument("--automation-step", type=int, default=12000)
    demo.add_argument("--stress-clip", action="store_true")
    demo.add_argument("--verify-stream", action="store_true")
    demo.add_argument("--output-dir", default="out/demo")
    demo.add_argument("--stem", default="master")
    demo.add_argument("--force", action="store_true")

    mix = sub.add_parser("mix", help="mix a manifest file")
    mix.add_argument("manifest", type=Path)
    mix.add_argument("--block-frames", type=int, default=257)
    mix.add_argument("--verify-stream", action="store_true")
    mix.add_argument("--output-dir", default="out/mix")
    mix.add_argument("--stem", default="master")
    mix.add_argument("--force", action="store_true")
    return parser


def _require_range(name: str, value: int, low: int, high: int) -> int:
    if not low <= value <= high:
        raise ManifestError(
            f"parameter {name}={value} outside allowed range [{low}, {high}]"
        )
    return value


def main(argv: list[str] | None = None) -> int:
    parser = build_parser()
    try:
        args = parser.parse_args(argv)
        _validate_args(args)
        if args.command == "demo":
            work_dir = Path(args.output_dir)
            manifest = _build_demo_manifest(args, work_dir)
            _write_demo_manifest(manifest, work_dir, args.force)
        else:
            manifest = load_manifest(args.manifest)
        report = _execute(manifest, args)
    except TrackMixError as exc:
        print(f"error: {exc}", file=sys.stderr)
        return exc.exit_code
    print(json.dumps(report, indent=2, sort_keys=True))
    return 0


def _validate_args(args: argparse.Namespace) -> None:
    """Range checks that must surface as parameter errors (exit code 2)."""
    _require_range("--block-frames", args.block_frames, 1, 200_000_000)
    if args.command == "demo":
        _require_range("--tracks", args.tracks, 1, 16)
        _require_range("--frames", args.frames, 1, 200_000_000)
        _require_range("--rate", args.rate, 1, 1_000_000)
        _require_range("--channels", args.channels, 1, 16)
        _require_range("--automation-step", args.automation_step, 1,
                       200_000_000)


def _write_demo_manifest(manifest: Manifest, work_dir: Path,
                         force: bool) -> None:
    from .io_utils import write_json

    payload = {
        "target_rate": manifest.target_rate,
        "total_frames": manifest.total_frames,
        "channels": manifest.channels,
        "tracks": [
            {
                "id": t.id,
                "file": str(t.file.relative_to(work_dir)),
                "start_frame": t.start_frame,
                "channel_matrix": t.matrix.tolist(),
                "automation": [
                    {"frame": f, "gain_db": g} for f, g in t.curve.points
                ],
            }
            for t in manifest.tracks
        ],
    }
    write_json(work_dir / "manifest.json", payload, force)
