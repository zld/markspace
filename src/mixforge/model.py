"""Session model: tracks, automation nodes and mix configuration.

Time values in manifests are seconds (floats); everything here is validated
eagerly so the render stage can assume well-formed input.
"""

from __future__ import annotations

import json
import math
from dataclasses import dataclass, field
from pathlib import Path

import soundfile as sf

MAX_TRACKS = 64
MIN_CHANNELS = 1
MAX_IN_CHANNELS = 8
MAX_OUT_CHANNELS = 2
INTERPOLATIONS = ("linear", "hold")


class ModelError(ValueError):
    """Raised for invalid manifest content or unusable input files."""


@dataclass(frozen=True)
class AutomationNode:
    """One automation breakpoint, time in seconds."""

    time: float
    gain: float


@dataclass(frozen=True)
class AutomationCurve:
    nodes: tuple[AutomationNode, ...]
    interpolation: str  # "linear" or "hold"

    def is_identity(self) -> bool:
        return all(node.gain == 1.0 for node in self.nodes)


@dataclass(frozen=True)
class TrackSpec:
    name: str
    path: Path
    offset: float
    gain: float
    channel_map: tuple[tuple[int, int], ...]
    sample_rate: int
    in_channels: int
    frames: int
    automation: AutomationCurve | None = None


@dataclass(frozen=True)
class MixConfig:
    name: str
    sample_rate: int
    channels: int
    block_size: int
    limiter: bool
    tracks: tuple[TrackSpec, ...] = field(default_factory=tuple)


def _is_finite_number(value, *, allow_bool: bool = False) -> bool:
    if isinstance(value, bool) and not allow_bool:
        return False
    return isinstance(value, (int, float)) and math.isfinite(float(value))


def _require(obj: dict, key: str, context: str):
    if key not in obj:
        raise ModelError(f"{context}: missing required field '{key}'")
    return obj[key]


def _parse_automation(raw, context: str) -> AutomationCurve:
    if not isinstance(raw, dict):
        raise ModelError(f"{context}: automation must be an object")
    interpolation = raw.get("interpolation", "linear")
    if interpolation not in INTERPOLATIONS:
        raise ModelError(
            f"{context}: interpolation must be one of {INTERPOLATIONS}"
        )
    raw_nodes = raw.get("nodes", [])
    if not isinstance(raw_nodes, list) or not raw_nodes:
        raise ModelError(f"{context}: automation needs at least one node")
    nodes: list[AutomationNode] = []
    previous_time: float | None = None
    for index, raw_node in enumerate(raw_nodes):
        node_ctx = f"{context}.nodes[{index}]"
        if not isinstance(raw_node, dict):
            raise ModelError(f"{node_ctx}: node must be an object")
        time_value = _require(raw_node, "time", node_ctx)
        gain_value = _require(raw_node, "gain", node_ctx)
        if not _is_finite_number(time_value):
            raise ModelError(f"{node_ctx}: time must be a finite number")
        if not _is_finite_number(gain_value):
            raise ModelError(f"{node_ctx}: gain must be a finite number")
        time_value = float(time_value)
        gain_value = float(gain_value)
        if gain_value < 0.0:
            raise ModelError(f"{node_ctx}: gain must not be negative")
        if previous_time is not None and time_value <= previous_time:
            raise ModelError(f"{node_ctx}: node times must be strictly increasing")
        previous_time = time_value
        nodes.append(AutomationNode(time_value, gain_value))
    return AutomationCurve(tuple(nodes), interpolation)


def _probe_wav(path: Path, context: str) -> tuple[int, int, int]:
    if not path.exists():
        raise ModelError(f"{context}: track file not found: {path}")
    if path.suffix.lower() != ".wav":
        raise ModelError(f"{context}: only uncompressed WAV files are supported")
    try:
        info = sf.info(str(path))
    except Exception as exc:  # libsndfile raises various subtypes
        raise ModelError(f"{context}: cannot read WAV file: {exc}") from exc
    if info.format != "WAV":
        raise ModelError(f"{context}: not a WAV container: {path}")
    if not (MIN_CHANNELS <= info.channels <= MAX_IN_CHANNELS):
        raise ModelError(
            f"{context}: input channels must be in [1, {MAX_IN_CHANNELS}]"
        )
    if info.samplerate <= 0 or info.frames < 0:
        raise ModelError(f"{context}: invalid WAV header")
    return info.samplerate, info.channels, int(info.frames)


def _parse_channel_map(raw, in_channels: int, out_channels: int, context: str):
    if not isinstance(raw, list) or not raw:
        raise ModelError(f"{context}: channel_map must be a non-empty list")
    mapping: list[tuple[int, int]] = []
    seen: set[tuple[int, int]] = set()
    for index, pair in enumerate(raw):
        pair_ctx = f"{context}.channel_map[{index}]"
        if not isinstance(pair, list) or len(pair) != 2:
            raise ModelError(f"{pair_ctx}: each mapping must be [src, dst]")
        src, dst = pair
        if not isinstance(src, int) or isinstance(src, bool):
            raise ModelError(f"{pair_ctx}: source channel must be an integer")
        if not isinstance(dst, int) or isinstance(dst, bool):
            raise ModelError(f"{pair_ctx}: destination channel must be an integer")
        if not (0 <= src < in_channels):
            raise ModelError(f"{pair_ctx}: source channel {src} out of range")
        if not (0 <= dst < out_channels):
            raise ModelError(f"{pair_ctx}: destination channel {dst} out of range")
        if (src, dst) in seen:
            raise ModelError(f"{pair_ctx}: duplicate channel mapping")
        seen.add((src, dst))
        mapping.append((src, dst))
    return tuple(mapping)


def _parse_track(raw, index: int, out_channels: int, base_dir: Path) -> TrackSpec:
    context = f"tracks[{index}]"
    if not isinstance(raw, dict):
        raise ModelError(f"{context}: track must be an object")
    name = raw.get("name", f"track{index}")
    if not isinstance(name, str) or not name:
        raise ModelError(f"{context}: name must be a non-empty string")
    raw_path = _require(raw, "path", context)
    if not isinstance(raw_path, str) or not raw_path:
        raise ModelError(f"{context}: path must be a non-empty string")
    path = (base_dir / raw_path).resolve()
    sample_rate, in_channels, frames = _probe_wav(path, context)
    offset = raw.get("offset", 0.0)
    if not _is_finite_number(offset) or offset < 0.0:
        raise ModelError(f"{context}: offset must be a finite, non-negative number")
    gain = raw.get("gain", 1.0)
    if not _is_finite_number(gain) or gain < 0.0:
        raise ModelError(f"{context}: gain must be a finite, non-negative number")
    default_map = [[0, 0]]
    if in_channels == 1 and out_channels == 2:
        default_map = [[0, 0], [0, 1]]
    channel_map = _parse_channel_map(
        raw.get("channel_map", default_map), in_channels, out_channels, context
    )
    automation = None
    if "automation" in raw:
        automation = _parse_automation(raw["automation"], f"{context}.automation")
    return TrackSpec(
        name=name,
        path=path,
        offset=float(offset),
        gain=float(gain),
        channel_map=channel_map,
        sample_rate=sample_rate,
        in_channels=in_channels,
        frames=frames,
        automation=automation,
    )


def load_manifest(path: str | Path) -> MixConfig:
    """Load and validate a mix-session manifest from a JSON file."""

    manifest_path = Path(path)
    if not manifest_path.exists():
        raise ModelError(f"manifest not found: {manifest_path}")
    try:
        raw = json.loads(manifest_path.read_text(encoding="utf-8"))
    except json.JSONDecodeError as exc:
        raise ModelError(f"invalid JSON in manifest: {exc}") from exc
    if not isinstance(raw, dict):
        raise ModelError("manifest root must be an object")
    sample_rate = _require(raw, "sample_rate", "manifest")
    if not isinstance(sample_rate, int) or isinstance(sample_rate, bool) or sample_rate <= 0:
        raise ModelError("manifest: sample_rate must be a positive integer")
    channels = raw.get("channels", 2)
    if not isinstance(channels, int) or isinstance(channels, bool):
        raise ModelError("manifest: channels must be an integer")
    if not (MIN_CHANNELS <= channels <= MAX_OUT_CHANNELS):
        raise ModelError(f"manifest: output channels must be in [1, {MAX_OUT_CHANNELS}]")
    block_size = raw.get("block_size", 1024)
    if not isinstance(block_size, int) or isinstance(block_size, bool) or block_size <= 0:
        raise ModelError("manifest: block_size must be a positive integer")
    limiter = raw.get("limiter", False)
    if not isinstance(limiter, bool):
        raise ModelError("manifest: limiter must be a boolean")
    raw_tracks = raw.get("tracks", [])
    if not isinstance(raw_tracks, list):
        raise ModelError("manifest: tracks must be a list")
    if len(raw_tracks) > MAX_TRACKS:
        raise ModelError(f"manifest: at most {MAX_TRACKS} tracks are supported")
    base_dir = manifest_path.parent.resolve()
    tracks = tuple(
        _parse_track(raw_track, index, channels, base_dir)
        for index, raw_track in enumerate(raw_tracks)
    )
    names = [track.name for track in tracks]
    if len(set(names)) != len(names):
        raise ModelError("manifest: track names must be unique")
    return MixConfig(
        name=raw.get("name", manifest_path.stem),
        sample_rate=sample_rate,
        channels=channels,
        block_size=block_size,
        limiter=limiter,
        tracks=tracks,
    )
