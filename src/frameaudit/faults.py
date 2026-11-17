"""Apply a fixture fault manifest to presentation-ordered decoded frames.

The manifest maps presentation positions to fault kinds; application happens
after bounded reordering and before timestamp repair, so every repair path
(missing / duplicate / backward / jump) is exercised against real decoded
frames while remaining fully deterministic and offline.
"""

from __future__ import annotations

import json
from fractions import Fraction
from pathlib import Path

from .decode import DecodedFrame
from .errors import UsageError


def load_manifest(path: str | Path) -> dict:
    try:
        data = json.loads(Path(path).read_text())
    except FileNotFoundError as exc:
        raise UsageError(f"faults manifest not found: {path}") from exc
    except json.JSONDecodeError as exc:
        raise UsageError(f"invalid JSON in faults manifest {path}: {exc}") from exc
    if not isinstance(data.get("faults"), list):
        raise UsageError(f"faults manifest {path} needs a 'faults' list")
    return data


def apply_faults(
    frames: list[DecodedFrame],
    manifest: dict,
    frame_rate: Fraction,
) -> list[DecodedFrame]:
    """Return *frames* with manifest faults applied to their PTS values.

    Frames are copied (shallowly) so the original decode output stays
    untouched; only the ``pts`` field is mutated on the copies.
    """
    step_ms = Fraction(1000, 1) / frame_rate  # ms per frame
    by_position = {
        entry["presentation_position"]: entry for entry in manifest["faults"]
    }

    result: list[DecodedFrame] = []
    previous_pts: int | None = None
    for order_index, frame in enumerate(frames):
        entry = by_position.get(order_index)
        if entry is None:
            result.append(frame)
            previous_pts = frame.pts
            continue

        kind = entry.get("kind")
        shift = int(entry.get("shift_frames", 0))
        # Work in milliseconds, the fixture's shared time base.
        base_ms = (
            frame.pts * frame.time_base * 1000
            if frame.pts is not None
            else Fraction(order_index) * step_ms
        )
        new_frame = DecodedFrame(
            decode_index=frame.decode_index,
            pts=frame.pts,
            dts=frame.dts,
            time_base=frame.time_base,
            keyframe=frame.keyframe,
            pict_type=frame.pict_type,
            width=frame.width,
            height=frame.height,
            luma=frame.luma,
        )

        if kind == "missing":
            new_frame.pts = None
        elif kind == "backward":
            shifted = base_ms - Fraction(shift) * step_ms
            new_frame.pts = round(shifted / (frame.time_base * 1000))
        elif kind == "jump":
            shifted = base_ms + Fraction(shift) * step_ms
            new_frame.pts = round(shifted / (frame.time_base * 1000))
        elif kind == "duplicate":
            new_frame.pts = previous_pts
        else:
            raise UsageError(f"unknown fault kind {kind!r} in manifest")

        result.append(new_frame)
        previous_pts = new_frame.pts
    return result
