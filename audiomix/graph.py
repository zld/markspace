"""Self-implemented mix graph shared by the offline and streaming runners.

Each track is a node chain: input -> resample -> channel map -> automation.
Chains are pulled in blocks aligned to the absolute output timeline, and a
sum node adds the chains in manifest order.  Because every node keeps its
own state, pulling in different block sizes yields identical results.
"""
from __future__ import annotations

import numpy as np

from .curve import gain_linear_at
from .model import ProcessError
from .resample import SincResampler, output_frames

# Input frames pushed into a track's resampler node at a time.
FEED_FRAMES = 8192


def map_to_stereo(frames):
    """Channel-map node: mono is duplicated, stereo passes through."""
    if frames.shape[1] == 2:
        return frames
    return np.repeat(frames, 2, axis=1)


class TrackPipeline:
    """One track's node chain, pulled in blocks on the output timeline."""

    def __init__(self, name, source, in_rate, out_rate, offset_seconds, curve):
        self.name = name
        self.curve = curve
        self.out_rate = out_rate
        self.offset_frames = int(round(offset_seconds * out_rate))
        self.resampler = SincResampler(in_rate, out_rate, source.shape[1])
        self.source = source
        self.cursor = 0            # next source frame to feed
        self.flushed = False
        self.pending = np.zeros((0, 2))  # mapped audio waiting to be pulled
        self.body_frames = output_frames(source.shape[0], in_rate, out_rate)
        self.total_frames = self.offset_frames + self.body_frames
        self.pos = 0               # absolute output frame of the next pull

    def pull(self, count):
        """Return this track's contribution to output frames [pos, pos+count)."""
        out = np.zeros((count, 2))
        start, end = self.pos, self.pos + count
        lo = max(start, self.offset_frames)
        hi = min(end, self.offset_frames + self.body_frames)
        if lo < hi:
            body = self._take_body(hi - lo)
            gain = gain_linear_at(self.curve, np.arange(lo, hi), self.out_rate)
            out[lo - start:hi - start] = body * gain[:, None]
        self.pos = end
        return out

    def _take_body(self, need):
        while self.pending.shape[0] < need:
            if self.cursor < self.source.shape[0]:
                step = min(FEED_FRAMES, self.source.shape[0] - self.cursor)
                produced = self.resampler.process(
                    self.source[self.cursor:self.cursor + step])
                self.cursor += step
            elif not self.flushed:
                produced = self.resampler.flush()
                self.flushed = True
            else:  # pragma: no cover - guarded by body_frames bookkeeping
                raise ProcessError(
                    f"track '{self.name}': resampler ran dry before stream end")
            self.pending = np.concatenate([self.pending, map_to_stereo(produced)])
        out = self.pending[:need]
        self.pending = self.pending[need:]
        return out


class MixGraph:
    """Sum node over track pipelines; pulled in blocks of any size."""

    def __init__(self, pipelines, out_rate):
        if not pipelines:
            raise ProcessError("mix graph needs at least one track")
        self.pipelines = list(pipelines)
        self.out_rate = out_rate
        self.total_frames = max(p.total_frames for p in self.pipelines)
        self.pos = 0

    def pull(self, count):
        """Sum node: mix the next `count` output frames (short at the end)."""
        count = min(count, self.total_frames - self.pos)
        if count <= 0:
            return np.zeros((0, 2))
        acc = np.zeros((count, 2))
        for pipe in self.pipelines:
            acc += pipe.pull(count)
        self.pos += count
        return acc


def build_graph(tracks, out_rate):
    """Create a fresh mix graph from (TrackSpec, audio, in_rate) tuples."""
    pipelines = [
        TrackPipeline(spec.name, audio, in_rate, out_rate,
                      spec.offset_seconds, spec.curve)
        for spec, audio, in_rate in tracks
    ]
    return MixGraph(pipelines, out_rate)
