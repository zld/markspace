"""Package-time input checks reusing the shared probe extractor.

A package is only written after every check passes; output is written
atomically (temp file plus rename) so a failure never leaves a half-built
artifact behind.
"""
from __future__ import annotations

import json
import os
from pathlib import Path

from .model import (
    KIND_AUDIO,
    KIND_VIDEO,
    Media,
    PackageManifest,
    Track,
    TrackError,
)

DEFAULT_REQUIRED_KINDS = (KIND_VIDEO, KIND_AUDIO)


class PackageError(ValueError):
    """Raised when package input validation fails."""

    def __init__(self, errors: list[TrackError]):
        self.errors = errors
        super().__init__("; ".join(e.message for e in errors))


class PackageOutputError(OSError):
    """Raised when the package manifest cannot be written."""


def check_package_inputs(
    media: Media,
    required_kinds: tuple[int, ...] = DEFAULT_REQUIRED_KINDS,
) -> list[TrackError]:
    """Return validation errors; empty list means the input is packageable."""
    errors: list[TrackError] = list(media.errors)
    seen: set[int] = set()
    present_kinds: set[int] = set()
    for track in media.tracks:
        if track.track_id in seen:
            errors.append(TrackError(
                code="E_DUPLICATE_TRACK_ID",
                message=f"duplicate track id {track.track_id}",
                track_id=track.track_id,
            ))
        seen.add(track.track_id)
        present_kinds.add(track.kind)
        if track.timebase <= 0:
            errors.append(TrackError(
                code="E_ZERO_TIMEBASE",
                message=f"track {track.track_id} has non-positive timebase "
                        f"{track.timebase}",
                track_id=track.track_id,
            ))
        if track.sample_count <= 0:
            errors.append(TrackError(
                code="E_EMPTY_TRACK",
                message=f"track {track.track_id} has no samples",
                track_id=track.track_id,
            ))
        elif track.start_ts > track.sample_count:
            errors.append(TrackError(
                code="E_ILLEGAL_TIMESTAMP",
                message=f"track {track.track_id} start_ts={track.start_ts} "
                        f"lies beyond sample_count={track.sample_count}",
                track_id=track.track_id,
            ))
    for kind in required_kinds:
        if kind not in present_kinds:
            errors.append(TrackError(
                code="E_MISSING_REQUIRED_TRACK",
                message=f"required track kind {kind} is missing",
            ))
    return errors


def _atomic_write_json(output: Path, payload: dict) -> None:
    output.parent.mkdir(parents=True, exist_ok=True)
    tmp = output.with_name(f".{output.name}.tmp")
    try:
        with tmp.open("w", encoding="utf-8") as fh:
            json.dump(payload, fh, indent=2, sort_keys=True)
            fh.write("\n")
        os.replace(tmp, output)
    except OSError as exc:
        try:
            tmp.unlink(missing_ok=True)
        except OSError:
            pass
        raise PackageOutputError(f"cannot write {output}: {exc}") from exc


def package_media(
    media: Media,
    output_path: str | Path,
    required_kinds: tuple[int, ...] = DEFAULT_REQUIRED_KINDS,
) -> PackageManifest:
    """Validate and atomically write the package manifest."""
    errors = check_package_inputs(media, required_kinds)
    if errors:
        raise PackageError(errors)
    output = Path(output_path)
    manifest = PackageManifest(
        output_path=str(output),
        source_path=media.path,
        track_count=len(media.tracks),
        tracks=list(media.tracks),
        content_hash=media.content_hash,
    )
    _atomic_write_json(output, manifest.to_dict())
    return manifest
