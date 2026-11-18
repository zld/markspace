"""Manifest parsing and validation.

The manifest is JSON. Every validation failure names the offending field and,
when applicable, the track. Nothing here touches audio files: media checks
belong to the render path so a broken file is reported as a media error.
"""

from __future__ import annotations

import json
from dataclasses import dataclass
from pathlib import Path
from typing import Any

from .automation import AutomationCurve, parse_automation
from .errors import ManifestError
from .fields import check_id, finite_number, positive_int

MAX_RATE = 384000
MAX_DURATION_MS = 24 * 60 * 60 * 1000
_CHANNEL_ALIASES = {"mono": 1, "stereo": 2}


@dataclass(frozen=True)
class Track:
    id: str
    path: Path
    offset_ms: float
    channel_count: int
    channel_matrix: tuple[tuple[float, ...], ...]
    gain_db: float
    bus: str
    automation: AutomationCurve | None


@dataclass(frozen=True)
class Bus:
    id: str
    gain_db: float
    automation: AutomationCurve | None


@dataclass(frozen=True)
class Session:
    manifest_path: Path
    root: Path
    tracks: tuple[Track, ...]
    buses: tuple[Bus, ...]
    rate: int | None
    duration_ms: float | None
    output_channels: int = 2

    def bus_by_id(self) -> dict[str, Bus]:
        return {bus.id: bus for bus in self.buses}


def load_manifest(path: str | Path) -> Session:
    manifest_path = Path(path)
    try:
        text = manifest_path.read_text(encoding="utf-8")
    except OSError as exc:
        raise ManifestError(f"cannot read manifest: {exc}", field="manifest") from exc
    try:
        data = json.loads(text)
    except json.JSONDecodeError as exc:
        raise ManifestError(
            f"invalid JSON at line {exc.lineno} column {exc.colno}: {exc.msg}",
            field="manifest",
        ) from exc
    if not isinstance(data, dict):
        raise ManifestError("manifest root must be a JSON object", field="manifest")
    return parse_session(data, manifest_path)


def parse_session(data: dict[str, Any], manifest_path: Path) -> Session:
    root = manifest_path.resolve().parent

    rate = data.get("output_rate")
    if rate is not None:
        rate = positive_int(rate, "output_rate")
        if rate > MAX_RATE:
            raise ManifestError(f"output_rate must be <= {MAX_RATE}",
                                field="output_rate")

    duration_ms = data.get("duration_ms")
    if duration_ms is not None:
        duration_ms = finite_number(duration_ms, "duration_ms")
        if duration_ms <= 0:
            raise ManifestError("duration_ms must be > 0", field="duration_ms")
        if duration_ms > MAX_DURATION_MS:
            raise ManifestError(f"duration_ms must be <= {MAX_DURATION_MS}",
                                field="duration_ms")

    buses = _parse_buses(data.get("buses"))
    bus_ids = {bus.id for bus in buses} | {"master"}
    tracks = _parse_tracks(data.get("tracks"), root, bus_ids)
    if not tracks:
        raise ManifestError("tracks must contain at least one track", field="tracks")

    return Session(
        manifest_path=manifest_path.resolve(),
        root=root,
        tracks=tuple(tracks),
        buses=tuple(buses),
        rate=rate,
        duration_ms=duration_ms,
    )


def _parse_buses(raw: Any) -> list[Bus]:
    if raw is None:
        return []
    if not isinstance(raw, dict):
        raise ManifestError("buses must be an object keyed by bus id", field="buses")
    buses: list[Bus] = []
    for bus_id, spec in raw.items():
        where = f"buses.{bus_id}"
        check_id(bus_id, where)
        if bus_id == "master":
            raise ManifestError("'master' is reserved and cannot be a bus id",
                                field=where)
        if not isinstance(spec, dict):
            raise ManifestError("bus spec must be an object", field=where)
        unknown = set(spec) - {"gain_db", "automation"}
        if unknown:
            raise ManifestError(f"unknown field(s): {', '.join(sorted(unknown))}",
                                field=where)
        gain_db = finite_number(spec.get("gain_db", 0.0), f"{where}.gain_db")
        automation = parse_automation(spec.get("automation"), f"{where}.automation")
        buses.append(Bus(id=bus_id, gain_db=gain_db, automation=automation))
    return buses


def _parse_tracks(raw: Any, root: Path, bus_ids: set[str]) -> list[Track]:
    if not isinstance(raw, list):
        raise ManifestError("tracks must be a non-empty array", field="tracks")
    tracks: list[Track] = []
    seen: set[str] = set()
    for index, item in enumerate(raw):
        where = f"tracks[{index}]"
        if not isinstance(item, dict):
            raise ManifestError("track entry must be an object", field=where)
        track_id = item.get("id")
        if not isinstance(track_id, str) or not track_id.strip():
            raise ManifestError("track is missing non-empty string field 'id'",
                                field=f"{where}.id")
        track_id = track_id.strip()
        if track_id in seen:
            raise ManifestError(f"duplicate track id '{track_id}'",
                                field=f"{where}.id", track_id=track_id)
        seen.add(track_id)
        tracks.append(_parse_track(item, track_id, f"{where}({track_id})", root,
                                   bus_ids))
    return tracks


def _parse_track(item: dict[str, Any], track_id: str, where: str, root: Path,
                 bus_ids: set[str]) -> Track:
    known = {"id", "path", "offset_ms", "channels", "gain_db", "bus", "automation"}
    unknown = set(item) - known
    if unknown:
        raise ManifestError(f"unknown field(s): {', '.join(sorted(unknown))}",
                            field=where, track_id=track_id)

    raw_path = item.get("path")
    if not isinstance(raw_path, str) or not raw_path.strip():
        raise ManifestError("missing non-empty string field 'path'",
                            field=f"{where}.path", track_id=track_id)
    source = Path(raw_path).expanduser()
    if not source.is_absolute():
        source = root / source
    source = source.resolve()

    offset_ms = finite_number(item.get("offset_ms", 0.0), f"{where}.offset_ms",
                              track_id=track_id)
    if offset_ms < 0:
        raise ManifestError("offset_ms must be >= 0", field=f"{where}.offset_ms",
                            track_id=track_id)

    channel_count, matrix = _parse_channels(item.get("channels"), where, track_id)
    gain_db = finite_number(item.get("gain_db", 0.0), f"{where}.gain_db",
                            track_id=track_id)

    bus = item.get("bus", "master")
    if not isinstance(bus, str) or not bus:
        raise ManifestError("bus must be a non-empty string", field=f"{where}.bus",
                            track_id=track_id)
    if bus not in bus_ids:
        raise ManifestError(f"unknown bus '{bus}'", field=f"{where}.bus",
                            track_id=track_id)

    automation = parse_automation(item.get("automation"), f"{where}.automation",
                                  track_id=track_id)
    return Track(
        id=track_id,
        path=source,
        offset_ms=offset_ms,
        channel_count=channel_count,
        channel_matrix=matrix,
        gain_db=gain_db,
        bus=bus,
        automation=automation,
    )


def _parse_channels(raw: Any, where: str,
                    track_id: str) -> tuple[int, tuple[tuple[float, ...], ...]]:
    field_name = f"{where}.channels"
    if raw is None:
        return 2, ((1.0, 0.0), (0.0, 1.0))
    if isinstance(raw, str):
        count = _CHANNEL_ALIASES.get(raw.lower())
        if count is None:
            raise ManifestError("channels must be 'mono', 'stereo' or a matrix",
                                field=field_name, track_id=track_id)
        if count == 1:
            return 1, ((1.0,), (1.0,))
        return 2, ((1.0, 0.0), (0.0, 1.0))
    if not isinstance(raw, list) or not raw or not all(isinstance(r, list) for r in raw):
        raise ManifestError("channels matrix must be a non-empty array of arrays",
                            field=field_name, track_id=track_id)
    width = len(raw[0])
    if width == 0:
        raise ManifestError("channels matrix rows must not be empty",
                            field=field_name, track_id=track_id)
    rows: list[tuple[float, ...]] = []
    for r_index, row in enumerate(raw):
        if len(row) != width:
            raise ManifestError("channels matrix rows must have equal length",
                                field=f"{field_name}[{r_index}]", track_id=track_id)
        rows.append(tuple(
            finite_number(value, f"{field_name}[{r_index}][{c_index}]",
                          track_id=track_id)
            for c_index, value in enumerate(row)
        ))
    if len(rows) != 2:
        raise ManifestError(f"channels matrix must have 2 output rows, got {len(rows)}",
                            field=field_name, track_id=track_id)
    return width, tuple(rows)
