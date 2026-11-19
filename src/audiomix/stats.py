"""Peak, RMS, valid-frame, and clipping statistics.

Definitions
-----------
* ``peak`` is the maximum absolute sample over *effective* frames only. A
  silent block therefore reports 0.0, not -inf.
* ``rms`` is ``sqrt(mean(x**2))`` over effective frames; it is exactly 0.0 for
  silence and for tracks with no effective frames.
* ``valid_frames`` counts frames that correspond to real output. Look-ahead
  padding used by the streaming driver is excluded.
* ``clip_count`` counts samples whose absolute amplitude is strictly greater
  than 1.0, per the project definition. It is reported per track, for the bus,
  and for the final output. With the ``limit`` policy the final output cannot
  clip, but track and bus counts still show what was limited.
* Non-finite samples are a hard error: ``AudioMixError`` rather than silently
  poisoning a peak or RMS.
"""

from __future__ import annotations

from dataclasses import dataclass, field

import numpy as np

from .errors import AudioMixError

CLIP_THRESHOLD = 1.0


@dataclass
class SignalStats:
    """Running statistics for one signal (track, bus, or master)."""

    name: str
    channels: int
    peak: float = 0.0
    valid_frames: int = 0
    clip_count: int = 0
    _sum_squares: float = 0.0

    def add(self, block: np.ndarray) -> None:
        """Accumulate a block of effective frames (frames x channels)."""
        if block.size == 0:
            return
        if not np.all(np.isfinite(block)):
            raise AudioMixError(f"non-finite samples reached statistics for '{self.name}'")
        self.valid_frames += int(block.shape[0])
        magnitudes = np.abs(block)
        self.peak = max(self.peak, float(magnitudes.max()))
        self._sum_squares += float(np.dot(block.ravel(), block.ravel()))
        self.clip_count += int(np.count_nonzero(magnitudes > CLIP_THRESHOLD))

    @property
    def rms(self) -> float:
        samples = self.valid_frames * max(1, self.channels)
        if samples == 0:
            return 0.0
        # Clamp tiny negative rounding residue before the square root.
        return float(np.sqrt(max(0.0, self._sum_squares / samples)))

    def to_dict(self) -> dict[str, float | int]:
        return {
            "peak": self.peak,
            "rms": self.rms,
            "valid_frames": self.valid_frames,
            "clip_count": self.clip_count,
        }


def signal_stats(name: str, channels: int) -> SignalStats:
    return SignalStats(name=name, channels=int(channels))


@dataclass
class RenderStats:
    """Aggregated statistics for a whole render."""

    tracks: list[SignalStats] = field(default_factory=list)
    bus: SignalStats | None = None
    master: SignalStats | None = None

    def add_block(
        self,
        track_blocks: dict[str, np.ndarray],
        bus: np.ndarray,
        master: np.ndarray,
    ) -> None:
        by_name = {stats.name: stats for stats in self.tracks}
        for name, block in track_blocks.items():
            by_name[name].add(block)
        if self.bus is not None:
            self.bus.add(bus)
        if self.master is not None:
            self.master.add(master)

    def to_dict(self) -> dict[str, object]:
        return {
            "tracks": {stats.name: stats.to_dict() for stats in self.tracks},
            "bus": None if self.bus is None else self.bus.to_dict(),
            "master": None if self.master is None else self.master.to_dict(),
        }
