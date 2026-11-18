"""Package builder: accepts media only after extractor-based validation.

The packer reuses the same extraction and comparison results as the
inspector. A package is written to a temporary sibling directory and
atomically renamed into place, so a failed validation never leaves a
half-finished package behind.
"""
from __future__ import annotations

import json
import os
import shutil
import tempfile
from dataclasses import dataclass
from datetime import datetime, timezone
from pathlib import Path

from .extract import EXTRACTOR_VERSION


class PackError(Exception):
    def __init__(self, message: str, exit_code: int = 2):
        super().__init__(message)
        self.exit_code = exit_code


@dataclass
class PackPolicy:
    max_duration_delta_ms: float = 40.0

    def to_dict(self) -> dict:
        return {"max_duration_delta_ms": self.max_duration_delta_ms}


def build_package(results, mismatches_by_path: dict, out_dir,
                  policy: PackPolicy) -> dict:
    """Validate *results* and write the package; return the manifest.

    Raises PackError (and writes nothing) when any file failed structural
    validation or any mismatch falls outside the accepted policy.
    """
    broken = [r for r in results if not r.ok]
    if broken:
        names = ", ".join(r.path for r in broken)
        raise PackError(
            f"refusing to pack: {len(broken)} file(s) failed structural "
            f"validation ({names})", exit_code=2)
    mismatch_total = sum(len(v) for v in mismatches_by_path.values())
    if mismatch_total:
        raise PackError(
            f"refusing to pack: {mismatch_total} mismatch(es) exceed the "
            f"pack policy (max duration delta "
            f"{policy.max_duration_delta_ms} ms)", exit_code=1)
    if not results:
        raise PackError("refusing to pack: no input files", exit_code=3)
    out = Path(out_dir)
    if out.exists() or out.is_symlink():
        raise PackError(f"output path already exists: {out}", exit_code=3)
    try:
        out.parent.mkdir(parents=True, exist_ok=True)
    except OSError as exc:
        raise PackError(f"cannot create output directory: {exc}",
                        exit_code=3) from exc
    tmp = Path(tempfile.mkdtemp(prefix=".trackaudit-pack-",
                                dir=str(out.parent)))
    try:
        media_dir = tmp / "media"
        media_dir.mkdir()
        entries = []
        for info in sorted(results, key=lambda r: r.path):
            source = Path(info.path)
            shutil.copy2(source, media_dir / source.name)
            entries.append({
                "name": source.name,
                "sha256": info.sha256,
                "size": info.size,
                "tracks": len(info.tracks),
                "duration_ms": info.duration_ms,
                "codec_summary": info.codec_summary,
            })
        manifest = {
            "created_at": datetime.now(timezone.utc).isoformat(),
            "extractor_version": EXTRACTOR_VERSION,
            "policy": policy.to_dict(),
            "files": entries,
        }
        (tmp / "manifest.json").write_text(
            json.dumps(manifest, indent=2, sort_keys=True) + "\n",
            encoding="utf-8")
        os.rename(tmp, out)
    except Exception:
        shutil.rmtree(tmp, ignore_errors=True)
        raise
    return manifest
