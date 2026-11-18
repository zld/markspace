"""Input discovery and baseline resolution.

All filesystem access for the CLI funnels through here so that path
containment, stable ordering and baseline precedence are defined once.
"""

from __future__ import annotations

import json
from pathlib import Path
from typing import Iterable, Sequence

from .model import SniffResult
from .sniff import sniff_file
from .store import Store
from .util import is_within

MEDIA_SUFFIXES = {".wav", ".mp4", ".mov", ".m4a", ".m4v", ".qt"}
BASELINE_FILENAMES = ("baseline.json", "expected.json")


class UsageError(Exception):
    """Raised for invalid command-line combinations (exit code 2)."""


def collect_media(paths: Iterable[str]) -> tuple[list[Path], list[Path]]:
    """Expand inputs into a sorted, de-duplicated media list.

    Returns ``(files, roots)``.  Directories are walked recursively and
    files are ordered by their POSIX path so output is reproducible.
    """
    files: list[Path] = []
    roots: list[Path] = []
    seen: set[Path] = set()
    for raw in paths:
        path = Path(raw)
        if not path.exists():
            raise UsageError(f"input does not exist: {raw}")
        roots.append(path.resolve())
        if path.is_dir():
            for candidate in path.rglob("*"):
                if candidate.is_file() and candidate.suffix.lower() in MEDIA_SUFFIXES:
                    resolved = candidate.resolve()
                    if resolved in seen:
                        continue
                    seen.add(resolved)
                    files.append(candidate)
        elif path.is_file():
            resolved = path.resolve()
            if resolved not in seen:
                seen.add(resolved)
                files.append(path)
    files.sort(key=lambda p: p.as_posix())
    return files, roots


def relative_path(path: Path, roots: Sequence[Path]) -> str:
    """Relative path against the deepest input root that contains it.

    Explicit file arguments use their own file name, so ``compare file.wav``
    reports ``file.wav`` rather than an opaque ``.``.
    """
    resolved = path.resolve()
    if resolved.is_file() and resolved in roots:
        return resolved.name
    best: Path | None = None
    for root in roots:
        if root == resolved:
            continue
        try:
            resolved.relative_to(root)
        except ValueError:
            continue
        if best is None or len(root.parts) > len(best.parts):
            best = root
    if best is not None:
        return resolved.relative_to(best).as_posix()
    return resolved.name


def explicit_baselines(directory: Path) -> dict[str, SniffResult]:
    """Sniff every media file under an explicit baseline directory."""
    baseline_files, _ = collect_media([str(directory)])
    baselines: dict[str, SniffResult] = {}
    for path in baseline_files:
        if not is_within(directory, path):
            raise UsageError(f"baseline path escapes root: {path}")
        relative = path.resolve().relative_to(directory).as_posix()
        baselines[relative] = sniff_file(path)
    return baselines


def manifest_baselines(directory: Path) -> dict[str, SniffResult]:
    """Load ``baseline.json`` from a directory, if present and valid."""
    for name in BASELINE_FILENAMES:
        candidate = directory / name
        if not candidate.is_file():
            continue
        try:
            raw = json.loads(candidate.read_text(encoding="utf-8"))
        except (OSError, json.JSONDecodeError) as exc:
            raise UsageError(f"cannot read baseline manifest {candidate}: {exc}")
        entries = raw.get("files")
        if not isinstance(entries, list):
            raise UsageError(f"baseline manifest {candidate} has no files list")
        result: dict[str, SniffResult] = {}
        for entry in entries:
            if not isinstance(entry, dict) or "path" not in entry:
                raise UsageError(f"malformed baseline entry in {candidate}")
            record = entry.get("record")
            if record is None:
                record = {
                    "container": entry.get("container", "unknown"),
                    "size": 0,
                    "declared_duration_ms": entry.get("declared_duration_ms"),
                    "tracks": entry.get("tracks", []),
                    "warnings": [],
                    "error": entry.get("error"),
                    "truncated": entry.get("truncated", False),
                }
            result[str(entry["path"])] = SniffResult.from_dict(record)
        return result
    return {}


def baseline_for(
    relative: str,
    path: Path,
    explicit: dict[str, SniffResult] | None,
    store: Store,
) -> SniffResult | None:
    """Resolve the expected state for one file.

    Precedence: explicit ``--baseline`` directory, a ``baseline.json``
    manifest beside the file, then the last successful scan recorded for
    this path in the database.
    """
    if explicit is not None:
        return explicit.get(relative)
    manifest = manifest_baselines(path.parent)
    if relative in manifest:
        return manifest[relative]
    scan_id = store.find_scan_by_path(relative)
    if scan_id is not None:
        return store.load_scan(scan_id)
    return None
