"""Deterministic off-screen renderer: timeline model -> PPM track map.

Output is a plain-text P3 PPM.  Colors derive from SHA-256 of clip ids,
so identical inputs produce byte-identical files in any process.
"""

from __future__ import annotations

import hashlib
from fractions import Fraction
from pathlib import Path
from typing import Union

from .model import Clip, Gap, Timeline

TRACK_HEIGHT = 24
TRACK_GAP = 6
MARGIN = 8
BACKGROUND = (18, 18, 22)
GAP_COLOR = (52, 52, 60)


def _clip_color(clip_id: str) -> tuple[int, int, int]:
    digest = hashlib.sha256(clip_id.encode("utf-8")).digest()
    r, g, b = digest[0], digest[1], digest[2]
    # Lift into a readable mid-bright band deterministically.
    return (96 + r % 140, 96 + g % 140, 96 + b % 140)


def render(timeline: Timeline, width: int = 640) -> bytes:
    if width <= 0:
        raise ValueError(f"render width must be positive, got {width}")
    starts = [it.start for t in timeline.tracks for it in t.items]
    t0 = min([Fraction(0), *starts])
    t1 = max([Fraction(1), timeline.end])
    span = t1 - t0
    height = MARGIN * 2 + len(timeline.tracks) * TRACK_HEIGHT
    if timeline.tracks:
        height += (len(timeline.tracks) - 1) * TRACK_GAP

    pixels = [[BACKGROUND] * width for _ in range(height)]

    def x_of(t: Fraction) -> int:
        return int((t - t0) * width / span)

    for ti, track in enumerate(timeline.tracks):
        top = MARGIN + ti * (TRACK_HEIGHT + TRACK_GAP)
        for item in track.items:
            x0 = max(0, min(width, x_of(item.start)))
            x1 = max(0, min(width, x_of(item.end)))
            if x1 <= x0:
                x1 = min(width, x0 + 1)  # zero/short durations stay visible
            color = _clip_color(item.clip_id) if isinstance(item, Clip) else GAP_COLOR
            for y in range(top, top + TRACK_HEIGHT):
                row = pixels[y]
                for x in range(x0, x1):
                    row[x] = color

    lines = [f"P3 {width} {height} 255"]
    for row in pixels:
        lines.append(" ".join(f"{r} {g} {b}" for r, g, b in row))
    return ("\n".join(lines) + "\n").encode("ascii")


def render_to_file(timeline: Timeline, path: Union[str, Path],
                   width: int = 640) -> tuple[int, int, str]:
    """Render and write; returns (width, height, sha256_hex)."""
    data = render(timeline, width=width)
    Path(path).write_bytes(data)
    header = data.split(b"\n", 1)[0].split()
    return int(header[1]), int(header[2]), hashlib.sha256(data).hexdigest()
