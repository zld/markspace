"""Session model: manifest parsing and validation.

A session manifest is JSON:

```
{
  "channels": 2,                     # project channel layout (1 or 2)
  "sample_rate": 48000,              # project sample rate
  "frames": 60000,                   # optional session length in frames
  "tracks": [
    {
      "name": "dialog",
      "path": "dialog.wav",          # relative to the manifest directory
      "start_frame": 0,              # placement in project frames
      "gain_db": 0.0,                # static track gain
      "pan": -0.25,                  # -1.0 left .. +1.0 right
      "channel_map": [0, 1],         # source channel -> project channel
      "loop": {"start_frame": 10, "end_frame": 500},
      "automation": [{"frame": 0, "value": 0.0, "unit": "db"}]
    }
  ]
}
```

All frame indices are project-rate samples. ``loop`` is an optional half-open
range of *source* frames that repeats until the session ends. ``start_frame``
is the project frame where the track begins. When ``frames`` is absent the
session length is the latest natural track end, measured in project frames.

Paths must stay relative to the manifest directory: absolute paths and ``..``
escapes are rejected so a manifest cannot pull audio from outside its session
folder. Paths are stored but not opened here; loading and format errors are
reported by the audio layer with the same track name.
"""

from __future__ import annotations

import math
from dataclasses import dataclass
from pathlib import Path
from typing import Any, Mapping, Sequence

from .automation import AutomationCurve, default_curve, parse_automation
from .errors import ManifestError, TrackError

SUPPORTED_CHANNELS = (1, 2)
DEFAULT_SAMPLE_RATE = 48000


@dataclass(frozen=True)
class LoopRegion:
    """A half-open source-frame range that repeats during playback."""

    start_frame: int
    end_frame: int

    @property
    def length(self) -> int:
        return self.end_frame - self.start_frame


@dataclass(frozen=True)
class TrackSpec:
    """A fully validated track definition before audio is loaded."""

    name: str
    path: Path
    start_frame: int = 0
    gain_db: float = 0.0
    pan: float = 0.0
    channel_map: tuple[int, ...] = ()
    loop: LoopRegion | None = None
    automation: AutomationCurve = default_curve()


@dataclass(frozen=True)
class Session:
    """A validated manifest ready for rendering."""

    channels: int
    sample_rate: int
    tracks: tuple[TrackSpec, ...]
    source_path: Path
    frames: int | None = None


def _require_mapping(value: Any, what: str) -> Mapping[str, Any]:
    if not isinstance(value, Mapping):
        raise ManifestError(f"{what} must be a JSON object")
    return value


def _require_int(value: Any, what: str, minimum: int | None = None) -> int:
    if isinstance(value, bool) or not isinstance(value, int):
        raise ManifestError(f"{what} must be an integer, got {value!r}")
    if minimum is not None and value < minimum:
        raise ManifestError(f"{what} must be >= {minimum}, got {value}")
    return value


def _require_number(value: Any, what: str) -> float:
    if isinstance(value, bool) or not isinstance(value, (int, float)):
        raise ManifestError(f"{what} must be a number, got {value!r}")
    numeric = float(value)
    if not math.isfinite(numeric):
        raise ManifestError(f"{what} must be finite, got {value!r}")
    return numeric


def _resolve_path(raw: Any, base: Path, track: str) -> Path:
    if not isinstance(raw, str) or not raw.strip():
        raise TrackError(track, "path must be a non-empty string")
    candidate = Path(raw)
    if candidate.is_absolute():
        raise TrackError(track, f"path {raw!r} must be relative to the manifest directory")
    resolved = (base / candidate).resolve()
    try:
        resolved.relative_to(base.resolve())
    except ValueError:
        raise TrackError(track, f"path {raw!r} escapes the manifest directory") from None
    if not resolved.is_file():
        raise TrackError(track, f"audio file not found: {raw}")
    return resolved


def _parse_loop(raw: Any, track: str) -> LoopRegion | None:
    if raw is None:
        return None
    data = _require_mapping(raw, f"track '{track}' loop")
    start = _require_int(data.get("start_frame"), f"track '{track}' loop.start_frame", 0)
    end = _require_int(data.get("end_frame"), f"track '{track}' loop.end_frame", 0)
    if end <= start:
        raise TrackError(track, f"loop end_frame {end} must be greater than start_frame {start}")
    return LoopRegion(start, end)


def _parse_channel_map(raw: Any, track: str, project_channels: int) -> tuple[int, ...]:
    if raw is None:
        return ()
    if isinstance(raw, (str, bytes)) or not isinstance(raw, Sequence):
        raise TrackError(track, "channel_map must be an array of channel indices")
    mapping: list[int] = []
    for index, item in enumerate(raw):
        if isinstance(item, bool) or not isinstance(item, int):
            raise TrackError(track, f"channel_map[{index}] must be an integer, got {item!r}")
        if item < 0 or item >= project_channels:
            raise TrackError(
                track,
                f"channel_map[{index}]={item} is outside project layout of "
                f"{project_channels} channel(s)",
            )
        mapping.append(item)
    if not mapping:
        raise TrackError(track, "channel_map must map at least one channel")
    return tuple(mapping)


def parse_track(raw: Any, base: Path, project_channels: int) -> TrackSpec:
    """Validate one manifest track entry."""
    data = _require_mapping(raw, "track entry")
    name = data.get("name")
    if not isinstance(name, str) or not name.strip():
        raise ManifestError("every track needs a non-empty string 'name'")
    path = _resolve_path(data.get("path"), base, name)
    start = _require_int(data.get("start_frame", 0), f"track '{name}' start_frame", 0)
    gain_db = _require_number(data.get("gain_db", 0.0), f"track '{name}' gain_db")
    pan = _require_number(data.get("pan", 0.0), f"track '{name}' pan")
    if not -1.0 <= pan <= 1.0:
        raise TrackError(name, f"pan {pan} must be within [-1.0, 1.0]")
    channel_map = _parse_channel_map(data.get("channel_map"), name, project_channels)
    loop = _parse_loop(data.get("loop"), name)
    automation = parse_automation(data.get("automation"), name)
    return TrackSpec(
        name=name,
        path=path,
        start_frame=start,
        gain_db=gain_db,
        pan=pan,
        channel_map=channel_map,
        loop=loop,
        automation=automation,
    )


def parse_manifest(data: Any, manifest_path: Path) -> Session:
    """Validate a decoded manifest and resolve it against its own directory."""
    root = _require_mapping(data, "manifest")
    channels = _require_int(root.get("channels", 2), "manifest channels")
    if channels not in SUPPORTED_CHANNELS:
        raise ManifestError(
            f"manifest channels must be one of {SUPPORTED_CHANNELS}, got {channels}"
        )
    sample_rate = _require_int(
        root.get("sample_rate", DEFAULT_SAMPLE_RATE), "manifest sample_rate", 1
    )
    frames_raw = root.get("frames")
    frames = None if frames_raw is None else _require_int(frames_raw, "manifest frames", 0)
    tracks_raw = root.get("tracks")
    if isinstance(tracks_raw, (str, bytes)) or not isinstance(tracks_raw, Sequence):
        raise ManifestError("manifest 'tracks' must be an array")
    if not tracks_raw:
        raise ManifestError("manifest must contain at least one track")
    base = manifest_path.resolve().parent
    tracks: list[TrackSpec] = []
    seen: set[str] = set()
    for entry in tracks_raw:
        track = parse_track(entry, base, channels)
        if track.name in seen:
            raise ManifestError(f"duplicate track name {track.name!r}")
        seen.add(track.name)
        tracks.append(track)
    return Session(
        channels=channels,
        sample_rate=sample_rate,
        tracks=tuple(tracks),
        source_path=manifest_path.resolve(),
        frames=frames,
    )
