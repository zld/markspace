"""The reusable mix graph: sources -> tracks -> buses -> master.

Every gain decision is evaluated from absolute output frame positions, so the
same graph can be rendered in one pass or in arbitrary blocks and land on the
same samples. Blocks are addressed by position; nothing is relative to a
block start.
"""

from __future__ import annotations

from dataclasses import dataclass

import numpy as np

from .audio_io import SourceInfo, probe, read_all
from .automation import AutomationCurve
from .errors import ManifestError, RenderError
from .manifest import Session, Track
from .resample import (
    apply_layout,
    db_to_gain,
    ms_to_frames,
    output_frame_count,
    resample_range,
    source_window,
)

MAX_FRAMES = 48000 * 60 * 60  # 1 hour at 48 kHz, guards against bad manifests


@dataclass(frozen=True)
class TrackProgram:
    track: Track
    info: SourceInfo
    data: np.ndarray
    start_frame: int
    out_frames: int
    matrix: np.ndarray
    base_gain: float
    bus: str

    @property
    def end_frame(self) -> int:
        return self.start_frame + self.out_frames


@dataclass(frozen=True)
class BlockResult:
    start: int
    master: np.ndarray
    tracks: dict[str, np.ndarray]


def _gain_envelope(curve: AutomationCurve | None, base_gain: float,
                   start: int, end: int, rate: int) -> np.ndarray:
    frames = np.arange(start, end, dtype=np.float64)
    if curve is None:
        return np.full(end - start, base_gain, dtype=np.float64)
    return base_gain * curve.gains_at_ms(frames * 1000.0 / rate)


class MixGraph:
    """Compiled, immutable session plus per-render position state."""

    def __init__(self, session: Session, rate: int,
                 duration_frames: int | None = None) -> None:
        if rate <= 0:
            raise ManifestError("output rate must be > 0", field="--rate")
        self.session = session
        self.rate = rate
        self.buses = session.bus_by_id()
        programs: list[TrackProgram] = []
        for track in session.tracks:
            programs.append(self._load_track(track, rate))
        self.programs = tuple(programs)

        content_end = max((p.end_frame for p in programs), default=0)
        if duration_frames is None:
            if session.duration_ms is not None:
                duration_frames = ms_to_frames(session.duration_ms, rate)
            else:
                duration_frames = content_end
        if duration_frames <= 0:
            raise ManifestError("session duration is zero frames", field="duration_ms")
        if duration_frames > MAX_FRAMES:
            raise ManifestError(
                f"session duration exceeds {MAX_FRAMES} frames", field="duration_ms")
        if content_end > duration_frames:
            overflow = max(self.programs, key=lambda p: p.end_frame)
            raise ManifestError(
                f"track content ends at frame {content_end}, beyond the target "
                f"duration of {duration_frames} frames (last track "
                f"'{overflow.track.id}')",
                field="duration_ms", track_id=overflow.track.id,
            )
        self.total_frames = duration_frames

    def _load_track(self, track: Track, rate: int) -> TrackProgram:
        info = probe(track.path, track_id=track.id)
        data = read_all(info, track_id=track.id)
        if track.channel_count != info.channels:
            raise RenderError(
                f"manifest declares {track.channel_count} channel(s) but source "
                f"has {info.channels}",
                track_id=track.id, field="channels",
            )
        matrix = np.array(track.channel_matrix, dtype=np.float64)
        # apply_layout validates width; do it here so the error precedes rendering.
        apply_layout(data[:1], track.channel_matrix, track_id=track.id)
        out_frames = output_frame_count(info.frames, info.rate, rate)
        return TrackProgram(
            track=track,
            info=info,
            data=data,
            start_frame=ms_to_frames(track.offset_ms, rate),
            out_frames=out_frames,
            matrix=matrix,
            base_gain=db_to_gain(track.gain_db),
            bus=track.bus,
        )

    def gain_samples(self, times_ms: np.ndarray) -> dict[str, np.ndarray]:
        """Per-track gain (base x curve) at review times, including buses."""
        result: dict[str, np.ndarray] = {}
        ones = np.ones_like(times_ms, dtype=np.float64)
        for program in self.programs:
            curve = program.track.automation
            curve_gain = curve.gains_at_ms(times_ms) if curve is not None else ones
            result[program.track.id] = program.base_gain * curve_gain
        return result

    def bus_gain_samples(self, times_ms: np.ndarray) -> dict[str, np.ndarray]:
        return {
            bus_id: db_to_gain(bus.gain_db) * (
                bus.automation.gains_at_ms(times_ms)
                if bus.automation is not None else 1.0
            )
            for bus_id, bus in self.buses.items()
        }

    def render_block(self, start: int, frames: int) -> BlockResult:
        """Render ``[start, start+frames)``; pure function of absolute position."""
        if start < 0 or frames < 0 or start + frames > self.total_frames:
            raise RenderError(
                f"block [{start}, {start + frames}) is outside the session",
                field="block-frames",
            )
        end = start + frames
        master = np.zeros((frames, 2), dtype=np.float64)
        track_sums: dict[str, np.ndarray] = {}
        bus_sums: dict[str, np.ndarray] = {}

        for program in self.programs:
            contribution = self._render_track(program, start, end)
            track_sums[program.track.id] = contribution
            bus = bus_sums.setdefault(
                program.bus, np.zeros((frames, 2), dtype=np.float64))
            bus += contribution

        master += bus_sums.pop("master", np.zeros((frames, 2), dtype=np.float64))
        for bus_id, bus_sum in bus_sums.items():
            bus = self.buses[bus_id]
            env = _gain_envelope(bus.automation, db_to_gain(bus.gain_db),
                                 start, end, self.rate)
            master += bus_sum * env[:, None]
        return BlockResult(start=start, master=master, tracks=track_sums)

    def _render_track(self, program: TrackProgram, start: int, end: int) -> np.ndarray:
        out = np.zeros((end - start, 2), dtype=np.float64)
        lo = max(start, program.start_frame)
        hi = min(end, program.end_frame)
        if hi <= lo:
            return out
        local_start = lo - program.start_frame
        local_end = hi - program.start_frame
        ratio = program.info.rate / self.rate
        src_a, src_b = source_window(local_start, local_end, ratio,
                                     program.info.frames)
        chunk = resample_range(
            program.data[src_a:src_b], src_a, program.info.rate, self.rate,
            local_start, local_end, track_id=program.track.id,
        )
        chunk = chunk @ program.matrix.T
        env = _gain_envelope(program.track.automation, program.base_gain,
                             lo, hi, self.rate)
        out[lo - start:hi - start] = chunk * env[:, None]
        return out
