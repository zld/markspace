"""Deterministic sample timelines, generated in code; no external files."""
from __future__ import annotations

from fractions import Fraction

from .model import Clip, Timeline, build_timeline

FPS = Fraction(24)


def _f(frames: int) -> Fraction:
    """Frame count to exact seconds at the fixture frame rate."""
    return Fraction(frames, 24)


def demo_timeline() -> Timeline:
    """Three-clip voice-over sample used by ``demo`` and acceptance tests.

    A[0,140) p1, B[120,150) p3, C[80,140) p2 on one track: three
    overlapping pairs, resolving to A[0,80) C[80,120) B[120,150).
    """
    return build_timeline(
        fps=FPS,
        name="demo",
        tracks=[
            (
                "VO",
                [
                    Clip(id="A", start=_f(0), duration=_f(140), priority=1),
                    Clip(id="B", start=_f(120), duration=_f(30), priority=3),
                    Clip(id="C", start=_f(80), duration=_f(60), priority=2),
                ],
            )
        ],
    )


def nested_timeline() -> Timeline:
    """Four clips nested like matryoshka dolls (more than three levels)."""
    return build_timeline(
        fps=FPS,
        name="nested",
        tracks=[
            (
                "FX",
                [
                    Clip(id="X", start=_f(0), duration=_f(100), priority=1),
                    Clip(id="Y", start=_f(10), duration=_f(80), priority=2),
                    Clip(id="Z", start=_f(20), duration=_f(60), priority=3),
                    Clip(id="W", start=_f(30), duration=_f(40), priority=4),
                ],
            )
        ],
    )


def empty_track_timeline() -> Timeline:
    """A timeline whose only track has no clips at all."""
    return build_timeline(fps=FPS, name="empty", tracks=[("VO", [])])


def long_tail_timeline() -> Timeline:
    """A low-priority clip with a tail far beyond the others."""
    return build_timeline(
        fps=FPS,
        name="long-tail",
        tracks=[
            (
                "AMBIENCE",
                [
                    Clip(id="P", start=_f(0), duration=_f(10), priority=2),
                    Clip(id="Q", start=_f(5), duration=_f(1000), priority=1),
                ],
            )
        ],
    )


def offset_timeline() -> Timeline:
    """V2-only features: source offsets and metadata (not v1-able)."""
    return build_timeline(
        fps=FPS,
        name="offsets",
        tracks=[
            (
                "VO",
                [
                    Clip(
                        id="DUB",
                        start=_f(0),
                        duration=_f(48),
                        priority=1,
                        source_start=_f(24),
                        metadata={"actor": "narrator", "lang": "zh"},
                    ),
                    Clip(
                        id="ROOM",
                        start=_f(48),
                        duration=_f(24),
                        priority=1,
                        metadata={"kind": "ambience"},
                    ),
                ],
            )
        ],
    )
