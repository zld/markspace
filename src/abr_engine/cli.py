"""Command-line interface: abr-simulate.

Exit codes: 0 success, 2 invalid parameters or trace, 1 runtime error.
"""
from __future__ import annotations

import argparse
import json
import os
import sys

from abr_engine import metrics
from abr_engine.models import ConfigError, Ladder
from abr_engine.policy import PolicyConfig
from abr_engine.simulator import (
    SimulationConfig,
    SimulationError,
    TraceError,
    run_simulation,
    trace_from_dict,
)
from abr_engine.tracegen import generate_trace


def build_parser() -> argparse.ArgumentParser:
    p = argparse.ArgumentParser(
        prog="abr-simulate",
        description="Simulate ABR bitrate decisions over a network trace.",
    )
    p.add_argument("--trace", help="trace JSON file (omit when using --seeds)")
    p.add_argument("--bitrates", required=True,
                   help="comma-separated ladder rungs in bits per second")
    p.add_argument("--segment-seconds", type=float, default=4.0)
    p.add_argument("--segments", type=int, default=None,
                   help="segment count (overrides the trace metadata)")
    p.add_argument("--initial-buffer", type=float, default=8.0)
    p.add_argument("--safety", type=float, default=0.85,
                   help="safety margin applied to the bandwidth estimate")
    p.add_argument("--danger-buffer", type=float, default=None,
                   help="force a downswitch below this buffer (seconds)")
    p.add_argument("--recover-buffer", type=float, default=None,
                   help="allow upswitches only at or above this buffer (seconds)")
    p.add_argument("--window", type=int, default=8,
                   help="estimator sliding-window size")
    p.add_argument("--decay", type=float, default=0.6,
                   help="estimator exponential decay factor in (0, 1]")
    p.add_argument("--max-buffer", type=float, default=None,
                   help="optional buffer capacity in seconds")
    p.add_argument("--seeds", default=None,
                   help="comma-separated seeds: batch mode generating traces")
    p.add_argument("--json", action="store_true", help="emit machine-readable JSON")
    return p


def _parse_bitrates(text: str) -> list[int]:
    try:
        return [int(part) for part in text.split(",") if part.strip()]
    except ValueError:
        raise ConfigError("bitrates must be comma-separated integers")


def _parse_seeds(text: str) -> list[int]:
    try:
        seeds = [int(part) for part in text.split(",") if part.strip()]
    except ValueError:
        raise ConfigError("seeds must be comma-separated integers")
    if not seeds:
        raise ConfigError("at least one seed is required for batch mode")
    return seeds


def _config_for(args, ladder: Ladder) -> SimulationConfig:
    danger = args.danger_buffer
    if danger is None:
        danger = ladder.segment_seconds
    recover = args.recover_buffer
    if recover is None:
        recover = 2.5 * ladder.segment_seconds
    policy = PolicyConfig(safety=args.safety, danger_buffer=danger,
                          recover_buffer=recover)
    policy.validate()
    return SimulationConfig(
        ladder=ladder,
        segment_count=args.segments or 0,
        initial_buffer=args.initial_buffer,
        policy=policy,
        window=args.window,
        decay=args.decay,
        max_buffer=args.max_buffer,
    )


def _simulate(trace, config: SimulationConfig, segment_count: int):
    config = SimulationConfig(**{**config.__dict__, "segment_count": segment_count})
    records = run_simulation(trace, config)
    return records, metrics.summarize(records)


def _load_trace_file(path: str):
    try:
        with open(path, "r", encoding="utf-8") as fh:
            return json.load(fh)
    except FileNotFoundError:
        raise TraceError(f"trace file not found: {os.path.basename(path)}")
    except json.JSONDecodeError as exc:
        raise TraceError(f"trace file is not valid JSON: {exc}")


def _run_single(args, config: SimulationConfig) -> dict:
    if not args.trace:
        raise ConfigError("--trace is required unless --seeds is given")
    trace, meta = trace_from_dict(_load_trace_file(args.trace))
    count = args.segments or meta.get("segments")
    if not count:
        raise ConfigError("segment count unknown: pass --segments")
    records, summary = _simulate(trace, config, int(count))
    return {"mode": "single", **summary,
            "segments": [r.to_dict() for r in records]}


def _run_batch(args, config: SimulationConfig) -> dict:
    seeds = _parse_seeds(args.seeds)
    if not args.segments:
        raise ConfigError("--segments is required together with --seeds")
    runs = []
    for seed in seeds:
        data = generate_trace(seed, args.segments, config.ladder.segment_seconds)
        trace, _ = trace_from_dict(data)
        _, summary = _simulate(trace, config, args.segments)
        runs.append({"seed": seed, **summary})
    rebuffers = [r["total_rebuffer_seconds"] for r in runs]
    worst_index = max(range(len(runs)), key=lambda i: rebuffers[i])
    return {
        "mode": "batch",
        "runs": runs,
        "aggregate": {
            "run_count": len(runs),
            "mean_rebuffer_seconds": round(sum(rebuffers) / len(rebuffers), 6),
            "max_rebuffer_seconds": rebuffers[worst_index],
            "worst_index": worst_index,
            "worst_seed": runs[worst_index]["seed"],
        },
    }


def _fail(code: int, kind: str, message: str, as_json: bool) -> int:
    if as_json:
        print(json.dumps({"error": {"type": kind, "message": message}}))
    else:
        print(f"error: {message}", file=sys.stderr)
    return code


def main(argv=None) -> int:
    args = build_parser().parse_args(argv)
    try:
        ladder = Ladder.build(_parse_bitrates(args.bitrates), args.segment_seconds)
        config = _config_for(args, ladder)
        if args.seeds:
            payload = _run_batch(args, config)
        else:
            payload = _run_single(args, config)
    except (ConfigError, TraceError) as exc:
        return _fail(2, "invalid_input", str(exc), args.json)
    except SimulationError as exc:
        return _fail(1, "simulation_error", str(exc), args.json)
    try:
        metrics.ensure_finite(payload)
    except ValueError as exc:
        return _fail(1, "non_finite_result", str(exc), args.json)
    if args.json:
        print(json.dumps(payload, allow_nan=False))
    else:
        _print_text(payload)
    return 0


def _print_text(payload: dict) -> None:
    if payload["mode"] == "batch":
        for run in payload["runs"]:
            print(f"seed={run['seed']} switches={run['switch_count']} "
                  f"rebuffer={run['total_rebuffer_seconds']:.3f}s "
                  f"fallbacks={run['fallback_count']}")
        agg = payload["aggregate"]
        print(f"aggregate: mean_rebuffer={agg['mean_rebuffer_seconds']:.3f}s "
              f"max_rebuffer={agg['max_rebuffer_seconds']:.3f}s "
              f"worst_index={agg['worst_index']}")
    else:
        print(f"segments={payload['segment_count']} "
              f"switches={payload['switch_count']} "
              f"rebuffer={payload['total_rebuffer_seconds']:.3f}s "
              f"fallbacks={payload['fallback_count']}")


if __name__ == "__main__":
    sys.exit(main())
