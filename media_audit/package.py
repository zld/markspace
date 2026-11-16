"""Reusable packaging entry point built on top of :mod:`media_audit.probe`.

Packaging never re-parses a file with separate logic: every candidate is first
validated through :func:`media_audit.probe.probe_file` (the same bounded
parsers and limits used by audits). Truncated files and unsupported tracks
reject the whole request *before* anything is copied, naming the file and the
byte offset where parsing stopped.
"""

from __future__ import annotations

import json
import os
import shutil
from dataclasses import dataclass, field
from typing import Any

from .probe import MediaInfo, SUPPORTED_EXTENSIONS, probe_file


class PackageError(Exception):
    """Raised when a package request cannot be fulfilled safely."""

    def __init__(self, message: str, file: str | None = None,
                 offset: int | None = None) -> None:
        super().__init__(message)
        self.file = file
        self.offset = offset


@dataclass
class PackageResult:
    dest: str
    files: list[str]
    input_summary: dict[str, Any] = field(default_factory=dict)
    manifest_path: str = ""


def _unsupported_track_reason(info: MediaInfo) -> str | None:
    for track in info.tracks:
        if track.type == "unknown":
            return f"track {track.track_id} has an unsupported handler type"
        if track.codec_family == "unknown":
            return (
                f"track {track.track_id} ({track.type}) uses unsupported "
                f"codec {track.codec!r}"
            )
    return None


def validate_for_packaging(path: str, limits=None) -> MediaInfo:
    """Run the shared boundary/extraction checks for one candidate."""
    info = probe_file(path, limits)
    ext = os.path.splitext(info.name)[1].lower()
    if ext not in SUPPORTED_EXTENSIONS:
        raise PackageError(
            f"unsupported container extension {ext or '<none>'}",
            file=info.name,
        )
    if info.parse_error is not None:
        raise PackageError(
            f"rejected, parsing failed: {info.parse_error}",
            file=info.name,
            offset=info.error_offset,
        )
    reason = _unsupported_track_reason(info)
    if reason is not None:
        raise PackageError(f"rejected, {reason}", file=info.name)
    return info


def package_files(
    paths: list[str], dest_dir: str, input_root: str, limits=None
) -> PackageResult:
    """Validate then copy every input into ``dest_dir``.

    All validation happens first; a single rejection aborts the package and
    no destination files are written.
    """
    if not paths:
        raise PackageError("package request contains no files")
    root = os.path.abspath(input_root)
    dest = os.path.abspath(dest_dir)
    if dest == root or dest.startswith(root + os.sep):
        raise PackageError(
            f"package destination {dest_dir} must lie outside the input root"
        )

    infos: list[MediaInfo] = []
    for path in paths:
        candidate = os.path.abspath(path)
        if candidate != root and not candidate.startswith(root + os.sep):
            raise PackageError(
                f"input {path} lies outside the allowed input root",
                file=os.path.basename(path),
            )
        infos.append(validate_for_packaging(path, limits))

    os.makedirs(dest, exist_ok=True)
    copied: list[str] = []
    track_totals = {"video": 0, "audio": 0, "subtitle": 0, "other": 0}
    try:
        for info in infos:
            rel = os.path.relpath(os.path.abspath(info.path), root)
            target = os.path.join(dest, rel)
            os.makedirs(os.path.dirname(target) or dest, exist_ok=True)
            shutil.copy2(info.path, target)
            copied.append(rel)
            for key, value in info.track_counts().items():
                track_totals[key] += value
    except OSError as exc:
        raise PackageError(f"failed while copying package files: {exc}") from exc

    summary = {
        "file_count": len(infos),
        "total_bytes": sum(info.byte_size for info in infos),
        "tracks": track_totals,
        "files": [
            {
                "name": info.name,
                "container": info.container,
                "byte_size": info.byte_size,
                "duration_ms": info.duration_ms,
                "brands": list(info.brands),
                "track_count": len(info.tracks),
            }
            for info in sorted(infos, key=lambda item: item.name)
        ],
    }
    manifest_path = os.path.join(dest, "package_manifest.json")
    with open(manifest_path, "w", encoding="utf-8") as handle:
        json.dump(
            {"package": "media-audit", "input_summary": summary,
             "members": sorted(copied)},
            handle, indent=2,
        )
        handle.write("\n")
    return PackageResult(
        dest=dest,
        files=sorted(copied),
        input_summary=summary,
        manifest_path=manifest_path,
    )
