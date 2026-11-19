"""Command line interface: generate, simulate and summarize.

Exit codes: 0 success, 2 invalid arguments or input, 1 simulation failure.
"""
from __future__ import annotations

import argparse
import json
import sys

from .models import ValidationError, validate_ladder
from .report import MAX_JSON_BYTES, SUMMARY_KEYS, dumps_payload, format_summary, to_payload
from .simulator import SimulationError, simulate
from .trace import generate_trace, load_trace, save_trace


def _parse_bitrates(text: str) -> tuple[int, ...]:
    parts = [p.strip() for p in text.split(",") if p.strip()]
    try:
        values = [int(p) for p in parts]
    except ValueError as exc:
        raise ValidationError(f"invalid bitrate list {text!r}: expected comma-separated integers") from exc
    return validate_ladder(values)


def _cmd_generate(args: argparse.Namespace) -> int:
    samples = generate_trace(seed=args.seed, duration_s=args.duration, interval_s=args.interval)
    save_trace(samples, args.output)
    print(f"wrote {len(samples)} samples to {args.output}")
    return 0


def _cmd_simulate(args: argparse.Namespace) -> int:
    ladder = _parse_bitrates(args.bitrates)
    samples = load_trace(args.trace)
    result = simulate(
        samples,
        ladder,
        segments=args.segments,
        segment_duration_s=args.segment_duration,
        initial_buffer_s=args.initial_buffer,
        buffer_capacity_s=args.buffer_capacity,
        segment_bytes=args.segment_bytes,
    )
    payload = to_payload(result)
    if args.output:
        with open(args.output, "w", encoding="utf-8") as fh:
            fh.write(dumps_payload(payload))
    if args.json:
        if not args.output:
            print(dumps_payload(payload))
    else:
        print(format_summary(payload["summary"]))
    return 0


def _cmd_summarize(args: argparse.Namespace) -> int:
    try:
        with open(args.result, "rb") as fh:
            raw = fh.read(MAX_JSON_BYTES + 1)
    except OSError as exc:
        raise ValidationError(f"cannot read result file {args.result}: {exc}") from exc
    if len(raw) > MAX_JSON_BYTES:
        raise ValidationError(f"result file {args.result} exceeds limit {MAX_JSON_BYTES} bytes")
    try:
        payload = json.loads(raw.decode("utf-8"))
    except (ValueError, UnicodeDecodeError) as exc:
        raise ValidationError(f"invalid result file {args.result}: {exc}") from exc
    if not isinstance(payload, dict) or not isinstance(payload.get("summary"), dict):
        raise ValidationError(f"result file {args.result}: missing 'summary' object")
    summary = payload["summary"]
    for key in SUMMARY_KEYS:
        if key not in summary or isinstance(summary[key], bool) or not isinstance(
            summary[key], (int, float)
        ):
            raise ValidationError(f"result file {args.result}: summary field {key!r} missing or not numeric")
    print(format_summary(summary))
    return 0


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="abr_engine",
        description="Offline ABR decision engine: deterministic trace generation and playback simulation.",
    )
    sub = parser.add_subparsers(dest="command", required=True)

    gen = sub.add_parser("generate", help="generate a network trace JSON file")
    gen.add_argument("--seed", type=int, required=True, help="non-negative random seed")
    gen.add_argument("--duration", type=float, required=True, help="trace duration in seconds")
    gen.add_argument("--interval", type=float, required=True, help="sampling interval in seconds")
    gen.add_argument("--output", required=True, help="output trace JSON path")
    gen.set_defaults(func=_cmd_generate)

    sim = sub.add_parser("simulate", help="simulate ABR playback over a trace")
    sim.add_argument("--trace", required=True, help="input trace JSON path")
    sim.add_argument("--bitrates", required=True, help="comma-separated ladder in bit/s, strictly increasing")
    sim.add_argument("--segments", type=int, required=True, help="number of segments to play")
    sim.add_argument("--segment-duration", type=float, required=True, help="segment duration in seconds")
    sim.add_argument("--initial-buffer", type=float, required=True, help="starting buffer in seconds")
    sim.add_argument("--segment-bytes", type=float, default=None,
                     help="explicit encoded segment size in bytes (must match bitrate*duration/8)")
    sim.add_argument("--buffer-capacity", type=float, default=None, help="buffer cap in seconds")
    sim.add_argument("--json", action="store_true", help="print full JSON result to stdout")
    sim.add_argument("--output", default=None, help="write full JSON result to this path")
    sim.set_defaults(func=_cmd_simulate)

    summ = sub.add_parser("summarize", help="print the terminal summary of a simulation result JSON")
    summ.add_argument("--result", required=True, help="simulation result JSON path")
    summ.set_defaults(func=_cmd_summarize)
    return parser


def main(argv=None) -> int:
    args = build_parser().parse_args(argv)
    try:
        return args.func(args)
    except ValidationError as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 2
    except SimulationError as exc:
        print(f"simulation failed: {exc}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    sys.exit(main())
