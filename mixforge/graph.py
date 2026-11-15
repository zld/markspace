"""Mix graph: track nodes -> gain/automation/pan -> bus sum -> output.

The graph is evaluated block by block on the absolute output-frame
timeline.  Track nodes own their decoder and resampler state, so both
the offline engine and the chunked streaming executor drive the exact
same node code; summation always follows manifest order.
"""

from __future__ import annotations

import numpy as np
import soundfile as sf

from .automation import GainCurve
from .errors import ManifestError, MediaError
from .manifest import Project, TrackSpec
from .resampler import StreamResampler

IO_CHUNK = 4096
BUS_CHANNELS = 2


def pan_gains_mono(pan: float) -> tuple[float, float]:
    """Constant-power pan, center-compensated to unity per channel."""
    ang = (pan + 1.0) * np.pi / 4.0
    return float(np.sqrt(2.0) * np.cos(ang)), float(np.sqrt(2.0) * np.sin(ang))


def pan_gains_stereo(pan: float) -> tuple[float, float]:
    """Balance for stereo sources: attenuate the opposite side."""
    return min(1.0, 1.0 - pan), min(1.0, 1.0 + pan)


class TrackNode:
    """Decode -> resample -> channel map -> gain/pan for one track."""

    def __init__(self, spec: TrackSpec, target_rate: int):
        self.spec = spec
        self.name = spec.name
        try:
            self._sf = sf.SoundFile(str(spec.path), "r")
        except Exception as exc:
            raise MediaError(f"track {spec.name!r}: cannot open {spec.path}: {exc}") from exc
        self.src_rate = int(self._sf.samplerate)
        self.src_channels = int(self._sf.channels)
        self.src_frames = len(self._sf)
        if self.src_channels not in (1, 2):
            self._sf.close()
            raise MediaError(
                f"track {spec.name!r}: unsupported channel count {self.src_channels}"
            )
        if self.src_frames == 0:
            self._sf.close()
            raise MediaError(f"track {spec.name!r}: no decodable frames")
        self.resampler = StreamResampler(self.src_rate, target_rate, self.src_channels, self.src_frames)
        self.out_frames = self.resampler.total_out
        self.start_frame = spec.start_frame
        self.static_gain = 10.0 ** (spec.gain_db / 20.0)
        self.curve = GainCurve(spec.automation, spec.interp) if spec.automation else None
        self._cache = np.zeros((0, self.src_channels))
        self._cache_start = 0
        self._fed_all = False

    def close(self) -> None:
        self._sf.close()

    def _ensure(self, local_end: int) -> None:
        """Decode/resample forward until cached output covers local_end."""
        while self._cache_start + len(self._cache) < local_end:
            if not self._fed_all:
                try:
                    chunk = self._sf.read(IO_CHUNK, dtype="float64", always_2d=True)
                except Exception as exc:
                    raise MediaError(f"track {self.name!r}: decode failed: {exc}") from exc
                if len(chunk) == 0:
                    self._fed_all = True
                    self.resampler.finish()
                    if self.resampler.fed != self.src_frames:
                        raise MediaError(
                            f"track {self.name!r}: truncated audio "
                            f"({self.resampler.fed}/{self.src_frames} frames)"
                        )
                else:
                    if not np.isfinite(chunk).all():
                        raise MediaError(f"track {self.name!r}: non-finite samples in input")
                    self.resampler.feed(chunk)
            got = self.resampler.read(local_end - (self._cache_start + len(self._cache)))
            if len(got) == 0:
                break
            self._cache = np.concatenate((self._cache, got)) if len(self._cache) else got

    def _to_stereo(self, seg: np.ndarray) -> np.ndarray:
        if self.src_channels == 1:
            gl, gr = pan_gains_mono(self.spec.pan)
            return np.column_stack((seg[:, 0] * gl, seg[:, 0] * gr))
        gl, gr = pan_gains_stereo(self.spec.pan)
        return seg * np.array([gl, gr])

    def render(self, out_start: int, out_count: int) -> np.ndarray:
        """Track contribution on the bus for output frames [start, start+count)."""
        out = np.zeros((out_count, BUS_CHANNELS))
        local0 = out_start - self.start_frame
        lo, hi = max(local0, 0), min(local0 + out_count, self.out_frames)
        if hi <= lo:
            return out
        self._ensure(hi)
        hi = min(hi, self._cache_start + len(self._cache))
        if hi <= lo:
            return out
        seg = self._cache[lo - self._cache_start : hi - self._cache_start]
        if lo > self._cache_start:  # drop frames no future block can need
            self._cache = self._cache[lo - self._cache_start :]
            self._cache_start = lo
        stereo = self._to_stereo(seg)
        frames = np.arange(lo, hi) + self.start_frame  # automation on output timeline
        gains = self.curve.linear_at(frames) if self.curve else np.ones(hi - lo)
        out[lo - local0 : hi - local0] = stereo * (gains * self.static_gain)[:, None]
        return out


class MixGraph:
    """Track nodes summed in manifest order into a stereo bus."""

    def __init__(self, project: Project, target_rate: int, master_gain_db: float = 0.0):
        if not project.tracks:
            raise ManifestError("empty project: no tracks")
        self.target_rate = target_rate
        self.tracks = [TrackNode(spec, target_rate) for spec in project.tracks]
        self.frames = max(t.start_frame + t.out_frames for t in self.tracks)
        if self.frames <= 0:
            raise ManifestError("empty project: zero output frames")
        self.master_gain = 10.0 ** (master_gain_db / 20.0)

    def close(self) -> None:
        for t in self.tracks:
            t.close()

    def __enter__(self) -> "MixGraph":
        return self

    def __exit__(self, *exc) -> None:
        self.close()

    def render_block(self, out_start: int, out_count: int) -> tuple[np.ndarray, list[np.ndarray]]:
        """Return (mix, per-track contributions) for one output block."""
        mix = np.zeros((out_count, BUS_CHANNELS))
        contribs: list[np.ndarray] = []
        for t in self.tracks:  # fixed manifest order keeps runs reproducible
            c = t.render(out_start, out_count)
            mix += c
            contribs.append(c)
        return mix * self.master_gain, contribs
