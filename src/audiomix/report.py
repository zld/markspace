"""Statistics reporting and atomic JSON/WAV serialisation."""

from __future__ import annotations

import json
import os
import tempfile
from dataclasses import dataclass, field
from pathlib import Path

import numpy as np
import soundfile as sf


@dataclass(frozen=True)
class TrackReport:
    name: str
    sample_rate_in: int
    frames_in: int
    frames_out: int
    peak: float
    rms: float


@dataclass
class RenderReport:
    frames_out: int
    sample_rate: int
    channels: int
    output: str
    tracks: list[TrackReport]
    automation_samples: list[dict] = field(default_factory=list)
    clip_count: int = 0
    offline_stream_max_abs_diff: float = 0.0

    def to_dict(self) -> dict:
        return {
            "frames_out": self.frames_out,
            "sample_rate": self.sample_rate,
            "channels": self.channels,
            "output": self.output,
            "tracks": [
                {
                    "name": t.name,
                    "sample_rate_in": t.sample_rate_in,
                    "frames_in": t.frames_in,
                    "frames_out": t.frames_out,
                    "peak": round(t.peak, 9),
                    "rms": round(t.rms, 9),
                }
                for t in self.tracks
            ],
            "automation_samples": [
                {"track": row["track"], "frame": int(row["frame"]),
                 "gain_db": round(float(row["gain_db"]), 9)}
                for row in self.automation_samples
            ],
            "clip_count": self.clip_count,
            "offline_stream_max_abs_diff": self.offline_stream_max_abs_diff,
        }

    def to_json(self, indent: int | None = 2) -> str:
        return json.dumps(self.to_dict(), ensure_ascii=False, indent=indent, sort_keys=False)


class AtomicWavWriter:
    """Streaming WAV writer that only publishes the file on clean close.

    Data is written to a sibling temporary file. ``commit`` (or a clean
    context-manager exit) atomically renames it into place; any exception
    removes it, so a failed render never leaves a plausible-looking output.
    """

    def __init__(self, path: str | os.PathLike[str], sample_rate: int,
                 channels: int = 2):
        if channels not in (1, 2):
            raise ValueError("only mono and stereo output is supported")
        self.target = Path(path)
        self.sample_rate = int(sample_rate)
        self.channels = channels
        self._handle = None
        self._tmp_name: str | None = None
        self._closed = False

    def __enter__(self) -> "AtomicWavWriter":
        parent = self.target.parent
        if parent and not parent.exists():
            parent.mkdir(parents=True, exist_ok=True)
        fd, self._tmp_name = tempfile.mkstemp(
            prefix=f".{self.target.name}.", suffix=".tmp", dir=str(parent)
        )
        os.close(fd)
        try:
            self._handle = sf.SoundFile(
                self._tmp_name, mode="w", samplerate=self.sample_rate,
                channels=self.channels, subtype="FLOAT", format="WAV",
            )
        except Exception:
            self._discard()
            raise
        return self

    def write(self, block: np.ndarray) -> None:
        """Append a ``(channels, frames)`` block."""
        if self._handle is None:
            raise RuntimeError("writer is not open")
        data = np.asarray(block, dtype=np.float64)
        if data.ndim != 2 or data.shape[0] != self.channels:
            raise ValueError("block must be shaped (channels, frames) for this writer")
        self._handle.write(data.T)

    def commit(self) -> None:
        if self._closed:
            return
        if self._handle is None or self._tmp_name is None:
            raise RuntimeError("writer is not open")
        self._handle.close()
        self._handle = None
        os.replace(self._tmp_name, self.target)
        self._tmp_name = None
        self._closed = True

    def _discard(self) -> None:
        if self._handle is not None:
            try:
                self._handle.close()
            except Exception:
                pass
            self._handle = None
        if self._tmp_name is not None:
            try:
                os.unlink(self._tmp_name)
            except FileNotFoundError:
                pass
            self._tmp_name = None
        self._closed = True

    def __exit__(self, exc_type, exc, tb) -> None:
        if exc_type is None:
            self.commit()
        else:
            self._discard()


def write_json_atomic(path: str | os.PathLike[str], payload: dict | str) -> None:
    """Write JSON atomically for reproducible reports."""
    target = Path(path)
    parent = target.parent
    if parent and not parent.exists():
        parent.mkdir(parents=True, exist_ok=True)
    text = payload if isinstance(payload, str) else json.dumps(payload, ensure_ascii=False, indent=2)
    fd, tmp_name = tempfile.mkstemp(prefix=f".{target.name}.", suffix=".tmp", dir=str(parent))
    try:
        with os.fdopen(fd, "w", encoding="utf-8") as handle:
            handle.write(text)
            handle.write("\n")
        os.replace(tmp_name, target)
    except Exception:
        try:
            os.unlink(tmp_name)
        except FileNotFoundError:
            pass
        raise
