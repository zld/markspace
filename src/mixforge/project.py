"""Project manifest loading and validation.

The manifest is a JSON document describing the mix timeline.  Every rejected
value raises :class:`ManifestError` with a dotted ``field`` path so the caller
can report exactly which entry was wrong.
"""

from __future__ import annotations

import json
import math
from dataclasses import dataclass, field
from pathlib import Path

from .errors import AssetError, LimitError, ManifestError

# Hard safety limits.  They keep a malformed manifest from allocating
# unbounded memory and are reported with explicit messages.
MAX_PROJECT_RATE = 384_000
MIN_PROJECT_RATE = 8_000
MAX_CHANNELS = 64
MAX_TRACKS = 64
MAX_FRAMES = 2 * 3600 * MAX_PROJECT_RATE  # two hours at the highest rate
MAX_BLOCK_FRAMES = 1 << 20
MIN_BLOCK_FRAMES = 64
VALID_CLIP_POLICIES = ("preserve", "hard_limit", "error")
VALID_INTERP = ("linear", "hold")
VALID_LAYOUT = ("mono_copy", "stereo", "matrix")


@dataclass
class AutomationPoint:
    """One automation keyframe, gain in dB relative to the track gain."""

    time: float
    gain_db: float
    interpolation: str = "linear"


@dataclass
class MuteSegment:
    """Half-open interval ``[start, end)`` in seconds where the track is off."""

    start: float
    end: float


@dataclass
class ChannelLayout:
    """Channel conversion from the source file into the project bus."""

    mode: str
    matrix: list[list[float]] | None = None


@dataclass
class TrackSpec:
    name: str
    file: Path
    start_frame: int
    gain_db: float
    layout: ChannelLayout
    automation: list[AutomationPoint] = field(default_factory=list)
    mutes: list[MuteSegment] = field(default_factory=list)


@dataclass
class Project:
    root: Path
    project_rate: int
    frames: int
    channels: int
    clip_policy: str
    clip_threshold: float
    automation_interval: float
    output: Path
    tracks: list[TrackSpec]


def _is_finite_number(value: object) -> bool:
    return isinstance(value, (int, float)) and not isinstance(value, bool) and math.isfinite(float(value))


def _require_number(value: object, field: str) -> float:
    if not _is_finite_number(value):
        raise ManifestError("expected a finite number", field)
    return float(value)


def _require_int(value: object, field: str, *, minimum: int | None = None,
                 maximum: int | None = None) -> int:
    if not isinstance(value, int) or isinstance(value, bool):
        raise ManifestError("expected an integer", field)
    if minimum is not None and value < minimum:
        raise ManifestError(f"must be >= {minimum}", field)
    if maximum is not None and value > maximum:
        raise ManifestError(f"must be <= {maximum}", field)
    return value


def _parse_layout(raw: object, field: str) -> ChannelLayout:
    if not isinstance(raw, dict):
        raise ManifestError("expected an object with 'mode'", field)
    mode = raw.get("mode")
    if mode not in VALID_LAYOUT:
        raise ManifestError(f"mode must be one of {VALID_LAYOUT}", f"{field}.mode")
    matrix = None
    if mode == "matrix":
        raw_matrix = raw.get("matrix")
        if not isinstance(raw_matrix, list) or not raw_matrix:
            raise ManifestError("matrix mode requires a non-empty matrix", f"{field}.matrix")
        matrix = []
        for row_index, row in enumerate(raw_matrix):
            if not isinstance(row, list) or not row:
                raise ManifestError("each matrix row must be a non-empty list",
                                    f"{field}.matrix[{row_index}]")
            parsed_row = [_require_number(v, f"{field}.matrix[{row_index}][{col}]")
                          for col, v in enumerate(row)]
            matrix.append(parsed_row)
    return ChannelLayout(mode=mode, matrix=matrix)


def _parse_track(raw: object, index: int, project_dir: Path) -> TrackSpec:
    base = f"tracks[{index}]"
    if not isinstance(raw, dict):
        raise ManifestError("track must be an object", base)
    name = raw.get("name")
    if not isinstance(name, str) or not name.strip():
        raise ManifestError("must be a non-empty string", f"{base}.name")
    file_name = raw.get("file")
    if not isinstance(file_name, str) or not file_name.strip():
        raise ManifestError("must be a non-empty string", f"{base}.file")
    file_path = (project_dir / file_name).resolve()
    start_frame = _require_int(raw.get("start_frame", 0), f"{base}.start_frame", minimum=0)
    gain_db = _require_number(raw.get("gain_db", 0.0), f"{base}.gain_db")
    layout = _parse_layout(raw.get("layout", {"mode": "stereo"}), f"{base}.layout")

    points: list[AutomationPoint] = []
    raw_points = raw.get("automation", [])
    if not isinstance(raw_points, list):
        raise ManifestError("automation must be a list", f"{base}.automation")
    last_time: float | None = None
    for p_index, point in enumerate(raw_points):
        pfield = f"{base}.automation[{p_index}]"
        if not isinstance(point, dict):
            raise ManifestError("automation point must be an object", pfield)
        time = _require_number(point.get("time"), f"{pfield}.time")
        if time < 0.0:
            raise ManifestError("time must be >= 0", f"{pfield}.time")
        gain = _require_number(point.get("gain_db"), f"{pfield}.gain_db")
        interp = point.get("interpolation", "linear")
        if interp not in VALID_INTERP:
            raise ManifestError(f"interpolation must be one of {VALID_INTERP}",
                                f"{pfield}.interpolation")
        if last_time is not None and time <= last_time:
            raise ManifestError("points must be sorted with unique times",
                                f"{pfield}.time")
        last_time = time
        points.append(AutomationPoint(time, gain, interp))

    mutes: list[MuteSegment] = []
    raw_mutes = raw.get("mutes", [])
    if not isinstance(raw_mutes, list):
        raise ManifestError("mutes must be a list", f"{base}.mutes")
    for m_index, segment in enumerate(raw_mutes):
        mfield = f"{base}.mutes[{m_index}]"
        if not isinstance(segment, dict):
            raise ManifestError("mute segment must be an object", mfield)
        start = _require_number(segment.get("start"), f"{mfield}.start")
        end = _require_number(segment.get("end"), f"{mfield}.end")
        if start < 0.0 or end <= start:
            raise ManifestError("require 0 <= start < end", mfield)
        mutes.append(MuteSegment(start, end))

    return TrackSpec(name=name, file=file_path, start_frame=start_frame,
                     gain_db=gain_db, layout=layout, automation=points, mutes=mutes)


def load_project(path: str | Path) -> Project:
    """Read and validate a manifest; all paths resolve relative to it."""
    manifest_path = Path(path).resolve()
    try:
        raw = json.loads(manifest_path.read_text(encoding="utf-8"))
    except FileNotFoundError as exc:
        raise AssetError(f"project manifest not found: {manifest_path}") from exc
    except (OSError, json.JSONDecodeError) as exc:
        raise ManifestError(f"cannot parse manifest: {exc}") from exc
    if not isinstance(raw, dict):
        raise ManifestError("top-level document must be an object", "<root>")

    rate = _require_int(raw.get("project_rate"), "project_rate",
                        minimum=MIN_PROJECT_RATE, maximum=MAX_PROJECT_RATE)
    frames = _require_int(raw.get("frames"), "frames", minimum=0, maximum=MAX_FRAMES)
    channels = _require_int(raw.get("channels", 2), "channels", minimum=1, maximum=MAX_CHANNELS)
    policy = raw.get("clip_policy", "preserve")
    if policy not in VALID_CLIP_POLICIES:
        raise ManifestError(f"clip_policy must be one of {VALID_CLIP_POLICIES}", "clip_policy")
    threshold = _require_number(raw.get("clip_threshold", 1.0), "clip_threshold")
    if threshold <= 0.0:
        raise ManifestError("clip_threshold must be > 0", "clip_threshold")
    interval = _require_number(raw.get("automation_interval", 0.1),
                               "automation_interval")
    if interval <= 0.0:
        raise ManifestError("automation_interval must be > 0", "automation_interval")

    raw_tracks = raw.get("tracks")
    if not isinstance(raw_tracks, list) or not raw_tracks:
        raise ManifestError("tracks must be a non-empty list", "tracks")
    if len(raw_tracks) > MAX_TRACKS:
        raise LimitError(f"track count {len(raw_tracks)} exceeds limit {MAX_TRACKS}", "tracks")

    tracks = [_parse_track(item, index, manifest_path.parent)
              for index, item in enumerate(raw_tracks)]
    seen: set[str] = set()
    for track in tracks:
        if track.name in seen:
            raise ManifestError(f"duplicate track name {track.name!r}", "tracks")
        seen.add(track.name)
        if track.start_frame > frames:
            raise ManifestError("start_frame beyond project frames",
                                f"track {track.name}.start_frame")

    output_name = raw.get("output", "mix.wav")
    if not isinstance(output_name, str) or not output_name.strip():
        raise ManifestError("output must be a non-empty string", "output")
    output_path = (manifest_path.parent / output_name).resolve()

    return Project(root=manifest_path.parent, project_rate=rate, frames=frames,
                   channels=channels, clip_policy=policy, clip_threshold=threshold,
                   automation_interval=interval, output=output_path, tracks=tracks)


def validate_block_frames(block_frames: int) -> int:
    """CLI-level check of the processing block size."""
    if not isinstance(block_frames, int) or isinstance(block_frames, bool):
        raise ManifestError("block-frames must be an integer", "block_frames")
    if not MIN_BLOCK_FRAMES <= block_frames <= MAX_BLOCK_FRAMES:
        raise LimitError(
            f"block-frames must be within [{MIN_BLOCK_FRAMES}, {MAX_BLOCK_FRAMES}]",
            "block_frames")
    return block_frames
