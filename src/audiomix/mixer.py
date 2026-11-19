"""Shared mixing graph used by both the offline and streaming drivers.

Design
------
Every track's source audio is loaded once and resampled once to the project
rate. The graph stores, per track, a *prepared source*: a float64 array whose
row ``k`` already reflects loop repetition where applicable. Mapping a
project frame to a prepared row is pure integer arithmetic:

* no loop: ``row = frame - start_frame`` (out of range -> silence)
* loop: the material before ``loop.start_frame`` plays once, then rows
  ``loop.start_frame .. loop.end_frame`` repeat until the session ends.

Both drivers call :meth:`MixGraph.render_frames` for a half-open project-frame
range. Nothing in this module knows about block sizes, so the offline path is
just one large range and the stream path is many small ranges over the same
function; that is what makes their outputs identical sample for sample.

Per-track processing order (fixed, shared by all drivers):

1. channel mapping / layout (source channels -> project channels)
2. static gain in dB converted to linear amplitude
3. automation curve sampled at the current project frame (absolute frames)
4. pan law applied to the project layout
5. accumulation into the bus
6. the clip policy applied once to the final master

The clip policy is deliberately applied only at the master: track and bus
stats then report where clipping really came from, instead of hiding it with
an early clamp. Under ``limit`` the master is clamped to +-1.0, so its
``clip_count`` is zero while track and bus counts still expose the overs.

Statistics are accumulated on the exact samples that are written, excluding
padding, so ``valid_frames`` describes real output only.
"""

from __future__ import annotations

import math
from dataclasses import dataclass
import numpy as np
import soundfile as sf

from .errors import TrackError
from .model import LoopRegion, Session, TrackSpec
from .resample import resample_linear


@dataclass(frozen=True)
class ClipPolicy:
    """How amplitude beyond +-1.0 is handled."""

    limit: bool = False

    @property
    def name(self) -> str:
        return "limit" if self.limit else "report"


POLICIES = {"limit": ClipPolicy(limit=True), "report": ClipPolicy(limit=False)}
DEFAULT_POLICY = "report"


def parse_policy(raw: str | None) -> ClipPolicy:
    key = (raw or DEFAULT_POLICY).lower()
    if key not in POLICIES:
        raise ValueError(f"unknown clip policy {raw!r}; expected one of {', '.join(POLICIES)}")
    return POLICIES[key]


def db_to_gain(db: float) -> float:
    return float(10.0 ** (db / 20.0))


def pan_gains(pan: float) -> tuple[float, float]:
    """Constant-power stereo gains for ``pan`` in [-1, 1] (L=cos, R=sin)."""
    angle = (pan + 1.0) * math.pi / 4.0
    return math.cos(angle), math.sin(angle)


def map_source_channels(data: np.ndarray, track: TrackSpec, layout: int) -> np.ndarray:
    """Expand ``data`` (frames x source_channels) to the project layout."""
    source_channels = int(data.shape[1])
    mapping = track.channel_map
    if mapping:
        if len(mapping) != source_channels:
            raise TrackError(
                track.name,
                f"channel_map has {len(mapping)} entries but the source has "
                f"{source_channels} channel(s)",
            )
        out = np.zeros((data.shape[0], layout), dtype=np.float64)
        for src, dst in enumerate(mapping):
            out[:, dst] += data[:, src]
        return out
    if source_channels == layout:
        return np.array(data, dtype=np.float64, copy=True)
    if source_channels == 1:
        return np.repeat(data[:, :1], layout, axis=1)
    raise TrackError(
        track.name,
        f"cannot map {source_channels} source channel(s) onto a {layout}-channel project "
        "layout without an explicit channel_map",
    )


def apply_pan(data: np.ndarray, pan: float) -> np.ndarray:
    if pan == 0.0:
        return data
    left, right = pan_gains(pan)
    out = np.array(data, copy=True)
    out[:, 0] *= left
    out[:, 1] *= right
    return out


@dataclass
class PreparedTrack:
    """Resampled, loop-expanded source rows for one track."""

    spec: TrackSpec
    source: np.ndarray
    loop: LoopRegion | None
    source_rate: int
    static_gain: float

def load_track(spec: TrackSpec, project_rate: int) -> PreparedTrack:
    """Load and resample one track, reporting every failure with its name."""
    try:
        data, rate = sf.read(str(spec.path), dtype="float64", always_2d=True)
    except Exception as exc:  # soundfile raises several unrelated types
        raise TrackError(spec.name, f"cannot read audio: {exc}") from exc
    if rate <= 0:
        raise TrackError(spec.name, f"invalid sample rate {rate}")
    if data.shape[0] == 0:
        raise TrackError(spec.name, "audio file contains no frames")
    if data.shape[1] == 0:
        raise TrackError(spec.name, "audio file has no channels")
    if not np.all(np.isfinite(data)):
        raise TrackError(spec.name, "audio contains NaN or Inf samples")
    converted = resample_linear(data, rate, project_rate)
    if spec.loop is not None and spec.loop.end_frame > converted.shape[0]:
        raise TrackError(
            spec.name,
            f"loop end_frame {spec.loop.end_frame} exceeds resampled length "
            f"{converted.shape[0]} frames",
        )
    return PreparedTrack(
        spec=spec,
        source=converted,
        loop=spec.loop,
        source_rate=rate,
        static_gain=db_to_gain(spec.gain_db),
    )


class MixGraph:
    """The single rendering graph shared by offline and streaming drivers."""

    def __init__(self, session: Session, project_rate: int, clip: ClipPolicy) -> None:
        self.session = session
        self.layout = session.channels
        self.project_rate = project_rate
        self.clip = clip
        self.tracks: list[PreparedTrack] = []
        for spec in session.tracks:
            if spec.pan != 0.0 and self.layout != 2:
                raise TrackError(spec.name, "pan requires a stereo project layout")
            self.tracks.append(load_track(spec, project_rate))

    # -- geometry ---------------------------------------------------------
    def track_natural_end(self, track: PreparedTrack) -> int:
        """Project frame where a track's material ends, ignoring the session cap."""
        spec = track.spec
        source_frames = track.source.shape[0]
        loop = track.loop
        if loop is not None:
            pre = loop.start_frame
            length = loop.length
            # One full cycle beyond the pre-roll; the session cap decides when
            # repetition actually stops.
            return spec.start_frame + pre + length
        return spec.start_frame + source_frames

    def total_frames(self) -> int:
        if self.session.frames is not None:
            return self.session.frames
        if not self.tracks:
            return 0
        return max(self.track_natural_end(t) for t in self.tracks)

    def source_index(self, track: PreparedTrack, frames: np.ndarray) -> np.ndarray:
        """Map project frames to source rows; ``-1`` marks silent frames."""
        spec = track.spec
        local = frames - spec.start_frame
        rows = np.full(frames.shape, -1, dtype=np.int64)
        source_frames = track.source.shape[0]
        loop = track.loop
        if loop is None:
            valid = (local >= 0) & (local < source_frames)
            rows[valid] = local[valid]
            return rows
        pre = loop.start_frame
        length = loop.length
        before = (local >= 0) & (local < pre)
        rows[before] = local[before]
        after = local >= pre
        if np.any(after):
            rows[after] = pre + np.mod(local[after] - pre, length)
        return rows

    # -- rendering --------------------------------------------------------
    def render_frames(self, start: int, count: int) -> dict[str, np.ndarray]:
        """Render ``count`` project frames starting at ``start``.

        Returns per-track and bus blocks. The master is the clipped or raw sum
        of the bus depending on the policy; callers should write ``master``.
        """
        frames = np.arange(start, start + count, dtype=np.int64)
        bus = np.zeros((count, self.layout), dtype=np.float64)
        per_track: dict[str, np.ndarray] = {}
        for track in self.tracks:
            block = self._render_track(track, frames)
            per_track[track.spec.name] = block
            bus += block
        master = np.clip(bus, -1.0, 1.0) if self.clip.limit else bus
        return {"tracks": per_track, "bus": bus, "master": master}

    def _render_track(self, track: PreparedTrack, frames: np.ndarray) -> np.ndarray:
        count = frames.shape[0]
        rows = self.source_index(track, frames)
        valid = rows >= 0
        block = np.zeros((count, self.layout), dtype=np.float64)
        if not np.any(valid):
            return block
        sampled = track.source[rows[valid]]
        sampled = map_source_channels(sampled, track.spec, self.layout)
        sampled *= track.static_gain
        gains = track.spec.automation.gain_at(frames[valid])
        sampled *= gains[:, None]
        sampled = apply_pan(sampled, track.spec.pan)
        block[valid] = sampled
        return block
