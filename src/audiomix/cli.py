"""Command-line entry point.

Commands
--------
``generate-sample``  write deterministic demo WAVs and a manifest.
``render``           load a manifest, render offline or streaming, write WAV
                     and/or JSON report, print a terminal summary.
``compare``          render the same manifest both ways and verify that the
                     per-sample difference stays within tolerance.

Exit codes: ``0`` success, ``2`` input/processing error, ``3`` internal error.
The WAV and report of a render are staged together and committed atomically,
so a failed run leaves no artifact that looks complete.
"""

from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path
from typing import Sequence

import numpy as np

from .artifacts import ArtifactSet
from .errors import (
    AudioMixError,
    EXIT_INTERNAL,
    EXIT_OK,
    ManifestError,
    OutputError,
)
from .mixer import MixGraph, parse_policy
from .model import Session, parse_manifest
from .offline import render_offline, render_offline_to_file
from .report import build_report, format_summary, write_json
from .sample import generate_sample
from .stream import render_stream

DEFAULT_TOLERANCE = 1e-6
DEFAULT_RATE = 48000


class _UsageError(AudioMixError):
    """A CLI argument combination that argparse cannot express."""


def _load_manifest(path: Path) -> Session:
    if not path.is_file():
        raise ManifestError(f"manifest not found: {path}")
    try:
        raw = path.read_text(encoding="utf-8")
    except OSError as exc:
        raise ManifestError(f"cannot read manifest {path}: {exc}") from exc
    try:
        data = json.loads(raw)
    except json.JSONDecodeError as exc:
        raise ManifestError(f"manifest {path} is not valid JSON: {exc}") from exc
    return parse_manifest(data, path)


def _build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(prog="audiomix", description=__doc__)
    sub = parser.add_subparsers(dest="command", required=True)

    gen = sub.add_parser("generate-sample", help="write demo audio and manifest")
    gen.add_argument("--dir", required=True, type=Path, help="output directory")

    render = sub.add_parser("render", help="render a session manifest")
    render.add_argument("--manifest", required=True, type=Path)
    render.add_argument("--output", type=Path, help="destination WAV")
    render.add_argument("--report", type=Path, help="destination JSON report")
    render.add_argument("--rate", type=int, default=None, help="project sample rate")
    render.add_argument("--block-frames", type=int, default=None)
    render.add_argument("--mode", choices=("offline", "stream"), default="offline")
    render.add_argument("--clip", choices=("limit", "report"), default="report")
    render.add_argument("--quiet", action="store_true", help="suppress the summary")

    compare = sub.add_parser("compare", help="check offline and stream agree")
    compare.add_argument("--manifest", required=True, type=Path)
    compare.add_argument("--rate", type=int, default=None)
    compare.add_argument("--block-frames", type=int, default=257)
    compare.add_argument("--clip", choices=("limit", "report"), default="report")
    compare.add_argument("--tolerance", type=float, default=DEFAULT_TOLERANCE)
    compare.add_argument("--output", type=Path, help="optional WAV of the stream render")
    return parser


def _protected_paths(session: Session) -> list[Path]:
    return [track.path for track in session.tracks]


def _cmd_generate(args: argparse.Namespace) -> int:
    manifest = generate_sample(args.dir)
    print(f"sample session written to {manifest}")
    return EXIT_OK


def _cmd_render(args: argparse.Namespace) -> int:
    session = _load_manifest(args.manifest)
    rate = args.rate or session.sample_rate or DEFAULT_RATE
    if rate <= 0:
        raise _UsageError(f"--rate must be positive, got {rate}")
    if args.output is None and args.report is None:
        raise _UsageError("render needs at least one of --output or --report")
    if args.mode == "stream":
        if args.block_frames is None:
            raise _UsageError("--block-frames is required in stream mode")
        if args.block_frames <= 0:
            raise _UsageError(f"--block-frames must be positive, got {args.block_frames}")
    policy = parse_policy(args.clip)
    protected = _protected_paths(session)

    graph = MixGraph(session, rate, policy)
    targets: dict[str, Path] = {}
    if args.output is not None:
        targets["wav"] = args.output
    if args.report is not None:
        targets["report"] = args.report

    with ArtifactSet(targets, protected) as artifacts:
        if args.mode == "offline":
            result = render_offline_to_file(graph, artifacts.temp("wav")) \
                if args.output is not None else render_offline(graph, policy)
        else:
            result = render_stream(graph, args.block_frames, artifacts.temp("wav")) \
                if args.output is not None else render_stream(graph, args.block_frames)
        report = build_report(
            graph, result.stats, rate, args.mode, args.block_frames, policy, result.frames
        )
        if args.report is not None:
            write_json(report, artifacts.temp("report"))
        artifacts.commit()
    if not args.quiet:
        print(format_summary(report))
    return EXIT_OK


def _cmd_compare(args: argparse.Namespace) -> int:
    session = _load_manifest(args.manifest)
    rate = args.rate or session.sample_rate or DEFAULT_RATE
    if rate <= 0:
        raise _UsageError(f"--rate must be positive, got {rate}")
    if args.block_frames <= 0:
        raise _UsageError(f"--block-frames must be positive, got {args.block_frames}")
    policy = parse_policy(args.clip)
    protected = _protected_paths(session)
    graph = MixGraph(session, rate, policy)
    offline = render_offline(graph, policy)
    paths: dict[str, Path] = {}
    if args.output is not None:
        paths["wav"] = args.output
    with ArtifactSet(paths, protected) as artifacts:
        if args.output is not None:
            streamed = render_stream(graph, args.block_frames, artifacts.temp("wav"))
        else:
            streamed = render_stream(graph, args.block_frames)
        diff = _max_abs_diff(offline, streamed)
        if offline.frames == streamed.frames and diff <= args.tolerance:
            artifacts.commit()
    if offline.frames != streamed.frames:
        print(
            f"frame count mismatch: offline {offline.frames}, stream {streamed.frames}",
            file=sys.stderr,
        )
        return 2
    print(
        f"frames: {offline.frames}  max abs diff: {diff:.3e}  "
        f"tolerance: {args.tolerance:.1e}"
    )
    if diff > args.tolerance:
        print("FAIL: offline and stream differ", file=sys.stderr)
        return 2
    print("OK: offline and stream agree")
    return EXIT_OK


def _max_abs_diff(offline, streamed) -> float:
    if not offline.frames:
        return 0.0
    left = np.asarray(offline.samples)
    right = np.asarray(streamed.samples)
    if left.shape != right.shape:
        return float("inf")
    return float(np.max(np.abs(left - right)))


def main(argv: Sequence[str] | None = None) -> int:
    parser = _build_parser()
    args = parser.parse_args(argv)
    try:
        if args.command == "generate-sample":
            return _cmd_generate(args)
        if args.command == "render":
            return _cmd_render(args)
        if args.command == "compare":
            return _cmd_compare(args)
        raise AudioMixError(f"unknown command {args.command}")
    except AudioMixError as exc:
        print(f"error: {exc.message}", file=sys.stderr)
        return exc.exit_code
    except (OutputError, ValueError) as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 2
    except Exception as exc:  # noqa: BLE001 - map to documented internal code
        print(f"internal error: {exc}", file=sys.stderr)
        return EXIT_INTERNAL


if __name__ == "__main__":
    raise SystemExit(main())
