"""The mixing graph: per-track nodes feeding a shared summing bus.

Both render paths call :meth:`MixGraph.process_block` with identical frame
ranges, which guarantees equal node semantics and reproducible output.
"""

from __future__ import annotations

import numpy as np

from .assets import apply_layout, read_audio
from .automation import GainCurve
from .metrics import LevelStats
from .project import Project, TrackSpec
from .resample import resample


class TrackNode:
    """One timeline track: placed signal, gain curve and level accumulator."""

    def __init__(self, spec: TrackSpec, project: Project) -> None:
        self.spec = spec
        self.curve = GainCurve(spec, project.project_rate)
        self.stats = LevelStats(project.clip_threshold)
        self.buffer = self._prepare(project)

    def _prepare(self, project: Project) -> np.ndarray:
        data, src_rate = read_audio(self.spec.file)
        # Empty file: keep a zero-length buffer of the right channel count.
        if data.shape[0] == 0:
            data = np.zeros((0, project.channels), dtype=np.float64)
        else:
            data = apply_layout(data, self.spec.layout, project.channels, self.spec.name)
        data = resample(data, src_rate, project.project_rate)
        timeline = np.zeros((project.frames, project.channels), dtype=np.float64)
        start = self.spec.start_frame
        if start >= project.frames or data.shape[0] == 0:
            return timeline  # fully beyond project end, or empty source
        end = min(start + data.shape[0], project.frames)  # overlong sources truncated
        timeline[start:end] = data[: end - start]
        return timeline

    def process_block(self, frame_start: int, count: int) -> np.ndarray:
        """Apply gain automation for one block and update track statistics."""
        block = self.buffer[frame_start: frame_start + count]
        gain = self.curve.multiplier(frame_start, count)[:, np.newaxis]
        rendered = block * gain
        self.stats.add(rendered)
        return rendered


class MixGraph:
    """All track nodes plus the summing bus and its level accumulator."""

    def __init__(self, project: Project) -> None:
        self.project = project
        self.nodes = [TrackNode(spec, project) for spec in project.tracks]
        self.bus_stats = LevelStats(project.clip_threshold)

    def process_block(self, frame_start: int, count: int) -> np.ndarray:
        """Render one project block; frame_start is absolute project time."""
        if count <= 0:
            return np.zeros((0, self.project.channels), dtype=np.float64)
        bus = np.zeros((count, self.project.channels), dtype=np.float64)
        for node in self.nodes:
            bus += node.process_block(frame_start, count)
        self.bus_stats.add(bus)
        return bus
