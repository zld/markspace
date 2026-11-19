"""Command line interface: parse arguments, wire modules, run simulations.

Exit codes: 0 success, 1 simulation failure, 2 invalid arguments or input.
"""
from __future__ import annotations

import argparse
import sys
from pathlib import Path

from .estimator import ESTIMATOR_MODES, EstimatorConfig, ThroughputEstimator
from .model import BitrateLadder, ConfigError, StreamConfig
from .policy import PolicyConfig, RatePolicy
from .report import format_jsonl, format_table
from .simulator import SimulationError, simulate
from .trace import trace_from_json

EXIT_OK = 0
EXIT_SIMULATION_FAILED = 1
EXIT_USAGE = 2


def _parse_bitrates(text: str) -> tuple[int, ...]:
    try:
        levels = tuple(int(part) for part in text.split(",") if part.strip())
    except ValueError as exc:
        raise ConfigError(f"invalid --bitrates list: {text!r}") from exc
    if not levels:
        raise ConfigError("--bitrates must not be empty")
    return levels


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="abr_engine",
        description="Reproducible ABR switching decision engine (local, offline).",
    )
    sub = parser.add_subparsers(dest="command", required=True)
    sim = sub.add_parser("simulate", help="run an ABR simulation over a trace")
    sim.add_argument("--trace", required=True, help="trace JSON file")
    sim.add_argument("--segments", type=int, required=True, help="segment count")
    sim.add_argument("--segment-duration", type=float, required=True,
                     help="segment duration in seconds")
    sim.add_argument("--bitrates", required=True,
                     help="comma-separated increasing bitrate levels in bps")
    sim.add_argument("--initial-buffer", type=float, default=4.0,
                     help="pre-roll buffer in seconds (default: 4)")
    sim.add_argument("--max-buffer", type=float, default=20.0,
                     help="buffer cap in seconds (default: 20)")
    sim.add_argument("--estimator", choices=ESTIMATOR_MODES, default="window",
                     help="throughput estimator mode (default: window)")
    sim.add_argument("--window", type=int, default=5,
                     help="sliding window size in samples (default: 5)")
    sim.add_argument("--alpha", type=float, default=0.3,
                     help="EMA smoothing factor (default: 0.3)")
    sim.add_argument("--safety-margin", type=float, default=0.85,
                     help="usable fraction of the estimate (default: 0.85)")
    sim.add_argument("--hysteresis", type=int, default=3,
                     help="consecutive estimates required to switch up (default: 3)")
    sim.add_argument("--buffer-safety", type=float, default=4.0,
                     help="buffer level below which the budget shrinks (default: 4)")
    sim.add_argument("--output", choices=("jsonl", "table"), default="table",
                     help="output format (default: table)")
    return parser


def _run_simulate(args: argparse.Namespace) -> str:
    # Validate everything before simulating: no partial fake summaries.
    ladder = BitrateLadder(_parse_bitrates(args.bitrates))
    stream = StreamConfig(
        segment_duration_s=args.segment_duration,
        segment_count=args.segments,
        initial_buffer_s=args.initial_buffer,
        max_buffer_s=args.max_buffer,
    )
    est_config = EstimatorConfig(mode=args.estimator, window=args.window,
                                 alpha=args.alpha)
    pol_config = PolicyConfig(safety_margin=args.safety_margin,
                              hysteresis=args.hysteresis,
                              buffer_safety_s=args.buffer_safety)
    try:
        text = Path(args.trace).read_text(encoding="utf-8")
    except OSError as exc:
        raise ConfigError(f"cannot read trace file: {exc}") from exc
    trace = trace_from_json(text)

    estimator = ThroughputEstimator(est_config, conservative_bps=ladder.lowest_bps)
    policy = RatePolicy(ladder, pol_config)
    result = simulate(trace, stream, policy, estimator)
    return format_jsonl(result) if args.output == "jsonl" else format_table(result)


def main(argv: list[str] | None = None) -> int:
    args = build_parser().parse_args(argv)
    try:
        if args.command == "simulate":
            output = _run_simulate(args)
        else:  # pragma: no cover - argparse enforces a valid command
            raise ConfigError(f"unknown command: {args.command}")
    except ConfigError as exc:
        print(f"error: {exc}", file=sys.stderr)
        return EXIT_USAGE
    except SimulationError as exc:
        print(f"simulation failed: {exc}", file=sys.stderr)
        return EXIT_SIMULATION_FAILED
    sys.stdout.write(output)
    return EXIT_OK


if __name__ == "__main__":
    sys.exit(main())
