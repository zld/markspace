"""Command-line interface: demo fixture generation and manifest rendering."""

from __future__ import annotations

import argparse
import json
import math
import sys
from pathlib import Path

import numpy as np
import soundfile as sf

from . import __version__
from .engine import render_project
from .graph import AudioIOError, ConsistencyError
from .model import (
    DEFAULT_CHUNK_FRAMES,
    MAX_DURATION_SECONDS,
    ManifestError,
    parse_manifest,
    resolve_track_paths,
)
from .report import write_json_atomic

EXIT_OK = 0
EXIT_USAGE = 2
EXIT_AUDIO = 3
EXIT_CONSISTENCY = 4

DEFAULT_DURATION = 2.0
DEFAULT_AUTOMATION_POINTS = 3
DEMO_CHUNK_FRAMES = 257


class _Parser(argparse.ArgumentParser):
    """Argument parser that reports usage errors with exit code 2."""

    def error(self, message: str) -> None:  # noqa: D401 - argparse contract
        self.print_usage(sys.stderr)
        self.exit(EXIT_USAGE, f"{self.prog}: error: {message}\n")


def _build_parser() -> argparse.ArgumentParser:
    parser = _Parser(prog="audiomix", description="Deterministic local multi-track mixdown engine")
    parser.add_argument("--version", action="version", version=f"audiomix {__version__}")
    sub = parser.add_subparsers(dest="command", required=True)

    render = sub.add_parser("render", help="render a JSON manifest to a stereo WAV")
    render.add_argument("--manifest", required=True, help="path to the project manifest JSON")
    render.add_argument("--output", required=True, help="destination WAV path")
    render.add_argument("--chunk-frames", type=int, default=DEFAULT_CHUNK_FRAMES,
                        help="streaming block size in frames (default: %(default)s)")
    render.add_argument("--automation-points", type=int, default=DEFAULT_AUTOMATION_POINTS,
                        help="automation samples reported per track (default: %(default)s)")
    render.add_argument("--automation-frame", type=int, action="append", dest="automation_frames",
                        metavar="FRAME",
                        help="sample every automation curve at FRAME (repeatable)")
    render.add_argument("--json", action="store_true", help="print the report as JSON")

    demo = sub.add_parser("demo", help="generate deterministic fixtures and render them")
    demo.add_argument("--workdir", required=True, help="directory for fixtures and output")
    demo.add_argument("--chunk-frames", type=int, default=DEMO_CHUNK_FRAMES,
                      help="streaming block size in frames (default: %(default)s)")
    demo.add_argument("--duration", type=float, default=DEFAULT_DURATION,
                      help="fixture duration in seconds (default: %(default)s)")
    demo.add_argument("--automation-points", type=int, default=DEFAULT_AUTOMATION_POINTS,
                      help="automation samples reported per track (default: %(default)s)")
    demo.add_argument("--automation-frame", type=int, action="append", dest="automation_frames",
                      metavar="FRAME",
                      help="sample every automation curve at FRAME (repeatable)")
    demo.add_argument("--json", action="store_true", help="print the report as JSON")
    return parser


def _write_fixture(path: Path, data: np.ndarray, sample_rate: int) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    sf.write(str(path), data, sample_rate, subtype="FLOAT", format="WAV")


def generate_demo_fixtures(workdir: Path, duration: float) -> dict:
    """Create two deterministic tracks with different sample rates.

    Track ``voice`` is stereo 48 kHz, track ``ambience`` is mono 44.1 kHz.
    Both carry a 220 Hz tone so the summed bus exceeds full scale and the
    pre-limiter clip counter is exercised.
    """
    if not math.isfinite(duration) or duration <= 0:
        raise ValueError("duration must be a finite positive number")
    if duration > MAX_DURATION_SECONDS:
        raise ValueError(
            f"duration must not exceed {MAX_DURATION_SECONDS}s (two hours)"
        )
    frames_a = int(duration * 48000 + 0.5)
    frames_b = int(duration * 44100 + 0.5)
    if frames_a <= 0 or frames_b <= 0:
        raise ValueError("duration is too short to produce any audio frames")
    t_a = np.arange(frames_a, dtype=np.float64) / 48000.0
    t_b = np.arange(frames_b, dtype=np.float64) / 44100.0

    tone_a = 0.85 * np.sin(2.0 * np.pi * 220.0 * t_a)
    voice = np.column_stack([tone_a, 0.75 * tone_a])

    tone_b = 0.85 * np.sin(2.0 * np.pi * 220.0 * t_b)
    # A deterministic amplitude envelope keeps the mono fixture non-trivial.
    envelope = 0.6 + 0.4 * np.cos(2.0 * np.pi * 0.5 * t_b) ** 2
    ambience = tone_b * envelope

    _write_fixture(workdir / "voice.wav", voice, 48000)
    _write_fixture(workdir / "ambience.wav", ambience, 44100)

    return {
        "sample_rate": 48000,
        "channels": 2,
        "tracks": [
            {
                "name": "voice",
                "path": "voice.wav",
                "gain_db": 0.0,
                "pan": 0.0,
                "automation": [
                    {"t": 0.0, "gain_db": 0.0},
                    {"t": duration / 2.0, "gain_db": -6.0},
                    {"t": duration, "gain_db": 0.0},
                ],
            },
            {
                "name": "ambience",
                "path": "ambience.wav",
                "gain_db": 0.0,
                "pan": 0.0,
                "automation": [
                    {"t": 0.0, "gain_db": 0.0},
                    {"t": duration / 2.0, "gain_db": -6.0},
                    {"t": duration, "gain_db": 0.0},
                ],
            },
        ],
    }


def _render_manifest(manifest_path: Path, output_path: Path, chunk_frames: int,
                     automation_points: int,
                     automation_frames: list[int] | None = None) -> dict:
    try:
        raw = json.loads(Path(manifest_path).read_text(encoding="utf-8"))
    except FileNotFoundError:
        raise ManifestError(f"manifest not found: {manifest_path}") from None
    except OSError as exc:
        raise ManifestError(f"cannot read manifest {manifest_path}: {exc}") from exc
    except json.JSONDecodeError as exc:
        raise ManifestError(f"invalid JSON in {manifest_path}: {exc}") from exc

    project = parse_manifest(raw)
    paths = resolve_track_paths(project, Path(manifest_path).resolve().parent)
    report = render_project(project, paths, output_path, chunk_frames, automation_points,
                            automation_frames=automation_frames)
    return report.to_dict()


def _print_human(report: dict) -> None:
    print(f"frames_out: {report['frames_out']}")
    print(f"output: {report['output']}")
    for track in report["tracks"]:
        print(
            f"track {track['name']}: sample_rate_in={track['sample_rate_in']} "
            f"peak={track['peak']:.6f} rms={track['rms']:.6f}"
        )
    print(f"clip_count: {report['clip_count']}")
    print(f"offline_stream_max_abs_diff: {report['offline_stream_max_abs_diff']:.3e}")


def _run(args: argparse.Namespace) -> int:
    if args.chunk_frames <= 0:
        print("audiomix: error: --chunk-frames must be a positive integer", file=sys.stderr)
        return EXIT_USAGE
    if args.automation_points <= 0:
        print("audiomix: error: --automation-points must be a positive integer", file=sys.stderr)
        return EXIT_USAGE

    try:
        if args.command == "demo":
            workdir = Path(args.workdir)
            workdir.mkdir(parents=True, exist_ok=True)
            manifest = generate_demo_fixtures(workdir, args.duration)
            manifest_path = workdir / "project.json"
            write_json_atomic(manifest_path, manifest)
            report = _render_manifest(
                manifest_path, workdir / "mix.wav", args.chunk_frames,
                args.automation_points, args.automation_frames,
            )
        else:
            report = _render_manifest(
                Path(args.manifest), Path(args.output), args.chunk_frames,
                args.automation_points, args.automation_frames,
            )
    except ManifestError as exc:
        print(f"audiomix: manifest error: {exc}", file=sys.stderr)
        return EXIT_USAGE
    except (AudioIOError, sf.LibsndfileError, OSError) as exc:
        print(f"audiomix: audio error: {exc}", file=sys.stderr)
        return EXIT_AUDIO
    except ConsistencyError as exc:
        print(f"audiomix: consistency error: {exc}", file=sys.stderr)
        return EXIT_CONSISTENCY
    except ValueError as exc:
        print(f"audiomix: error: {exc}", file=sys.stderr)
        return EXIT_USAGE

    if args.json:
        print(json.dumps(report, ensure_ascii=False, indent=2))
    else:
        _print_human(report)
    return EXIT_OK


def main(argv: list[str] | None = None) -> int:
    parser = _build_parser()
    args = parser.parse_args(argv)
    return _run(args)


if __name__ == "__main__":  # pragma: no cover
    sys.exit(main())
