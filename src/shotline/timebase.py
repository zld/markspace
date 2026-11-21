"""Frame clock and audio timing math.

All time arithmetic uses :class:`fractions.Fraction` (or exact integer
microseconds) so that long timelines never accumulate floating-point drift.
Frame count is the ceiling of ``duration * fps`` and every produced frame is
guaranteed to start strictly before the timeline's end.
"""

from __future__ import annotations

from dataclasses import dataclass
from fractions import Fraction

from .model import Cue, Timeline


@dataclass(frozen=True)
class FrameClock:
    fps: Fraction
    duration: float
    count: int

    @property
    def frame_period(self) -> Fraction:
        """Duration of one frame in seconds, exact."""
        return Fraction(1, 1) / self.fps

    def pts_seconds(self, index: int) -> Fraction:
        """Exact presentation timestamp (seconds) of frame ``index``."""
        if not 0 <= index < self.count:
            raise IndexError(f"frame index {index} out of range 0..{self.count - 1}")
        return index * self.frame_period

    def pts_us(self, index: int) -> int:
        """Presentation timestamp in whole microseconds (exact for rational fps)."""
        pts = self.pts_seconds(index)
        return int(pts * 1_000_000)

    def all_pts_us(self) -> list[int]:
        return [self.pts_us(i) for i in range(self.count)]


def build_clock(timeline: Timeline) -> FrameClock:
    fps = timeline.fps
    # ceil(duration * fps), computed without float: duration is a finite float
    # but the product is rounded away from zero via Fraction rounding.
    product = Fraction(timeline.duration).limit_denominator(1_000_000_000) * fps
    count = product.numerator // product.denominator
    if product.numerator % product.denominator:
        count += 1
    if count <= 0:
        raise ValueError("timeline yields no frames")
    # The last frame may not start at or beyond the timeline duration.
    last_pts = (count - 1) * (Fraction(1, 1) / fps)
    if last_pts >= timeline.duration:
        # Defensive: ceiling math should make this unreachable for sane input.
        count -= 1
    return FrameClock(fps=fps, duration=timeline.duration, count=count)


@dataclass(frozen=True)
class CueAlignment:
    sample_rate: int
    sample_index: int
    cue_time_s: float          # audio cue time in seconds
    frame_index: int           # nearest frame index
    frame_pts_s: float         # that frame's exact pts in seconds
    abs_error_ms: float        # |cue_time - frame_pts| in milliseconds


def _nearest_frame(clock: FrameClock, cue_seconds: Fraction) -> tuple[int, Fraction]:
    period = clock.frame_period
    # Round to nearest frame, half-up; clamp into the valid frame range.
    scaled = cue_seconds / period
    idx = (scaled.numerator * 2 + scaled.denominator) // (2 * scaled.denominator)
    idx = max(0, min(idx, clock.count - 1))
    return idx, idx * period


def align_cues(timeline: Timeline, clock: FrameClock) -> list[CueAlignment]:
    """Align every audio cue to its nearest picture frame and measure the gap."""
    out: list[CueAlignment] = []
    for cue in timeline.cues:
        cue_s = Fraction(cue.sample_index, cue.sample_rate)
        idx, pts = _nearest_frame(clock, cue_s)
        error_ms = abs(float(cue_s - pts)) * 1000.0
        out.append(
            CueAlignment(
                sample_rate=cue.sample_rate,
                sample_index=cue.sample_index,
                cue_time_s=float(cue_s),
                frame_index=idx,
                frame_pts_s=float(pts),
                abs_error_ms=error_ms,
            )
        )
    return out


def summarise_av(alignments: list[CueAlignment]) -> dict[str, float]:
    """Return max/mean absolute A/V error in milliseconds (0.0 when no cues)."""
    if not alignments:
        return {"max_abs_av_error_ms": 0.0, "mean_abs_av_error_ms": 0.0}
    errors = [a.abs_error_ms for a in alignments]
    return {
        "max_abs_av_error_ms": max(errors),
        "mean_abs_av_error_ms": sum(errors) / len(errors),
    }
