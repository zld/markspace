"""Manifest loading, track validation and audio input reading."""
from __future__ import annotations

import json
import math
from dataclasses import dataclass
from pathlib import Path

import numpy as np
import soundfile as sf

from .curve import validate_points

MIN_SAMPLE_RATE = 8000
MAX_SAMPLE_RATE = 384000
SUPPORTED_CHANNELS = (1, 2)


class ManifestError(ValueError):
    """Raised when the mix manifest itself is invalid (exit code 2)."""


class InputError(RuntimeError):
    """Raised when a track's audio input cannot be used (exit code 2)."""

    def __init__(self, track_name, reason):
        self.track_name = track_name
        self.reason = reason
        super().__init__(f"track '{track_name}': {reason}")


class ProcessError(RuntimeError):
    """Raised when rendering fails (exit code 3)."""


class WriteError(RuntimeError):
    """Raised when the output file cannot be written (exit code 3)."""


@dataclass
class TrackSpec:
    name: str
    path: Path
    offset_seconds: float
    curve: list  # validated (time_seconds, db) points


@dataclass
class Manifest:
    sample_rate: int  # output sample rate
    tracks: list  # list[TrackSpec]


def _validate_sample_rate(value, where):
    if isinstance(value, bool) or not isinstance(value, int):
        raise ManifestError(f"{where}: sample_rate must be an integer, got {value!r}")
    if not (MIN_SAMPLE_RATE <= value <= MAX_SAMPLE_RATE):
        raise ManifestError(
            f"{where}: sample_rate {value} outside "
            f"[{MIN_SAMPLE_RATE}, {MAX_SAMPLE_RATE}]"
        )
    return value


def load_manifest(path):
    """Parse and validate a JSON mix manifest."""
    path = Path(path)
    try:
        raw = json.loads(path.read_text())
    except OSError as exc:
        raise ManifestError(f"cannot read manifest '{path}': {exc}") from exc
    except json.JSONDecodeError as exc:
        raise ManifestError(f"manifest '{path}' is not valid JSON: {exc}") from exc
    if not isinstance(raw, dict):
        raise ManifestError(f"manifest '{path}': top level must be an object")

    sample_rate = _validate_sample_rate(raw.get("sample_rate"), f"manifest '{path}'")

    entries = raw.get("tracks")
    if not isinstance(entries, list) or not entries:
        raise ManifestError(f"manifest '{path}': 'tracks' must be a non-empty list")

    tracks = []
    seen_names = set()
    for index, entry in enumerate(entries):
        where = f"manifest '{path}' track #{index}"
        if not isinstance(entry, dict):
            raise ManifestError(f"{where}: entry must be an object")
        name = entry.get("name") or f"track{index}"
        if not isinstance(name, str) or name in seen_names:
            raise ManifestError(f"{where}: missing or duplicate track name {name!r}")
        seen_names.add(name)

        file_value = entry.get("path")
        if not isinstance(file_value, str) or not file_value:
            raise ManifestError(f"{where} ('{name}'): 'path' must be a file path string")
        track_path = Path(file_value)
        if not track_path.is_absolute():
            track_path = path.parent / track_path

        offset = entry.get("offset_seconds", 0.0)
        try:
            offset = float(offset)
        except (TypeError, ValueError) as exc:
            raise ManifestError(
                f"{where} ('{name}'): offset_seconds must be a number"
            ) from exc
        if not math.isfinite(offset) or offset < 0.0:
            raise ManifestError(f"{where} ('{name}'): invalid offset_seconds {offset!r}")

        curve = validate_points(entry.get("curve", [[0.0, 0.0]]), name)
        tracks.append(TrackSpec(name=name, path=track_path,
                                offset_seconds=offset, curve=curve))
    return Manifest(sample_rate=sample_rate, tracks=tracks)


def load_track_audio(spec):
    """Read one track's audio file, returning (float64 frames, sample_rate).

    Corrupt files, illegal sample rates and unsupported channel counts are
    reported as InputError carrying the track name and the reason.
    """
    if not spec.path.is_file():
        raise InputError(spec.name, f"file not found: '{spec.path}'")
    try:
        data, rate = sf.read(str(spec.path), dtype="float64", always_2d=True)
    except Exception as exc:  # soundfile raises SoundFileError/RuntimeError
        raise InputError(spec.name, f"cannot decode '{spec.path}': {exc}") from exc
    if not (MIN_SAMPLE_RATE <= rate <= MAX_SAMPLE_RATE):
        raise InputError(spec.name, f"unsupported sample rate {rate}")
    channels = data.shape[1]
    if channels not in SUPPORTED_CHANNELS:
        raise InputError(spec.name, f"unsupported channel count {channels}")
    if data.shape[0] == 0:
        raise InputError(spec.name, "file contains no audio frames")
    if not np.all(np.isfinite(data)):
        raise InputError(spec.name, "file contains non-finite samples")
    return data, int(rate)
