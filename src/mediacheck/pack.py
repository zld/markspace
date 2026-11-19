"""Build a local delivery manifest from media files.

The manifest embeds the exact same summaries ``scan`` persists and
``compare`` consumes, so packaging and verification cannot drift apart.
"""

from __future__ import annotations

import json
import os
from datetime import datetime, timezone
from pathlib import Path
from typing import Any

from . import MANIFEST_VERSION
from .extract import ExtractError, discover_media, summarize_file


class PackError(Exception):
    def __init__(self, message: str, *, path: str | None = None):
        super().__init__(message)
        self.message = message
        self.path = path

    def to_dict(self) -> dict:
        return {"path": self.path, "code": "pack_error", "message": self.message}


def build_manifest(root: str | Path, *, recursive: bool = True,
                   label: str | None = None) -> dict[str, Any]:
    """Summarize every media file under root into a manifest dict.

    Individual failures are recorded in ``failures`` and do not abort the
    batch. A failure to even enumerate the root raises PackError.
    """
    root_path = Path(root)
    failures: list[dict] = []
    try:
        files = discover_media(root_path, recursive=recursive,
                               failures=failures)
    except ExtractError as exc:
        raise PackError(exc.message, path=exc.path) from None
    except OSError as exc:
        raise PackError(f"cannot list {root_path}: {exc}", path=str(root_path)) from None

    media: list[dict] = []
    for path in files:
        try:
            summary = summarize_file(path)
        except ExtractError as exc:
            failures.append(exc.to_dict())
        else:
            summary["relative_path"] = _relative(path, root_path)
            media.append(summary)

    tracks_total = sum(m["track_count"] for m in media)
    manifest = {
        "manifest_version": MANIFEST_VERSION,
        "kind": "mediacheck.manifest",
        "generated_at": datetime.now(timezone.utc).isoformat(timespec="seconds"),
        "label": label or root_path.name or str(root_path),
        "root": str(root_path.resolve()),
        "recursive": recursive,
        "files_scanned": len(files),
        "files_ok": len(media),
        "files_failed": len(failures),
        "tracks_total": tracks_total,
        "media": media,
        "failures": failures,
    }
    return manifest


def _relative(path: Path, root: Path) -> str:
    try:
        return path.relative_to(root).as_posix()
    except ValueError:
        return path.as_posix()


def write_manifest(manifest: dict[str, Any], out: str | Path) -> Path:
    """Write the manifest JSON atomically; raise PackError on write failure."""
    target = Path(out)
    tmp = target.with_name(target.name + ".tmp")
    try:
        target.parent.mkdir(parents=True, exist_ok=True)
        payload = json.dumps(manifest, ensure_ascii=False, indent=2, sort_keys=False)
        with open(tmp, "w", encoding="utf-8") as handle:
            handle.write(payload)
            handle.write("\n")
            handle.flush()
            os.fsync(handle.fileno())
        os.replace(tmp, target)
    except OSError as exc:
        try:
            tmp.unlink(missing_ok=True)
        except OSError:
            pass
        raise PackError(f"cannot write manifest to {target}: {exc}",
                        path=str(target)) from None
    return target


def load_manifest(path: str | Path) -> dict[str, Any]:
    """Load and validate a manifest or a raw single-media summary."""
    target = Path(path)
    try:
        raw = target.read_text(encoding="utf-8")
    except OSError as exc:
        raise PackError(f"cannot read {target}: {exc}", path=str(target)) from None
    try:
        data = json.loads(raw)
    except json.JSONDecodeError as exc:
        raise PackError(
            f"{target}: invalid JSON at line {exc.lineno} column {exc.colno}: {exc.msg}",
            path=str(target)) from None
    if not isinstance(data, dict):
        raise PackError(f"{target}: top-level JSON must be an object", path=str(target))
    if "media" in data:
        media = data["media"]
        if not isinstance(media, list):
            raise PackError(f"{target}: 'media' must be a list", path=str(target))
        return data
    if "tracks" in data and "container" in data:
        # A single media summary; wrap it so compare accepts it uniformly.
        return {
            "manifest_version": data.get("manifest_version", MANIFEST_VERSION),
            "kind": "mediacheck.manifest",
            "label": data.get("name", target.stem),
            "root": str(target.parent.resolve()),
            "media": [data],
            "failures": [],
            "files_scanned": 1,
            "files_ok": 1,
            "files_failed": 0,
            "tracks_total": data.get("track_count", len(data.get("tracks", []))),
        }
    raise PackError(
        f"{target}: unrecognized document; expected a manifest with 'media' "
        "or a single media summary with 'tracks'", path=str(target))
