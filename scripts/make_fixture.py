#!/usr/bin/env python3
"""Generate a deterministic v2 exchange fixture with genuine overlaps.

The sample sequence runs at 30000/1001 and contains:

* ``v1`` with three clips and three pairwise conflicts,
* ``a1`` with two overlapping audio clips,
* a segmented-speed ``speed_ramp`` clip on ``v2`` whose second segment runs at
  two frames of source per timeline frame.

Audio ``a1`` sits on level 0, ``v1`` on level 1 and ``v2`` on level 2, so the
offline renderer samples the overlay; the segmented clip is exported
losslessly to JSON and intentionally rejected by the structural OTIO exporter.

Running the script twice with the same arguments always writes identical bytes.
"""

from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "src"))

from cutline.exchange import write  # noqa: E402
from cutline.model import Clip, RateSegment, Sequence, Track  # noqa: E402
from cutline.timebase import FrameRate  # noqa: E402
from fractions import Fraction  # noqa: E402

RATE = FrameRate(30000, 1001)


def build_sequence() -> Sequence:
    video = Track(
        id="v1",
        kind="video",
        level=1,
        clips=(
            Clip(id="opening", media_id="media/opening.mov", timeline_start=0,
                 duration=72, source_in=0, source_duration=72, fade_in=12),
            Clip(id="hero", media_id="media/hero.mov", timeline_start=48,
                 duration=48, source_in=24, source_duration=120, priority=5),
            # Extends the sequence past its other clips so the priority
            # strategy has room to relocate the clipped remainder.
            Clip(id="insert", media_id="media/insert.mov", timeline_start=90,
                 duration=72, source_in=0, source_duration=90, priority=2,
                 fade_out=8),
        ),
    )
    audio = Track(
        id="a1",
        kind="audio",
        level=0,
        clips=(
            Clip(id="vo_01", media_id="media/vo_01.wav", timeline_start=0,
                 duration=90, source_in=0, source_duration=90),
            Clip(id="vo_02", media_id="media/vo_02.wav", timeline_start=60,
                 duration=60, source_in=0, source_duration=60, priority=3),
        ),
    )
    overlay = Track(
        id="v2",
        kind="video",
        level=2,
        clips=(
            Clip(id="speed_ramp", media_id="media/ramp.mov", timeline_start=0,
                 duration=40, source_in=0, source_duration=60,
                 rate_segments=(
                     RateSegment(0, 20, Fraction(3, 2)),
                     RateSegment(20, 20, Fraction(1, 2)),
                 )),
        ),
    )
    return Sequence(id="demo-fixture", frame_rate=RATE,
                    tracks=(audio, video, overlay), name="cutline-demo")


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description="Write a reproducible cutline v2 fixture")
    parser.add_argument("--output", required=True, help="destination JSON path")
    parser.add_argument("--version", type=int, choices=(1, 2), default=2,
                        help="exchange version to emit (default: 2)")
    args = parser.parse_args(argv)
    try:
        sequence = build_sequence()
        target = write(sequence, args.output, version=args.version)
    except Exception as exc:  # surface a clean message, keep exit code non-zero
        print(f"make_fixture: {exc}", file=sys.stderr)
        return 1
    document = json.loads(Path(target).read_text(encoding="utf-8"))
    clip_count = sum(len(track["clips"]) for track in document["sequence"]["tracks"])
    print(f"wrote {target} (version {document['version']}, "
          f"{len(document['sequence']['tracks'])} tracks, {clip_count} clips)")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
