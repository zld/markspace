"""Shared mix graph: tracks -> buses -> master, evaluated per block.

Both the offline renderer and the streaming renderer call
:meth:`MixGraph.render_block`.  Every output sample is computed from its
absolute timeline position, so changing the block size cannot move a
keyframe, a loop boundary, or a track onset.
"""

from __future__ import annotations

from dataclasses import dataclass, field
from pathlib import Path

import numpy as np

from .automation import AutomationCurve, db_to_linear, fade_amplitude
from .errors import ConfigError, ProcessingError
from .io import WavSource
from .model import Project, Track
from .resample import output_length


def loop_map(timeline: np.ndarray, project: Project) -> np.ndarray:
    """Map output frames to project-content frames for loop playback.

    Tiles inside ``[start, start + span * count)`` repeat the segment; other
    frames pass through.  Automation is deliberately evaluated at the
    returned *output* time, never at this mapped time, so repeats do not
    reset keyframes.
    """

    positions = np.asarray(timeline, dtype=np.int64)
    loop = project.loop
    if loop is None:
        return positions.copy()
    mapped = positions.copy()
    in_loop = (positions >= loop.start_frame) & (positions < loop.expanded_end)
    mapped[in_loop] = loop.start_frame + (positions[in_loop] - loop.start_frame) % loop.span
    return mapped


@dataclass
class TrackRuntime:
    track: Track
    source: WavSource
    clip_frames: int
    curve: AutomationCurve | None
    _fade_in: int = 0
    _fade_out: int = 0

    def close(self) -> None:
        self.source.close()

    def _adapt_channels(self, data: np.ndarray, channels: int) -> np.ndarray:
        if data.shape[1] == channels:
            return data
        if data.shape[1] == 1:
            return np.repeat(data, channels, axis=1)
        if channels == 1:
            return data.mean(axis=1, keepdims=True)
        raise ProcessingError(
            f"cannot adapt {data.shape[1]} channels to {channels}",
            location=f"track[{self.track.id}]",
        )

    def _source_positions(self, output_positions: np.ndarray, target_rate: int) -> np.ndarray:
        ratio = target_rate / self.source.sample_rate
        return output_positions.astype(np.float64) * ratio

    def _resampled(
        self,
        output_positions: np.ndarray,
        target_rate: int,
        block_start: int,
        block_frames: int,
    ) -> np.ndarray:
        """Interpolate the source at clip-local *output* positions.

        Fades are applied here, on the source clip's own frame axis, before
        timeline gain and before the block boundary truncates anything.
        """

        count = output_positions.size
        if count == 0:
            return np.empty((0, self.source.channels), dtype=np.float64)
        source_pos = self._source_positions(output_positions, target_rate)
        left = np.floor(source_pos).astype(np.int64)
        np.clip(left, 0, self.source.frames - 1, out=left)
        right = np.minimum(left + 1, self.source.frames - 1)
        fraction = source_pos - left
        low, high = int(left.min()), int(right.max())
        chunk = self.source.read(
            low, high - low + 1, block_start=block_start, block_frames=block_frames
        )
        data = chunk[left - low] * (1.0 - fraction)[:, None] + chunk[right - low] * fraction[:, None]
        if self._fade_in > 0 or self._fade_out > 0:
            ratio = target_rate / self.source.sample_rate
            fade_in = int(np.floor(self._fade_in * ratio + 0.5))
            fade_out = int(np.floor(self._fade_out * ratio + 0.5))
            data = data * fade_amplitude(
                self.clip_frames,
                fade_in,
                fade_out,
                output_positions,
                location=f"track[{self.track.id}].fade",
            )[:, None]
        return data

    def render(self, timeline: np.ndarray, project: Project, block_start: int) -> np.ndarray:
        channels = project.channels
        block_frames = timeline.size
        contribution = np.zeros((block_frames, channels), dtype=np.float64)
        if self.track.mute or self.clip_frames == 0:
            return contribution
        mapped = loop_map(timeline, project)
        local = mapped - np.int64(self.track.start_frame)
        valid = (local >= 0) & (local < self.clip_frames)
        if not np.any(valid):
            return contribution
        valid_positions = local[valid]
        samples = self._resampled(valid_positions, project.sample_rate, block_start, block_frames)
        samples = self._adapt_channels(samples, channels)
        gains = (
            db_to_linear(self.curve.gain_db_at(timeline) + np.float64(self.track.gain_db))
            if self.curve is not None
            else np.full(block_frames, float(db_to_linear(self.track.gain_db)), dtype=np.float64)
        )
        contribution[valid] = samples * gains[valid][:, None]
        return contribution


@dataclass
class BlockResult:
    start: int
    frames: int
    tracks: dict[str, np.ndarray]
    buses: dict[str, np.ndarray]
    master_pre_limit: np.ndarray
    master: np.ndarray

    @property
    def channels(self) -> int:
        return int(self.master.shape[1])


class MixGraph:
    """Compiled project graph.  Construct once, render any block partition."""

    def __init__(self, project: Project) -> None:
        if project.frames < 0 or project.sample_rate <= 0:
            raise ConfigError("project duration and sample rate must be valid", location="project")
        self.project = project
        self._runtimes: list[TrackRuntime] = []
        self._buses = {bus.id: bus for bus in project.buses}
        try:
            for track in project.tracks:
                self._runtimes.append(self._compile_track(track))
        except Exception:
            self.close()
            raise

    @property
    def runtimes(self) -> tuple[TrackRuntime, ...]:
        return tuple(self._runtimes)

    def _compile_track(self, track: Track) -> TrackRuntime:
        path = Path(track.path)
        if not path.is_absolute():
            path = self.project.source_dir / path
        source = WavSource(path, track_id=track.id)
        clip_frames = output_length(source.frames, source.sample_rate, self.project.sample_rate)
        curve = None
        if track.automation is not None:
            curve = AutomationCurve.from_spec(
                track.automation, location=f"track[{track.id}].automation"
            )
        # Validate fades at build time, including zero-length clips.
        fade_amplitude(
            clip_frames,
            track.fade.in_frames,
            track.fade.out_frames,
            np.empty(0, dtype=np.int64),
            location=f"track[{track.id}].fade",
        )
        ratio = self.project.sample_rate / source.sample_rate
        return TrackRuntime(
            track=track,
            source=source,
            clip_frames=clip_frames,
            curve=curve,
            _fade_in=int(np.floor(track.fade.in_frames * ratio + 0.5)),
            _fade_out=int(np.floor(track.fade.out_frames * ratio + 0.5)),
        )

    def close(self) -> None:
        for runtime in self._runtimes:
            runtime.close()

    def __enter__(self) -> "MixGraph":
        return self

    def __exit__(self, exc_type: object, exc: object, tb: object) -> None:
        self.close()

    def render_block(self, start: int, frames: int) -> BlockResult:
        if start < 0 or frames < 0 or start + frames > self.project.frames:
            raise ProcessingError(
                "render block is outside the project duration",
                location="render_block",
                context={"start": start, "frames": frames, "project_frames": self.project.frames},
            )
        timeline = np.arange(start, start + frames, dtype=np.int64)
        track_data: dict[str, np.ndarray] = {}
        bus_data = {
            bus_id: np.zeros((frames, self.project.channels), dtype=np.float64)
            for bus_id in self._buses
        }
        direct = np.zeros((frames, self.project.channels), dtype=np.float64)
        for runtime in self._runtimes:
            data = runtime.render(timeline, self.project, start)
            track_data[runtime.track.id] = data
            if runtime.track.bus is None:
                direct = direct + data
            else:
                bus_data[runtime.track.bus] = bus_data[runtime.track.bus] + data
        master = direct
        for bus_id, bus in self._buses.items():
            data = bus_data[bus_id]
            if bus.mute:
                data = np.zeros_like(data)
            else:
                data = data * float(db_to_linear(bus.gain_db))
            bus_data[bus_id] = data
            master = master + data
        master = master * float(db_to_linear(self.project.master.gain_db))
        pre_limit = master
        if self.project.master.limiter:
            ceiling = float(self.project.master.ceiling)
            master = np.clip(master, -ceiling, ceiling)
        if not np.all(np.isfinite(master)):
            raise ProcessingError(
                "mix produced NaN or infinite samples",
                location="master",
                context={"block_start": start, "block_frames": frames},
            )
        return BlockResult(
            start=start,
            frames=frames,
            tracks=track_data,
            buses=bus_data,
            master_pre_limit=pre_limit,
            master=master,
        )
