"""Build a pack manifest from the same sniffer used by ``compare``.

Inputs carrying structural errors or ambiguous tracks are refused outright:
a manifest must never contain a file the tool could not verify.
"""

from __future__ import annotations

from dataclasses import dataclass, field
from pathlib import Path
from typing import Any

from . import __version__
from .compare import match_key
from .model import SniffResult
from .sniff import sniff_file
from .util import is_within, sha256_file


@dataclass(slots=True)
class PackedFile:
    relative_path: str
    sha256: str
    container: str
    size: int
    declared_duration_ms: float | None
    tracks: list[dict[str, Any]]
    truncated: bool

    def to_dict(self) -> dict[str, Any]:
        return {
            "path": self.relative_path,
            "sha256": self.sha256,
            "container": self.container,
            "size": self.size,
            "declared_duration_ms": self.declared_duration_ms,
            "truncated": self.truncated,
            "tracks": list(self.tracks),
        }


@dataclass(slots=True)
class RejectedFile:
    relative_path: str
    reason: str

    def to_dict(self) -> dict[str, Any]:
        return {"path": self.relative_path, "reason": self.reason}


@dataclass(slots=True)
class PackManifest:
    root: str
    files: list[PackedFile] = field(default_factory=list)
    rejected: list[RejectedFile] = field(default_factory=list)

    def to_dict(self) -> dict[str, Any]:
        return {
            "tool": "mediacheck",
            "tool_version": __version__,
            "root": self.root,
            "packed_files": len(self.files),
            "rejected_files": len(self.rejected),
            "files": [f.to_dict() for f in self.files],
            "rejected": [r.to_dict() for r in self.rejected],
        }


def _ambiguity(result: SniffResult) -> str | None:
    seen: dict[tuple, int] = {}
    for track in result.tracks:
        key = match_key(track)
        seen[key] = seen.get(key, 0) + 1
        if seen[key] > 1:
            kind, language, codec, index = key
            return (
                "ambiguous track identity "
                f"{kind}/{language or '?'}/{codec or '?'}/#{index}"
            )
    return None


def build_manifest(root: Path, media_paths: list[Path]) -> PackManifest:
    manifest = PackManifest(root=str(root))
    for path in media_paths:
        resolved = path.resolve()
        if not is_within(root, resolved):
            manifest.rejected.append(
                RejectedFile(
                    path.name, "path escapes the input root"
                )
            )
            continue
        relative = resolved.relative_to(root.resolve()).as_posix()
        result = sniff_file(resolved)
        if result.error:
            manifest.rejected.append(RejectedFile(relative, result.error))
            continue
        if result.truncated:
            manifest.rejected.append(
                RejectedFile(relative, "container is truncated")
            )
            continue
        if not result.tracks:
            manifest.rejected.append(
                RejectedFile(relative, "container declares no tracks")
            )
            continue
        ambiguous = _ambiguity(result)
        if ambiguous:
            manifest.rejected.append(RejectedFile(relative, ambiguous))
            continue
        manifest.files.append(
            PackedFile(
                relative_path=relative,
                sha256=sha256_file(resolved),
                container=result.container,
                size=result.size,
                declared_duration_ms=result.declared_duration_ms,
                tracks=[t.to_dict() for t in result.tracks],
                truncated=False,
            )
        )
    return manifest
