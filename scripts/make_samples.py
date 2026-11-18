#!/usr/bin/env python3
"""Generate deterministic sample exchange files for cutline.

The default sample (``--out``) is the v2 file used by the acceptance demo and
the test-suite fixture.  It contains exactly three conflicts:

1. same-track overlap on ``v1`` between ``sh010`` and ``sh020`` (sh020 has the
   higher priority and wins; sh010 keeps its left piece);
2. cross-track overlap between ``sh030`` (v1) and ``bg-main`` (v2);
3. cross-track overlap between ``sh050`` (v1) and ``sub-002`` (subs).

``sh030``/``sh040`` and ``sh020``/``sub-001`` touch exactly at frame
boundaries, which must be reported as adjacency and never as a conflict.

Additional files are produced on request:

``--v1-out``      the same timeline in the v1 (integer frames) format;
``--broken-out``  a deliberately malformed document for error-path tests;
``--batch-dir``   a directory with three inputs, one of them corrupt.
"""

from __future__ import annotations

import argparse
import json
import sys
from fractions import Fraction
from pathlib import Path

DEFAULT_TIMELINE_ID = "demo-reel-01"
RATE_24 = "24"


def _clip(
    clip_id: str,
    start: str,
    duration: str,
    priority: int,
    name: str,
    *,
    rate: str = RATE_24,
    metadata: dict | None = None,
) -> dict:
    return {
        "id": clip_id,
        "name": name,
        "start": start,
        "duration": duration,
        "rate": rate,
        "priority": priority,
        "metadata": metadata or {},
    }


def sample_v2() -> dict:
    """Return the canonical v2 sample document."""
    return {
        "version": "v2",
        "timeline": {
            "id": DEFAULT_TIMELINE_ID,
            "name": "Demo Reel",
            "metadata": {"generator": "make_samples.py", "shot_day": 12},
            "tracks": [
                {
                    "id": "v1",
                    "name": "Editorial",
                    "rate": RATE_24,
                    "metadata": {"role": "picture"},
                    "clips": [
                        _clip("sh010", "0", "3", 1, "Opening wide"),
                        _clip("sh020", "2", "3", 5, "Push in"),
                        _clip("sh030", "8", "2", 2, "Cutaway"),
                        _clip("sh040", "10", "2", 2, "Establishing"),
                        _clip("sh050", "13", "2", 2, "Reaction"),
                    ],
                },
                {
                    "id": "v2",
                    "name": "Background",
                    "rate": RATE_24,
                    "metadata": {"role": "background"},
                    "clips": [
                        _clip("bg-main", "9", "1", 3, "City plate"),
                        _clip("bg-close", "15", "1", 3, "Rooftop plate"),
                    ],
                },
                {
                    "id": "subs",
                    "name": "Subtitles",
                    "rate": RATE_24,
                    "metadata": {"role": "caption"},
                    "clips": [
                        _clip("sub-001", "5", "2", 9, "Caption A"),
                        _clip("sub-002", "13", "1", 9, "Caption B"),
                    ],
                },
            ],
        },
    }


def sample_v1() -> dict:
    """Same shape expressed with integer frames (v1)."""
    return {
        "version": "v1",
        "timeline": {
            "id": DEFAULT_TIMELINE_ID,
            "name": "Demo Reel",
            "tracks": [
                {
                    "id": "v1",
                    "name": "Editorial",
                    "rate": "24",
                    "clips": [
                        {"id": "sh010", "name": "Opening wide", "start_frame": 0, "duration_frames": 72, "rate": "24", "priority": 1},
                        {"id": "sh020", "name": "Push in", "start_frame": 48, "duration_frames": 72, "rate": "24", "priority": 5},
                        {"id": "sh030", "name": "Cutaway", "start_frame": 192, "duration_frames": 48, "rate": "24", "priority": 2},
                        {"id": "sh040", "name": "Establishing", "start_frame": 240, "duration_frames": 48, "rate": "24", "priority": 2},
                        {"id": "sh050", "name": "Reaction", "start_frame": 312, "duration_frames": 48, "rate": "24", "priority": 2},
                    ],
                },
                {
                    "id": "v2",
                    "name": "Background",
                    "rate": "24",
                    "clips": [
                        {"id": "bg-main", "name": "City plate", "start_frame": 216, "duration_frames": 24, "rate": "24", "priority": 3},
                        {"id": "bg-close", "name": "Rooftop plate", "start_frame": 360, "duration_frames": 24, "rate": "24", "priority": 3},
                    ],
                },
                {
                    "id": "subs",
                    "name": "Subtitles",
                    "rate": "24",
                    "clips": [
                        {"id": "sub-001", "name": "Caption A", "start_frame": 120, "duration_frames": 48, "rate": "24", "priority": 9},
                        {"id": "sub-002", "name": "Caption B", "start_frame": 312, "duration_frames": 24, "rate": "24", "priority": 9},
                    ],
                },
            ],
        },
    }


def broken_v2() -> dict:
    """A v2 document that exercises value validation (negative start, bad rate)."""
    return {
        "version": "v2",
        "timeline": {
            "id": "broken",
            "name": "Broken",
            "tracks": [
                {
                    "id": "v1",
                    "rate": "0",
                    "clips": [
                        {
                            "id": "bad-start",
                            "start": "-1",
                            "duration": "2",
                            "rate": "24",
                            "priority": 0,
                        }
                    ],
                }
            ],
        },
    }


def _write(path: Path, payload: dict) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(payload, indent=2) + "\n", encoding="utf-8")


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--out", required=True, help="path for the v2 sample (required)")
    parser.add_argument("--v1-out", help="optional path for the v1 sample")
    parser.add_argument("--broken-out", help="optional path for a malformed sample")
    parser.add_argument("--batch-dir", help="optional directory with a 3-file batch")
    args = parser.parse_args(argv)

    out = Path(args.out)
    _write(out, sample_v2())
    written = [out]
    if args.v1_out:
        _write(Path(args.v1_out), sample_v1())
        written.append(Path(args.v1_out))
    if args.broken_out:
        _write(Path(args.broken_out), broken_v2())
        written.append(Path(args.broken_out))
    if args.batch_dir:
        batch = Path(args.batch_dir)
        _write(batch / "sample-v2.json", sample_v2())
        _write(batch / "sample-v1.json", sample_v1())
        bad = batch / "corrupt.json"
        bad.write_text('{"version": "v2", "timeline": {', encoding="utf-8")
        written.extend([batch / "sample-v2.json", batch / "sample-v1.json", bad])

    for path in written:
        print(path)
    return 0


if __name__ == "__main__":
    sys.exit(main())
