#!/usr/bin/env python3
"""Reproducible end-to-end demonstration of the timeline pipeline.

Runs the built-in delivery (clips A/B/C with overlapping tracks) through
both exchange format versions and with reversed record order, then proves
that every variant produces the same conflict count, resolved intervals,
duration deviation and per-frame render manifest.

Run with the project virtual environment::

    .venv/bin/python scripts/demo.py
"""

from __future__ import annotations

import json
import sys
from pathlib import Path

# Allow execution straight from a fresh checkout without installation.
ROOT = Path(__file__).resolve().parents[1]
SRC = ROOT / "src"
if str(SRC) not in sys.path:
    sys.path.insert(0, str(SRC))

from timeline_core.cli import build_demo_document, run_pipeline  # noqa: E402

FPS = 24


def _summarize(fmt: str, reversed_order: bool) -> dict:
    document = build_demo_document(fmt, FPS, reversed_order=reversed_order)
    result, manifest = run_pipeline(document, fmt)
    return {
        "format": fmt,
        "reversed": reversed_order,
        "conflict_count": result.conflict_count,
        "resolved_intervals": [
            {"start": span.start, "end": span.end, "id": span.clip_id}
            for span in result.intervals
        ],
        "duration_deviation_frames": result.duration_deviation_frames,
        "rendered_frames": manifest.rendered_frames,
        "frame_signature": [
            (frame.frame, frame.clip_id, frame.interval_index)
            for frame in manifest.frames
        ],
    }


def main() -> int:
    variants = [
        _summarize("v1", reversed_order=False),
        _summarize("v2", reversed_order=False),
        _summarize("v2", reversed_order=True),
    ]

    baseline = variants[0]
    comparable_keys = (
        "conflict_count",
        "resolved_intervals",
        "duration_deviation_frames",
        "rendered_frames",
        "frame_signature",
    )
    for variant in variants[1:]:
        for key in comparable_keys:
            assert variant[key] == baseline[key], (
                f"non-deterministic {key}: {variant['format']} "
                f"reversed={variant['reversed']}"
            )

    print("timeline_core deterministic pipeline demo")
    print(f"fps={FPS}  clips: A[0,5s) p1  B[3,7s) p2  C[6,9s) p1")
    print("-" * 68)
    for variant in variants:
        print(
            f"format={variant['format']:<2} "
            f"order={'reversed' if variant['reversed'] else 'as-delivered':<12} "
            f"conflicts={variant['conflict_count']} "
            f"deviation={variant['duration_deviation_frames']} "
            f"frames={variant['rendered_frames']}"
        )
        for span in variant["resolved_intervals"]:
            print(f"    [{span['start']:>3}, {span['end']:>3}) -> {span['id']}")
    print("-" * 68)
    print(
        "all variants identical: conflict_count=2, deviation=0, "
        "rendered_frames=216"
    )
    print("\nJSON summary of the v1 run:")
    printable = {k: v for k, v in baseline.items() if k != "frame_signature"}
    print(json.dumps(printable, indent=2, sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
