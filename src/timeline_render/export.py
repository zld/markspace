"""Frame digest exporter.

Calls the shared compositor for every frame, hashes the final RGBA bytes with
SHA-256, and reports audio-cue alignment. No images are ever written — only
frame numbers, digests, and alignment errors leave the process.
"""

from __future__ import annotations

import hashlib
from fractions import Fraction

from .compose import compose_frame, load_images
from .model import Timeline


def audio_alignment_error(timeline: Timeline, frame: int) -> int | float | None:
    """Distance in samples from this frame's start boundary to the nearest
    audio event, computed with exact rationals. None when no audio events."""
    if timeline.sample_rate <= 0 or not timeline.events:
        return None
    boundary = timeline.frame_start_sample(frame)
    err = min(abs(ev.sample - boundary) for ev in timeline.events)
    return err.numerator if err.denominator == 1 else float(err)


def render_summary(timeline: Timeline, width: int, height: int) -> dict:
    """Compose every frame and build the verifiable summary.

    The summary is assembled in memory and returned whole; callers must not
    emit anything until this returns, so a failure never yields a partial
    digest list.
    """
    images = load_images(timeline, width, height)
    frames = []
    for frame in range(timeline.frame_count):
        rgba = compose_frame(timeline, images, width, height, frame)
        frames.append(
            {
                "frame": frame,
                "pixel_sha256": hashlib.sha256(rgba).hexdigest(),
                "audio_alignment_error_samples": audio_alignment_error(
                    timeline, frame
                ),
            }
        )
        del rgba  # release per-frame temporaries promptly
    return {"frame_count": timeline.frame_count, "frames": frames}
