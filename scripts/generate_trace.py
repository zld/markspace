#!/usr/bin/env python3
"""Generate a reproducible synthetic network trace JSON file."""
from __future__ import annotations

import argparse
import sys
from pathlib import Path

from abr_engine.model import ConfigError
from abr_engine.trace import generate_trace, trace_to_json


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--seed", type=int, required=True, help="RNG seed")
    parser.add_argument("--segments", type=int, required=True,
                        help="number of stream segments the trace should cover")
    parser.add_argument("--segment-duration", type=float, default=2.0,
                        help="segment duration in seconds (default: 2)")
    parser.add_argument("--dt", type=float, default=0.5,
                        help="trace sample interval in seconds (default: 0.5)")
    parser.add_argument("--base-bandwidth", type=float, default=3_000_000.0,
                        help="nominal bandwidth in bps (default: 3000000)")
    parser.add_argument("--output", required=True, help="output JSON path")
    args = parser.parse_args(argv)

    try:
        if args.segments < 1:
            raise ConfigError("segments must be >= 1")
        if args.segment_duration <= 0:
            raise ConfigError("segment duration must be positive")
        # Headroom so downloads slower than real time still find samples.
        duration_s = args.segments * args.segment_duration * 1.5 + 10.0
        trace = generate_trace(
            seed=args.seed,
            duration_s=duration_s,
            dt_s=args.dt,
            base_bandwidth_bps=args.base_bandwidth,
        )
        Path(args.output).write_text(trace_to_json(trace), encoding="utf-8")
    except ConfigError as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 2
    except OSError as exc:
        print(f"error: cannot write trace: {exc}", file=sys.stderr)
        return 1
    print(f"wrote {len(trace.samples)} samples to {args.output}", file=sys.stderr)
    return 0


if __name__ == "__main__":
    sys.exit(main())
