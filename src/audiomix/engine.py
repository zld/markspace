"""Render orchestration: shared graphs for offline and chunked streaming.

A project is rendered twice: once with a very large block and once with the
caller's chunk size. Both passes build the same graph topology and node
classes; only the block sizes differ, so any difference in output would mean
state leaks across block boundaries. The offline reference is held in a
chunked bridge and compared frame by frame against the streaming output, so
the full mix is never resident in memory twice.
"""

from __future__ import annotations

import os
from dataclasses import dataclass
from pathlib import Path

import numpy as np

from .automation import (
    AutomationCurve,
    automation_samples,
    automation_samples_at,
    build_curve,
)
from .graph import READ_AHEAD_MIN, ConsistencyError, SourceNode, TrackGraph, WavInfo
from .nodes import BusNode
from .model import (
    CONSISTENCY_TOLERANCE,
    TARGET_CHANNELS,
    TARGET_SAMPLE_RATE,
    ManifestError,
    Project,
    resampled_frames,
)
from .report import AtomicWavWriter, RenderReport, TrackReport

OFFLINE_BLOCK = 1 << 14
MIN_AUTOMATION_SAMPLES = 3


@dataclass
class _TrackAccumulator:
    """Post-automation, pre-bus statistics for one track.

    RMS is the root mean square over all channel samples (frames x channels),
    matching how the mixed stereo file is measured.
    """

    frames: int = 0
    samples: int = 0
    sum_squares: float = 0.0
    peak: float = 0.0

    def add(self, block: np.ndarray, produced: int) -> None:
        if produced <= 0:
            return
        real = block[:, :produced]
        self.frames += produced
        self.samples += real.size
        self.sum_squares += float(np.sum(real * real))
        self.peak = max(self.peak, float(np.max(np.abs(real))))

    def rms(self) -> float:
        if self.samples == 0:
            return 0.0
        return float(np.sqrt(self.sum_squares / self.samples))


class _MixPass:
    """One graph set driven at a fixed block size, yielding stereo mix blocks."""

    def __init__(self, project: Project, paths: list[Path], curves: list[AutomationCurve],
                 frames_total: int, block: int, read_block: int):
        self.project = project
        self.frames_total = frames_total
        self.block = block
        self.position = 0
        self.bus = BusNode(frames_total)
        self.accumulators = {track.name: _TrackAccumulator() for track in project.tracks}
        self.graphs: list[TrackGraph] = []
        # Internal reads may be larger than the processing block: extra frames
        # wait in the track buffer. This keeps tiny chunks practical without
        # changing block sizes seen by the resampler or gain nodes.
        read_block = max(block, READ_AHEAD_MIN)
        try:
            for track, path, curve in zip(project.tracks, paths, curves):
                self.graphs.append(
                    TrackGraph.create(
                        name=track.name,
                        path=str(path),
                        pan=track.pan,
                        gain_db=track.gain_db,
                        curve=curve,
                        read_block=read_block,
                    )
                )
        except Exception:
            self.close()
            raise
        self._result: tuple[int, int] | None = None

    def __iter__(self) -> "_MixPass":
        return self

    def __next__(self) -> np.ndarray:
        if self._result is not None:
            raise StopIteration
        if self.position >= self.frames_total:
            self._result = (self.position, self.bus.finish())
            raise StopIteration
        count = min(self.block, self.frames_total - self.position)
        blocks: list[np.ndarray] = []
        for graph in self.graphs:
            block, produced = graph.pull(count)
            self.accumulators[graph.name].add(block, produced)
            blocks.append(block)
        mix = self.bus.mix_block(blocks, count)
        self.position += count
        return mix

    @property
    def clip_count(self) -> int:
        if self._result is None:
            raise RuntimeError("pass has not completed")
        return self._result[1]

    def close(self) -> None:
        for graph in self.graphs:
            graph.close()


def probe_sources(project: Project, paths: list[Path]) -> list[WavInfo]:
    """Open each source once for validation and metadata."""
    info: list[WavInfo] = []
    for track, path in zip(project.tracks, paths):
        source = SourceNode(str(path), track.name)
        try:
            info.append(source.info)
        finally:
            source.close()
    return info


def _build_curves(project: Project, frames_total: int) -> list[AutomationCurve]:
    curves: list[AutomationCurve] = []
    for track in project.tracks:
        try:
            curves.append(
                build_curve(
                    track.automation,
                    frames_total=frames_total,
                    sample_rate=TARGET_SAMPLE_RATE,
                )
            )
        except ValueError as exc:
            raise ManifestError(str(exc), track.name, "automation") from exc
    return curves


def _track_reports(project: Project, info: list[WavInfo],
                   accumulators: dict[str, _TrackAccumulator]) -> list[TrackReport]:
    reports: list[TrackReport] = []
    for track, source_info in zip(project.tracks, info):
        accumulator = accumulators[track.name]
        reports.append(
            TrackReport(
                name=track.name,
                sample_rate_in=source_info.sample_rate,
                frames_in=source_info.frames,
                frames_out=resampled_frames(source_info.frames, source_info.sample_rate),
                peak=accumulator.peak,
                rms=accumulator.rms(),
            )
        )
    return reports


def _sample_automation(project: Project, curves: list[AutomationCurve],
                       frames_total: int, per_track: int,
                       explicit_frames: list[int] | None = None) -> list[dict]:
    rows: list[dict] = []
    for track, curve in zip(project.tracks, curves):
        if explicit_frames is None:
            samples = automation_samples(curve, per_track)
        else:
            samples = automation_samples_at(curve, explicit_frames)
        for frame, db in samples:
            rows.append({"track": track.name, "frame": frame, "gain_db": round(db, 9)})
    return rows


def _check_output_not_a_source(output_path: Path, paths: list[Path]) -> None:
    """Refuse to overwrite a manifest source, which would destroy the input."""
    target = output_path.resolve()
    for path in paths:
        if path.resolve() == target:
            raise ValueError(f"output path {output_path} would overwrite source {path}")


class _Bridge:
    """Holds offline blocks until the streaming pass has verified them.

    Offline blocks are large, streaming blocks can be tiny; keeping a list of
    chunks (instead of re-concatenating a growing buffer) keeps the comparison
    linear in the number of frames.
    """

    def __init__(self) -> None:
        self.chunks: list[np.ndarray] = []
        self.offset = 0
        self.remaining = 0

    def push(self, block: np.ndarray) -> None:
        if block.shape[1]:
            self.chunks.append(block)
            self.remaining += block.shape[1]

    def take(self, count: int) -> np.ndarray | None:
        """Return exactly ``count`` frames, or ``None`` if not enough remain."""
        if self.remaining < count:
            return None
        head = self.chunks[0]
        available = head.shape[1] - self.offset
        if available >= count:
            out = head[:, self.offset:self.offset + count]
            self.offset += count
            if self.offset == head.shape[1]:
                self.chunks.pop(0)
                self.offset = 0
            self.remaining -= count
            return out
        out = np.empty((2, count), dtype=np.float64)
        filled = 0
        while filled < count:
            head = self.chunks[0]
            available = head.shape[1] - self.offset
            used = min(available, count - filled)
            out[:, filled:filled + used] = head[:, self.offset:self.offset + used]
            filled += used
            self.offset += used
            if self.offset == head.shape[1]:
                self.chunks.pop(0)
                self.offset = 0
        self.remaining -= count
        return out

    def fill_from(self, offline: "_MixPass", count: int) -> np.ndarray | None:
        """Take ``count`` frames, pulling offline blocks as needed."""
        while self.remaining < count:
            try:
                self.push(next(offline))
            except StopIteration:
                break
        return self.take(count)


def render_project(project: Project, paths: list[Path],
                   output_path: str | os.PathLike[str],
                   chunk_frames: int,
                   automation_points: int = MIN_AUTOMATION_SAMPLES,
                   automation_frames: list[int] | None = None) -> RenderReport:
    """Render offline and chunked, verify agreement and atomically write the WAV.

    ``automation_frames`` samples the gain curves at explicit frames; otherwise
    ``automation_points`` evenly spaced samples per track are reported.

    Raises :class:`ConsistencyError` when the two paths differ by more than
    ``CONSISTENCY_TOLERANCE``; in that case no output file is left behind.
    """
    if chunk_frames <= 0:
        raise ValueError("chunk frames must be a positive integer")
    if automation_points <= 0:
        raise ValueError("automation sample count must be positive")
    if automation_frames is not None:
        for frame in automation_frames:
            if not isinstance(frame, int) or isinstance(frame, bool) or frame < 0:
                raise ValueError("automation frames must be non-negative integers")

    output_path = Path(output_path)
    _check_output_not_a_source(output_path, paths)

    info = probe_sources(project, paths)
    frames_total = max(resampled_frames(entry.frames, entry.sample_rate) for entry in info)
    if frames_total <= 0:
        raise ValueError("project renders to zero frames")
    curves = _build_curves(project, frames_total)
    if automation_frames is not None:
        for frame in automation_frames:
            if frame >= frames_total:
                raise ValueError(
                    f"automation frame {frame} is outside the rendered length "
                    f"{frames_total}"
                )

    offline = _MixPass(project, paths, curves, frames_total,
                       block=OFFLINE_BLOCK, read_block=OFFLINE_BLOCK)
    streaming = _MixPass(project, paths, curves, frames_total,
                         block=chunk_frames, read_block=chunk_frames)
    try:
        bridge = _Bridge()
        max_abs_diff = 0.0
        written = 0
        with AtomicWavWriter(output_path, TARGET_SAMPLE_RATE) as writer:
            for stream_mix in streaming:
                count = stream_mix.shape[1]
                if count <= 0:
                    continue
                reference = bridge.fill_from(offline, count)
                if reference is None:
                    raise ConsistencyError(
                        f"offline pass ended early: needed {count} frames, "
                        f"had {bridge.remaining}"
                    )
                diff = float(np.max(np.abs(reference - stream_mix)))
                max_abs_diff = max(max_abs_diff, diff)
                if max_abs_diff > CONSISTENCY_TOLERANCE:
                    raise ConsistencyError(
                        f"offline/streaming max abs diff {max_abs_diff:.3e} exceeds "
                        f"{CONSISTENCY_TOLERANCE:g}"
                    )
                writer.write(reference)
                written += count

            # Drain both passes so bus totals and per-track statistics finalise
            # before the writer publishes the file.
            for _ in offline:
                pass
            for _ in streaming:
                pass
            if offline.clip_count != streaming.clip_count:
                raise ConsistencyError(
                    f"clip counts diverged: offline {offline.clip_count}, "
                    f"streaming {streaming.clip_count}"
                )
            if bridge.remaining:
                raise ConsistencyError(
                    f"offline pass produced {bridge.remaining} unverified trailing frames"
                )
            if written != frames_total:
                raise ConsistencyError(
                    f"wrote {written} frames, expected {frames_total}"
                )
            clip_count = offline.clip_count
            accumulators = offline.accumulators
    finally:
        offline.close()
        streaming.close()

    return RenderReport(
        frames_out=frames_total,
        sample_rate=TARGET_SAMPLE_RATE,
        channels=TARGET_CHANNELS,
        output=str(output_path),
        tracks=_track_reports(project, info, accumulators),
        automation_samples=_sample_automation(
            project, curves, frames_total, automation_points, automation_frames
        ),
        clip_count=int(clip_count),
        offline_stream_max_abs_diff=max_abs_diff,
    )
