"""Deterministic sample timeline generator.

The canonical 24 fps sample exercises an overlap chain on an exclusive
video track and permitted overlap on a layered audio track.  The seed
only perturbs media URIs, never timing, so reports are reproducible.
"""

from __future__ import annotations

import random
from fractions import Fraction

from .model import Clip, MediaRef, Timeline, Track

RATE = Fraction(24)


def _clip(clip_id: str, name: str, start: int, duration: int,
          token: str, priority: int = 0) -> Clip:
    return Clip(
        clip_id=clip_id,
        name=name,
        start=Fraction(start),
        duration=Fraction(duration),
        source_in=Fraction(0),
        source_out=Fraction(duration),
        media=MediaRef(name=f"{name}.mov",
                       uri=f"sample://{name}-{token}.mov",
                       rate=RATE,
                       duration=Fraction(10_000)),
        priority=priority,
    )


def make_sample(seed: int = 41) -> Timeline:
    token = f"{random.Random(seed).getrandbits(32):08x}"
    video = Track(name="video", kind="video", mode="exclusive", items=[
        _clip("intro", "intro", 0, 100, token, priority=10),
        _clip("dialog", "dialog", 70, 70, token, priority=20),
        _clip("fx", "fx", 120, 40, token, priority=30),
    ])
    audio = Track(name="audio", kind="audio", mode="layered", items=[
        _clip("amb_a", "amb_a", 0, 120, token),
        _clip("amb_b", "amb_b", 60, 120, token),
    ])
    return Timeline(name="cutline-demo", rate=RATE, tracks=[video, audio])
