"""Manifest validation and local WAV ingestion.

A manifest describes the master timeline::

    {
      "target_rate": 48000,
      "total_frames": 96000,
      "channels": 2,
      "tracks": [
        {
          "id": "dialog",
          "file": "dialog.wav",
          "start_frame": 0,
          "channel_matrix": [[1, 0], [0, 1]],
          "automation": [{"frame": 0, "gain_db": 0.0}]
        }
      ]
    }

Every validation error names the track id (where applicable) and the
offending field.  Schema problems raise :class:`ManifestError` (exit 2);
a manifest that parses but whose WAV cannot be decoded raises
:class:`AudioError` (exit 3).
"""

from __future__ import annotations

import json
import math
from dataclasses import dataclass
from pathlib import Path

import numpy as np
import soundfile as sf

from .automation import GainCurve
from .errors import AudioError, ManifestError

MAX_FRAMES = 200_000_000
"""Hard guard so a malformed huge length cannot drive unbounded memory."""
MAX_CHANNELS = 16
"""Hard guard on channel counts for sources and the master bus."""


@dataclass(frozen=True)
class Track:
    id: str
    file: Path
    start_frame: int
    source_frames: int
    source_rate: int
    source_channels: int
    matrix: np.ndarray  # (source_channels, bus_channels)
    curve: GainCurve
    samples: np.ndarray  # (source_frames, source_channels), float64


@dataclass(frozen=True)
class Manifest:
    target_rate: int
    total_frames: int
    channels: int
    tracks: tuple[Track, ...]
    root: Path


# ---------------------------------------------------------------------- #
# Entry point
# ---------------------------------------------------------------------- #
def load_manifest(path: str | Path) -> Manifest:
    path = Path(path)
    try:
        raw = json.loads(path.read_text(encoding="utf-8"))
    except FileNotFoundError as exc:
        raise ManifestError(f"manifest not found: {path}") from exc
    except json.JSONDecodeError as exc:
        raise ManifestError(f"manifest {path} is not valid JSON: {exc}") from exc
    if not isinstance(raw, dict):
        raise ManifestError("manifest root must be a JSON object")
    root = path.parent

    target_rate = _positive_int(raw, "target_rate", limit=1_000_000)
    total_frames = _positive_int(raw, "total_frames", limit=MAX_FRAMES)
    channels = _positive_int(raw, "channels", limit=MAX_CHANNELS)
    tracks_raw = raw.get("tracks")
    if not isinstance(tracks_raw, list) or not tracks_raw:
        raise ManifestError("field 'tracks' must be a non-empty list")

    seen_ids: set[str] = set()
    tracks: list[Track] = []
    for entry in tracks_raw:
        tracks.append(
            _parse_track(entry, seen_ids, root, target_rate, total_frames, channels)
        )
    return Manifest(target_rate, total_frames, channels, tuple(tracks), root)


# ---------------------------------------------------------------------- #
# Track parsing
# ---------------------------------------------------------------------- #
def _parse_track(
    entry: object,
    seen_ids: set[str],
    root: Path,
    target_rate: int,
    total_frames: int,
    bus_channels: int,
) -> Track:
    if not isinstance(entry, dict):
        raise ManifestError("each track entry must be a JSON object")
    track_id = _required_str(entry, "id")
    if track_id in seen_ids:
        raise ManifestError(f"duplicate track id {track_id!r}")
    seen_ids.add(track_id)
    prefix = f"track {track_id!r}"

    file_value = _required_str(entry, "file", prefix)
    wav_path = (root / file_value).resolve()
    if not wav_path.is_file():
        raise ManifestError(f"{prefix} references missing file: {file_value}")

    start_frame = _non_negative_int(entry, "start_frame", total_frames, prefix)
    samples, source_rate, source_channels = _read_wav(wav_path, track_id)
    source_frames = samples.shape[0]
    if source_frames == 0:
        raise AudioError(f"{prefix} file {file_value} contains zero frames")

    end_frame = start_frame + int(
        np.ceil(source_frames * target_rate / source_rate)
    )
    if end_frame > total_frames:
        raise ManifestError(
            f"{prefix} extends past the timeline: resampled end frame "
            f"{end_frame} > total_frames {total_frames}"
        )

    matrix = _parse_matrix(
        entry.get("channel_matrix"), source_channels, bus_channels, track_id
    )
    curve = GainCurve.from_manifest(
        entry.get("automation"), total_frames, track_id
    )
    return Track(
        id=track_id,
        file=wav_path,
        start_frame=start_frame,
        source_frames=source_frames,
        source_rate=source_rate,
        source_channels=source_channels,
        matrix=matrix,
        curve=curve,
        samples=samples,
    )


def _read_wav(
    wav_path: Path, track_id: str
) -> tuple[np.ndarray, int, int]:
    try:
        data, source_rate = sf.read(str(wav_path), dtype="float64", always_2d=True)
    except (sf.LibsndfileError, RuntimeError, ValueError) as exc:
        raise AudioError(
            f"track {track_id!r} WAV could not be decoded: {wav_path.name}: {exc}"
        ) from exc
    if not np.all(np.isfinite(data)):
        raise AudioError(
            f"track {track_id!r} WAV contains NaN or infinite samples"
        )
    if data.shape[0] > MAX_FRAMES:
        raise AudioError(
            f"track {track_id!r} exceeds the {MAX_FRAMES} frame safety limit"
        )
    if data.shape[1] > MAX_CHANNELS:
        raise AudioError(
            f"track {track_id!r} exceeds the {MAX_CHANNELS} channel safety limit"
        )
    return data, int(source_rate), int(data.shape[1])


def _parse_matrix(
    raw: object, source_channels: int, bus_channels: int, track_id: str
) -> np.ndarray:
    prefix = f"track {track_id!r} field 'channel_matrix'"
    if source_channels == bus_channels and raw is None:
        return np.eye(source_channels, dtype=np.float64)
    if raw is None:
        raise ManifestError(
            f"{prefix} is required because source has {source_channels} "
            f"channel(s) but the bus has {bus_channels}; implicit mapping "
            f"or truncation is not allowed"
        )
    if not isinstance(raw, list) or not raw or any(
        not isinstance(row, list) for row in raw
    ):
        raise ManifestError(f"{prefix} must be a list of numeric lists")
    rows = len(raw)
    cols = len(raw[0])
    if any(len(row) != cols for row in raw):
        raise ManifestError(f"{prefix} has ragged rows")
    if rows != source_channels or cols != bus_channels:
        raise ManifestError(
            f"{prefix} shape must be ({source_channels}, {bus_channels}) "
            f"for {source_channels} source channel(s) and {bus_channels} "
            f"bus channel(s); got ({rows}, {cols})"
        )
    flat = [value for row in raw for value in row]
    for value in flat:
        if isinstance(value, bool) or not isinstance(value, (int, float)):
            raise ManifestError(f"{prefix} coefficients must be numbers")
        if not math.isfinite(float(value)):
            raise ManifestError(f"{prefix} coefficients must be finite")
    return np.asarray(raw, dtype=np.float64)


# ---------------------------------------------------------------------- #
# Scalar field helpers
# ---------------------------------------------------------------------- #
def _required_str(entry: dict, field: str, prefix: str = "manifest") -> str:
    value = entry.get(field)
    if not isinstance(value, str) or not value:
        raise ManifestError(f"{prefix} field {field!r} must be a non-empty string")
    return value


def _positive_int(raw: dict, field: str, limit: int) -> int:
    value = raw.get(field)
    if isinstance(value, bool) or not isinstance(value, int) or value <= 0:
        raise ManifestError(f"field {field!r} must be a positive integer")
    if value > limit:
        raise ManifestError(f"field {field!r}={value} exceeds safety limit {limit}")
    return value


def _non_negative_int(
    entry: dict, field: str, total_frames: int, prefix: str
) -> int:
    value = entry.get(field)
    if isinstance(value, bool) or not isinstance(value, int) or value < 0:
        raise ManifestError(f"{prefix} field {field!r} must be a non-negative integer")
    if value > total_frames:
        raise ManifestError(
            f"{prefix} field {field!r}={value} is outside the timeline "
            f"[0, {total_frames}]"
        )
    return value
