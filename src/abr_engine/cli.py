"""Command-line interface for the ABR simulation engine."""

from __future__ import annotations

import argparse
import json
import math
import sys
from typing import Sequence

from . import __version__
from .buffer import BufferModel
from .errors import ConfigError, SimulationError
from .estimator import BandwidthEstimator
from .ladder import Ladder, parse_bitrates
from .policy import AbrPolicy, PolicyConfig
from .simulate import SimulationResult, simulate_from_parameters
from .trace import (
    DEFAULT_BASE_KBPS,
    DEFAULT_BASE_RTT_MS,
    DEFAULT_JITTER_KBPS,
    DEFAULT_LOSS_PROBABILITY,
)

EXIT_OK = 0
EXIT_SIMULATION_ERROR = 1
EXIT_CONFIG_ERROR = 2


def _number(text: str) -> float:
    try:
        value = float(text)
    except (TypeError, ValueError) as exc:
        raise argparse.ArgumentTypeError(f"invalid number: {text!r}") from exc
    if not math.isfinite(value):
        raise argparse.ArgumentTypeError(f"number must be finite: {text!r}")
    return value


def _integer(text: str) -> int:
    try:
        return int(text)
    except (TypeError, ValueError) as exc:
        raise argparse.ArgumentTypeError(f"invalid integer: {text!r}") from exc


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="abr_engine",
        description="Simulate an adaptive-bitrate policy over a synthetic network trace.",
    )
    parser.add_argument("--version", action="version", version=f"abr-engine {__version__}")
    subparsers = parser.add_subparsers(dest="command", required=True)

    sim = subparsers.add_parser(
        "simulate",
        help="run one trace through the decision engine",
        description="Generate a deterministic trace and simulate segment-by-segment adaptation.",
    )
    sim.add_argument("--segments", type=_integer, required=True, help="number of segments (>= 1)")
    sim.add_argument("--seed", type=_integer, required=True, help="trace random seed")
    sim.add_argument(
        "--segment-seconds", type=_number, default=4.0, help="segment duration in seconds"
    )
    sim.add_argument(
        "--bitrates",
        default="300,750,1200,1850,2600",
        help="comma-separated bitrate ladder in kbps (strictly increasing)",
    )
    sim.add_argument("--initial-buffer", type=_number, default=12.0, help="initial buffer seconds")
    sim.add_argument("--buffer-capacity", type=_number, default=30.0, help="buffer capacity seconds")
    sim.add_argument("--output", choices=("text", "json"), default="text", help="output format")

    trace = sim.add_argument_group("trace")
    trace.add_argument("--base-kbps", type=_number, default=DEFAULT_BASE_KBPS)
    trace.add_argument("--jitter-kbps", type=_number, default=DEFAULT_JITTER_KBPS)
    trace.add_argument("--base-rtt-ms", type=_number, default=DEFAULT_BASE_RTT_MS)
    trace.add_argument("--loss-probability", type=_number, default=DEFAULT_LOSS_PROBABILITY)

    policy = sim.add_argument_group("policy")
    policy.add_argument("--initial-tier", type=_integer, default=0)
    policy.add_argument("--danger-buffer", type=_number, default=4.0)
    policy.add_argument("--up-min-buffer", type=_number, default=8.0)
    policy.add_argument("--up-safety-margin", type=_number, default=1.15)
    policy.add_argument("--min-dwell", type=_integer, default=2)
    policy.add_argument("--up-streak", type=_integer, default=2)

    estimator = sim.add_argument_group("estimator")
    estimator.add_argument("--forget-factor", type=_number, default=0.2)
    estimator.add_argument("--safety-factor", type=_number, default=0.85)
    estimator.add_argument("--cold-start-kbps", type=_number, default=300.0)
    return parser


def _run_simulation(args: argparse.Namespace) -> SimulationResult:
    ladder = Ladder(parse_bitrates(args.bitrates), args.segment_seconds)
    policy_config = PolicyConfig(
        danger_buffer_seconds=args.danger_buffer,
        up_min_buffer_seconds=args.up_min_buffer,
        up_safety_margin=args.up_safety_margin,
        min_dwell_segments=args.min_dwell,
        up_streak_segments=args.up_streak,
    )
    policy = AbrPolicy(ladder, policy_config, initial_tier=args.initial_tier)
    estimator = BandwidthEstimator(
        forget_factor=args.forget_factor,
        safety_factor=args.safety_factor,
        cold_start_kbps=args.cold_start_kbps,
    )
    # Construct the buffer early so configuration errors surface before the trace.
    buffer_model = BufferModel(args.buffer_capacity, args.initial_buffer)
    return simulate_from_parameters(
        segments=args.segments,
        seed=args.seed,
        ladder=ladder,
        initial_buffer_seconds=args.initial_buffer,
        buffer_capacity_seconds=args.buffer_capacity,
        policy=policy,
        estimator=estimator,
        buffer_model=buffer_model,
        base_kbps=args.base_kbps,
        jitter_kbps=args.jitter_kbps,
        base_rtt_ms=args.base_rtt_ms,
        loss_probability=args.loss_probability,
    )


def _format_float(value: float) -> str:
    return f"{value:.3f}"


def render_text(result: SimulationResult) -> str:
    summary = result.summary
    if summary is None:
        raise SimulationError("simulation result has no summary")
    lines = [
        f"segments={summary.segments}",
        f"switch_count={summary.switch_count}",
        f"stall_seconds={_format_float(summary.stall_seconds)}",
        f"mean_estimated_kbps={_format_float(summary.mean_estimated_kbps)}",
        f"mean_selected_kbps={_format_float(summary.mean_selected_kbps)}",
        f"mean_buffer_seconds={_format_float(summary.mean_buffer_seconds)}",
        f"mean_download_seconds={_format_float(summary.mean_download_seconds)}",
        f"final_tier={summary.final_tier}",
        f"final_bitrate_kbps={_format_float(summary.final_bitrate_kbps)}",
        f"final_buffer_seconds={_format_float(summary.final_buffer_seconds)}",
    ]
    for record in result.records:
        lines.append(
            "segment={segment} tier={tier} estimated_kbps={est} selected_kbps={sel} "
            "throughput_kbps={tput} rtt_ms={rtt} download_seconds={dl} "
            "buffer_after_seconds={buf} stall_seconds={stall} reason={reason} "
            "switched={switched}".format(
                segment=record.segment,
                tier=record.tier,
                est=_format_float(record.estimated_kbps),
                sel=_format_float(record.selected_kbps),
                tput=_format_float(record.throughput_kbps),
                rtt=_format_float(record.rtt_ms),
                dl=_format_float(record.download_seconds),
                buf=_format_float(record.buffer_after_seconds),
                stall=_format_float(record.stall_seconds),
                reason=record.reason,
                switched="true" if record.switched else "false",
            )
        )
    return "\n".join(lines)


def render_json(result: SimulationResult) -> str:
    return json.dumps(result.to_dict(), indent=2, sort_keys=False)


def main(argv: Sequence[str] | None = None) -> int:
    parser = build_parser()
    args = parser.parse_args(argv)
    try:
        result = _run_simulation(args)
        output = render_json(result) if args.output == "json" else render_text(result)
    except (ConfigError, ValueError) as exc:
        print(f"error: {exc}", file=sys.stderr)
        return EXIT_CONFIG_ERROR
    except SimulationError as exc:
        print(f"simulation error: {exc}", file=sys.stderr)
        return EXIT_SIMULATION_ERROR
    except (ArithmeticError, FloatingPointError) as exc:
        print(f"simulation error: {exc}", file=sys.stderr)
        return EXIT_SIMULATION_ERROR
    try:
        sys.stdout.write(output + "\n")
        sys.stdout.flush()
    except BrokenPipeError:
        return EXIT_SIMULATION_ERROR
    return EXIT_OK


def entrypoint() -> None:
    raise SystemExit(main())


if __name__ == "__main__":
    entrypoint()
