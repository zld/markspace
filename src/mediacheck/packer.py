"""Packing manifests built from the same extractor as the validator.

Packing refuses media whose comparison failed, unless ``allow_issues`` is set
explicitly, in which case the diagnostics travel with the manifest.
"""

from __future__ import annotations

import json
from pathlib import Path
from typing import Any, Iterable

from .compare import blocking_mismatches
from .model import MediaFile, ValidationResult

MANIFEST_VERSION = 1


class PackError(Exception):
    """A manifest could not be produced."""


def build_manifest(entries: Iterable[tuple[MediaFile, ValidationResult | None]],
                   allow_issues: bool = False,
                   generator: str = "mediacheck") -> dict[str, Any]:
    """Return a JSON-ready manifest for ``(media, validation)`` pairs.

    ``status`` is ``ok``, ``blocked`` (fatal probe or blocking mismatch) or
    ``diagnostic`` (problems exist but ``allow_issues`` was requested).
    """
    items: list[dict[str, Any]] = []
    blocked: list[dict[str, Any]] = []
    for media, validation in entries:
        problems = _problems(media, validation)
        item = {
            "path": media.path,
            "sha256": media.sha256,
            "size": media.size,
            "container": media.container,
            "duration_ms": media.duration_ms,
            "probe_ok": media.probe_ok,
            "tracks": [
                {
                    "track_id": t.track_id,
                    "kind": t.kind,
                    "codec": t.codec,
                    "time_base": t.time_base,
                    "duration_ms": t.duration_ms,
                    "codec_detail": dict(t.codec_detail),
                }
                for t in media.tracks
            ],
            "issues": [i.to_dict() for i in media.issues],
            "validation": None if validation is None else {
                "status": validation.status,
                "tolerance_ms": validation.tolerance_ms,
                "mismatch_count": len(validation.mismatches),
                "mismatches": [m.to_dict() for m in validation.mismatches],
            },
            "blocking_problems": problems,
        }
        if problems:
            blocked.append({
                "path": media.path,
                "sha256": media.sha256,
                "problems": problems,
            })
        items.append(item)

    if not blocked:
        status = "ok"
    elif allow_issues:
        status = "diagnostic"
    else:
        status = "blocked"
    return {
        "manifest_version": MANIFEST_VERSION,
        "generator": generator,
        "allow_issues": allow_issues,
        "status": status,
        "file_count": len(items),
        "track_count": sum(len(i["tracks"]) for i in items),
        "blocked": blocked,
        "files": items,
    }


def _problems(media: MediaFile,
              validation: ValidationResult | None) -> list[str]:
    problems: list[str] = []
    if media.fatal:
        problems.append("probe failed fatally")
    for issue in media.issues:
        if issue.severity in ("error", "fatal"):
            problems.append(f"{issue.code}: {issue.message}")
    if validation is not None:
        for mismatch in blocking_mismatches(validation):
            problems.append(f"{mismatch.kind}: {mismatch.message}")
    return problems


def _write_json_direct(manifest: dict[str, Any], path: Path) -> None:
    """Write the manifest without creating directories.

    The packer must fail (exit 3) rather than silently create an output tree
    the build system did not ask for, so missing parents are an error here.
    """
    try:
        path.write_text(json.dumps(manifest, indent=2, sort_keys=True) + "\n",
                        encoding="utf-8")
    except OSError as exc:
        raise PackError(
            f"cannot write manifest to {path}: {exc.strerror or exc}") from exc


def write_manifest(manifest: dict[str, Any], json_path: str | Path,
                   create_parents: bool = False) -> Path:
    """Write the manifest as UTF-8 JSON.

    By default a missing parent directory is an error so the CLI can report a
    stable code 3.  Callers that own the layout may opt into creation.
    """
    path = Path(json_path)
    parent = path.parent
    if str(parent) and not parent.exists():
        if not create_parents:
            raise PackError(
                f"cannot write manifest to {path}: output directory "
                f"does not exist: {parent}")
        try:
            parent.mkdir(parents=True, exist_ok=True)
        except OSError as exc:
            raise PackError(
                f"cannot create output directory {parent}: "
                f"{exc.strerror or exc}") from exc
    _write_json_direct(manifest, path)
    return path


def default_manifest_path(package_dir: str | Path,
                          name: str = "manifest.json") -> Path:
    return Path(package_dir) / name
