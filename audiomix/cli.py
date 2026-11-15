"""Command line interface: argument parsing and exit-code mapping.

Exit codes::

    0 - success
    2 - invalid arguments or project configuration
    3 - decoding, resampling or graph processing failure
    4 - output file write failure
"""

from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path

from . import (
    EXIT_CONFIG,
    EXIT_OK,
    EXIT_PROCESS,
    EXIT_WRITE,
    MAX_BLOCK_FRAMES,
    MAX_SAMPLE_RATE,
    MIN_BLOCK_FRAMES,
    MIN_SAMPLE_RATE,
    AudioMixError,
    ConfigError,
    WriteError,
)
from .demo import run_demo
from .engine import write_float_wav
from .graph import MixGraph
from .model import load_project
from .stream import parity_error, render_stream


def _build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="audiomix",
        description="Deterministic offline/streaming audio mixing toolkit",
    )
    sub = parser.add_subparsers(dest="command", required=True)

    demo = sub.add_parser("demo", help="generate seeded fixtures and run both paths")
    demo.add_argument("--seed", type=int, default=17)
    demo.add_argument("--rate", type=int, default=48000)
    demo.add_argument("--seconds", type=float, default=3.0)
    demo.add_argument("--block-frames", type=int, default=257)
    demo.add_argument("--wav", required=True, help="output float WAV path")
    demo.add_argument("--report", required=True, help="output JSON report path")

    render = sub.add_parser(
        "render", help="render a JSON project (offline write + streaming check)"
    )
    render.add_argument("--project", required=True, help="input project JSON")
    render.add_argument("--wav", required=True, help="output float WAV path")
    render.add_argument("--report", required=True, help="output JSON report path")
    render.add_argument("--block-frames", type=int, default=None)
    return parser


def _validate_cli_rates(rate: int) -> None:
    if not MIN_SAMPLE_RATE <= rate <= MAX_SAMPLE_RATE:
        raise ConfigError(
            f"--rate must be within [{MIN_SAMPLE_RATE}, {MAX_SAMPLE_RATE}], got {rate}"
        )


def _validate_block(block: int) -> None:
    if not MIN_BLOCK_FRAMES <= block <= MAX_BLOCK_FRAMES:
        raise ConfigError(
            f"--block-frames must be within [{MIN_BLOCK_FRAMES}, {MAX_BLOCK_FRAMES}]"
        )


def _cmd_demo(args) -> dict:
    _validate_cli_rates(args.rate)
    _validate_block(args.block_frames)
    if args.seed < 0:
        raise ConfigError("--seed must be >= 0")
    if args.seconds <= 0:
        raise ConfigError("--seconds must be > 0")
    return run_demo(
        seed=args.seed,
        rate=args.rate,
        seconds=args.seconds,
        block_frames=args.block_frames,
        wav_path=args.wav,
        report_path=args.report,
    )


def _cmd_render(args) -> dict:
    project = load_project(args.project)
    block = args.block_frames or project.block_frames or 257
    _validate_block(block)
    with MixGraph(project) as graph:
        offline = write_float_wav(
            args.wav, graph, block, project.automation_sample_frames
        )
    stream_data, _ = render_stream(project, block)
    other_block = 1024 if block != 1024 else 511
    other_data, _ = render_stream(project, other_block)
    return {
        "frames": offline["frames"],
        "sample_rate": project.sample_rate,
        "block_frames": block,
        "clip_samples": offline["clip_samples"],
        "parity_max_abs": parity_error(stream_data, other_data),
        "tracks": offline["tracks"],
    }


def main(argv: list[str] | None = None) -> int:
    parser = _build_parser()
    args = parser.parse_args(argv)
    try:
        if args.command == "demo":
            report = _cmd_demo(args)
        else:
            report = _cmd_render(args)
        Path(args.report).parent.mkdir(parents=True, exist_ok=True)
        Path(args.report).write_text(
            json.dumps(report, indent=2, sort_keys=True) + "\n", encoding="utf-8"
        )
        print(json.dumps(report, indent=2, sort_keys=True))
        return EXIT_OK
    except AudioMixError as exc:
        # ConfigError maps to 2, WriteError to 4, every processing error to 3.
        if isinstance(exc, ConfigError):
            code = EXIT_CONFIG
        elif isinstance(exc, WriteError):
            code = EXIT_WRITE
        else:
            code = EXIT_PROCESS
        print(f"audiomix: error: {exc}", file=sys.stderr)
        return code
    except (OSError, ValueError) as exc:
        print(f"audiomix: error: {exc}", file=sys.stderr)
        return EXIT_CONFIG


if __name__ == "__main__":
    raise SystemExit(main())
