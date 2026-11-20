"""Command line interface: ``render`` and ``compare`` subcommands.

Exit codes: 0 success, 2 argument/manifest error, 3 audio I/O error,
4 render comparison beyond tolerance.
"""

from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path

import numpy as np
import soundfile as sf

from .manifest import load_manifest
from .model import AudioIOError, ManifestError
from .render import render

EXIT_OK = 0
EXIT_USAGE = 2
EXIT_AUDIO = 3
EXIT_MISMATCH = 4


def _positive_int(value: str) -> int:
    try:
        result = int(value)
    except ValueError:
        raise argparse.ArgumentTypeError(f"not an integer: {value!r}")
    if result <= 0:
        raise argparse.ArgumentTypeError(f"must be positive: {value!r}")
    return result


def _positive_float(value: str) -> float:
    try:
        result = float(value)
    except ValueError:
        raise argparse.ArgumentTypeError(f"not a number: {value!r}")
    if not np.isfinite(result) or result <= 0:
        raise argparse.ArgumentTypeError(f"must be a positive finite number: {value!r}")
    return result


def _build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="mixforge",
        description="Deterministic offline/streaming audio mixdown tool.",
    )
    sub = parser.add_subparsers(dest="command", required=True)

    render_p = sub.add_parser("render", help="render a manifest to a WAV file")
    render_p.add_argument("--manifest", required=True, help="path to manifest JSON")
    render_p.add_argument("--out", required=True, help="output WAV path")
    render_p.add_argument("--mode", choices=("offline", "stream"), required=True)
    render_p.add_argument("--rate", type=_positive_int, required=True,
                          help="output sample rate")
    render_p.add_argument("--duration", type=_positive_float, required=True,
                          help="timeline duration in seconds")
    render_p.add_argument("--block-size", type=_positive_int, default=1024)
    render_p.add_argument("--curve-step-ms", type=_positive_float, default=100.0)

    compare_p = sub.add_parser("compare", help="compare two WAV files")
    compare_p.add_argument("--left", required=True)
    compare_p.add_argument("--right", required=True)
    compare_p.add_argument("--tolerance", type=float, default=1e-6)
    return parser


def _cmd_render(args: argparse.Namespace) -> int:
    try:
        project = load_manifest(args.manifest)
        report = render(
            project,
            out_path=args.out,
            sample_rate=args.rate,
            duration=args.duration,
            mode=args.mode,
            block_size=args.block_size,
            curve_step_ms=args.curve_step_ms,
        )
    except ManifestError as exc:
        print(f"error: {exc}", file=sys.stderr)
        return EXIT_USAGE
    except ValueError as exc:
        print(f"error: {exc}", file=sys.stderr)
        return EXIT_USAGE
    except AudioIOError as exc:
        print(f"error: {exc}", file=sys.stderr)
        return EXIT_AUDIO
    json.dump(report, sys.stdout, indent=2)
    sys.stdout.write("\n")
    return EXIT_OK


def _read_wav(path: str) -> tuple[np.ndarray, int]:
    try:
        data, rate = sf.read(path, dtype="float64", always_2d=True)
    except Exception as exc:
        raise AudioIOError(f"cannot read {path}: {exc}") from exc
    return data, rate


def _cmd_compare(args: argparse.Namespace) -> int:
    try:
        left, left_rate = _read_wav(args.left)
        right, right_rate = _read_wav(args.right)
    except AudioIOError as exc:
        print(f"error: {exc}", file=sys.stderr)
        return EXIT_AUDIO
    report: dict = {
        "left": str(Path(args.left)),
        "right": str(Path(args.right)),
        "tolerance": args.tolerance,
    }
    if left_rate != right_rate or left.shape != right.shape:
        report.update(
            match=False,
            reason="shape/rate mismatch",
            left={"rate": left_rate, "frames": int(left.shape[0]),
                  "channels": int(left.shape[1])},
            right={"rate": right_rate, "frames": int(right.shape[0]),
                   "channels": int(right.shape[1])},
        )
        json.dump(report, sys.stdout, indent=2)
        sys.stdout.write("\n")
        return EXIT_MISMATCH
    max_abs_delta = (
        float(np.abs(left - right).max()) if left.size else 0.0
    )
    match = max_abs_delta <= args.tolerance
    report.update(
        match=match,
        frames=int(left.shape[0]),
        channels=int(left.shape[1]),
        sample_rate=left_rate,
        max_abs_delta=max_abs_delta,
    )
    json.dump(report, sys.stdout, indent=2)
    sys.stdout.write("\n")
    return EXIT_OK if match else EXIT_MISMATCH


def main(argv: list[str] | None = None) -> int:
    parser = _build_parser()
    args = parser.parse_args(argv)
    if args.command == "render":
        return _cmd_render(args)
    if args.command == "compare":
        return _cmd_compare(args)
    parser.error(f"unknown command {args.command!r}")
    return EXIT_USAGE


if __name__ == "__main__":
    raise SystemExit(main())
