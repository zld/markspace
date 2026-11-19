"""Normalize sniff results into serializable media and track summaries.

``extract`` is the single place durations are converted to seconds, so the
manifest written by ``pack`` and the checks run by ``compare``/``scan`` can
never disagree about metadata for the same file.
"""

from __future__ import annotations

import os
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any

from . import MANIFEST_VERSION
from .sniff import SniffError, SniffResult, SniffTrack, sniff_path

MEDIA_KINDS = ("video", "audio", "subtitle", "timecode", "hint", "other")
_REASON_NULL = "value unavailable from container header"


class ExtractError(Exception):
    """A file could not be read or understood; carries a locatable payload."""

    def __init__(self, path: str, code: str, message: str, *,
                 offset: int | None = None, box_path: str | None = None):
        super().__init__(message)
        self.path = path
        self.code = code
        self.message = message
        self.offset = offset
        self.box_path = box_path

    def to_dict(self) -> dict:
        return {
            "path": self.path,
            "code": self.code,
            "message": self.message,
            "offset": self.offset,
            "box_path": self.box_path,
        }


def _seconds(units: int | None, timescale: int | None) -> float | None:
    if units is None or timescale is None or timescale <= 0:
        return None
    return units / timescale


def normalize_kind(handler: str | None) -> str:
    if handler == "video":
        return "video"
    if handler == "audio":
        return "audio"
    if handler in ("text", "subtitle"):
        return "subtitle"
    if handler == "timecode":
        return "timecode"
    if handler == "hint":
        return "hint"
    return "other"


def _track_summary(track: SniffTrack, index: int, *,
                   movie_timescale: int | None = None,
                   movie_duration_units: int | None = None
                   ) -> dict[str, Any]:
    reasons = dict(track.reasons)
    duration_s = _seconds(track.duration_units, track.timescale)
    start_units = track.start_time_units
    if start_units is None and track.timescale:
        # Absent edit list means the media starts at zero.
        start_units = 0
    start_s = _seconds(start_units, track.timescale)
    # An empty leading edit delays presentation. Convert it from movie units
    # to seconds so every duration in the summary lives on one time base.
    presentation_s = _seconds(track.start_movie_units, movie_timescale)
    if presentation_s is None:
        presentation_s = 0.0
    if start_s is not None:
        start_s += presentation_s
    if duration_s is None:
        reasons.setdefault("duration_seconds", _REASON_NULL)
    if start_s is None:
        reasons.setdefault("start_seconds", _REASON_NULL)

    entry: dict[str, Any] = {
        "index": index,
        "track_id": track.track_id,
        "kind": normalize_kind(track.handler),
        "handler": track.handler,
        "codec": track.codec,
        "codec_raw": track.codec_raw,
        "timescale": track.timescale,
        "duration_units": track.duration_units,
        "duration_seconds": _round(duration_s),
        "start_units": start_units,
        "presentation_offset_seconds": _round(presentation_s),
        "start_seconds": _round(start_s),
        "language": track.language,
        "width": track.width,
        "height": track.height,
        "sample_rate": track.sample_rate,
        "channels": track.channels,
        "bit_depth": track.bit_depth,
        "reasons": reasons,
        "warnings": [w.to_dict() for w in track.warnings],
    }
    for key in ("track_id", "kind", "codec", "duration_seconds", "start_seconds"):
        if entry[key] is None:
            reasons.setdefault(key, _REASON_NULL)
    return entry


def _round(value: float | None, digits: int = 6) -> float | None:
    if value is None:
        return None
    return round(value, digits)


def summarize_file(path: str | Path) -> dict[str, Any]:
    """Sniff one local file and return its normalized summary dict."""
    target = Path(path)
    try:
        result = sniff_path(target)
    except SniffError as exc:
        raise ExtractError(str(target), exc.code, exc.message,
                           offset=exc.offset, box_path=exc.path) from None
    except OSError as exc:
        raise ExtractError(str(target), "io_error",
                           f"{type(exc).__name__}: {exc.strerror or exc}") from None
    return summary_from_sniff(target, result)


def summary_from_sniff(target: Path, result: SniffResult) -> dict[str, Any]:
    tracks = [_track_summary(t, i, movie_timescale=result.movie_timescale,
                             movie_duration_units=result.movie_duration_units)
              for i, t in enumerate(result.tracks)]
    durations = [t["duration_seconds"] for t in tracks
                 if t["duration_seconds"] is not None]
    starts = [t["start_seconds"] for t in tracks
              if t["start_seconds"] is not None]
    kinds = sorted({t["kind"] for t in tracks})
    duration_s = max(durations) if durations else None
    summary: dict[str, Any] = {
        "manifest_version": MANIFEST_VERSION,
        "path": str(target),
        "name": target.name,
        "container": result.container,
        "file_size": result.file_size,
        "brands": list(result.brands),
        "movie_timescale": result.movie_timescale,
        "movie_duration_units": result.movie_duration_units,
        "movie_duration_seconds": _round(
            _seconds(result.movie_duration_units, result.movie_timescale)),
        "track_count": len(tracks),
        "track_kinds": kinds,
        "duration_seconds": _round(duration_s),
        "start_seconds": _round(min(starts)) if starts else None,
        "codec_summary": sorted(
            {t["codec"] for t in tracks if t["codec"] is not None}),
        "tracks": tracks,
        "warnings": [w.to_dict() for w in result.warnings],
    }
    if summary["duration_seconds"] is None:
        summary["duration_reason"] = _REASON_NULL
    return summary


def summarize_many(paths: list[Path]) -> tuple[list[dict], list[dict]]:
    """Summarize files, returning (summaries, failures) without aborting."""
    summaries: list[dict] = []
    failures: list[dict] = []
    for path in paths:
        try:
            summaries.append(summarize_file(path))
        except ExtractError as exc:
            failures.append(exc.to_dict())
    return summaries, failures


@dataclass
class ExtractBatch:
    summaries: list[dict] = field(default_factory=list)
    failures: list[dict] = field(default_factory=list)

    @property
    def ok(self) -> bool:
        return not self.failures


SUPPORTED_SUFFIXES = {".mp4", ".m4v", ".m4a", ".mov", ".wav", ".wave"}


def discover_media(root: Path, *, recursive: bool = True,
                   failures: list[dict] | None = None) -> list[Path]:
    """Return media files under root in a stable, platform-independent order.

    Directories that cannot be listed are appended to ``failures`` (when a
    list is supplied) so a permission problem on one subtree does not hide
    files elsewhere in the batch.
    """
    if root.is_file():
        return [root]
    if not root.is_dir():
        raise ExtractError(str(root), "not_found", f"{root} does not exist")

    unreadable = list(failures) if failures is not None else None

    def on_error(exc: OSError) -> None:
        if unreadable is not None:
            unreadable.append({
                "path": exc.filename or str(root),
                "code": "io_error",
                "message": f"{type(exc).__name__}: {exc.strerror or exc}",
                "offset": None,
                "box_path": None,
            })

    files: list[Path] = []
    if recursive:
        for dirpath, _dirnames, filenames in os.walk(root, onerror=on_error):
            base = Path(dirpath)
            for name in filenames:
                candidate = base / name
                if candidate.suffix.lower() in SUPPORTED_SUFFIXES:
                    files.append(candidate)
    else:
        try:
            for candidate in root.iterdir():
                if candidate.is_file() and \
                        candidate.suffix.lower() in SUPPORTED_SUFFIXES:
                    files.append(candidate)
        except OSError as exc:
            on_error(exc)
    if failures is not None and unreadable is not None:
        failures[:] = unreadable
    return sorted(files, key=lambda p: p.relative_to(root).as_posix())
