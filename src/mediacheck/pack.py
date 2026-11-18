"""Packaging manifest built from the same probe results as scan/compare."""

from __future__ import annotations

from typing import Any

from .models import UNKNOWN, Container, issue_sort_key
from .probe import MAX_FILE_BYTES, probe_path


def _track_row(container: Container, track: Any) -> dict[str, Any]:
    return {
        "path": container.path,
        "track_id": track.track_id,
        "kind": track.kind,
        "codec": track.codec if track.codec is not None else UNKNOWN,
        "timescale": track.timescale,
        "start_ms": track.start_ms,
        "duration_ms": track.duration_ms,
        "params": {key: track.params[key] for key in sorted(track.params)},
        "problems": list(track.problems),
    }


def build_pack(paths: list[str], max_bytes: int = MAX_FILE_BYTES) -> dict[str, Any]:
    """Probe `paths` and build a deterministic manifest.

    The manifest reuses :func:`probe.probe_path` output verbatim; it never
    re-parses or normalises values on its own.
    """

    containers = [probe_path(path, max_bytes=max_bytes) for path in sorted(paths)]
    manifest: dict[str, Any] = {
        "format": "mediacheck-pack/1",
        "inputs": [],
        "tracks": [],
        "errors": [],
    }
    for container in containers:
        manifest["inputs"].append({
            "path": container.path,
            "sha256": container.sha256,
            "size": container.size,
            "container": container.container,
            "duration_ms": container.duration_ms,
            "error": container.error,
        })
        for track in container.tracks:
            manifest["tracks"].append(_track_row(container, track))
        if container.error is not None:
            manifest["errors"].append({
                "path": container.path,
                "message": container.error,
                "offset": container.error_offset,
            })
    manifest["tracks"].sort(
        key=lambda row: (
            row["path"],
            row["track_id"] is None,
            row["track_id"] if row["track_id"] is not None else 0,
        )
    )
    exit_code = 1 if manifest["errors"] else 0
    return {"manifest": manifest, "exit_code": exit_code}


__all__ = ["build_pack", "issue_sort_key"]
