#!/usr/bin/env python3
"""Generate a deterministic synthetic network trace as JSON."""
from __future__ import annotations

import argparse
import json
import sys

from abr_engine.tracegen import generate_trace


def main(argv=None) -> int:
    parser = argparse.ArgumentParser(
        description="Generate a seeded synthetic bandwidth trace (JSON).")
    parser.add_argument("--segments", type=int, required=True,
                        help="number of playback segments to cover")
    parser.add_argument("--seed", type=int, required=True,
                        help="random seed (reproducible)")
    parser.add_argument("--segment-seconds", type=float, default=4.0,
                        help="segment duration the trace should cover")
    parser.add_argument("--output", required=True, help="output JSON file")
    args = parser.parse_args(argv)
    try:
        trace = generate_trace(args.seed, args.segments, args.segment_seconds)
    except ValueError as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 2
    with open(args.output, "w", encoding="utf-8") as fh:
        json.dump(trace, fh)
    print(f"samples={len(trace['samples'])} seed={args.seed} "
          f"segments={args.segments}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
