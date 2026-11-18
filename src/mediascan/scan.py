"""File discovery and per-file scanning that never aborts the batch."""

from __future__ import annotations

import os
import stat
from dataclasses import dataclass, field

from .errors import ERR_INTERNAL, ERR_PERMISSION, ScanFailure
from .extract import Record, extract_file
from .limits import Limits

SKIP_EXTENSIONS = frozenset(
    {".json", ".txt", ".md", ".py", ".sqlite", ".sqlite3", ".db", ".log", ".csv"}
)


@dataclass
class FileTask:
    path: str  # absolute or as given on the CLI
    rel_path: str  # path used for baseline lookup and reporting


@dataclass
class ScanOutcome:
    task: FileTask
    record: Record | None = None
    failure: ScanFailure | None = None

    @property
    def ok(self) -> bool:
        return self.record is not None


@dataclass
class Discovery:
    tasks: list[FileTask] = field(default_factory=list)
    skipped: list[dict] = field(default_factory=list)


def discover(
    paths: list[str],
    recursive: bool = True,
    exclude: list[str] | None = None,
    base: str | None = None,
    max_files: int = 100_000,
) -> Discovery:
    """Collect regular files, ignoring symlinks and explicitly excluded paths."""
    excluded = {os.path.realpath(p) for p in (exclude or [])}
    found: Discovery = Discovery()
    seen: set[str] = set()

    def add(file_path: str, rel: str) -> None:
        real = os.path.realpath(file_path)
        if real in excluded or real in seen:
            return
        ext = os.path.splitext(file_path)[1].lower()
        if ext in SKIP_EXTENSIONS:
            found.skipped.append({"path": rel, "reason": "non_media_extension"})
            return
        seen.add(real)
        found.tasks.append(FileTask(file_path, rel))

    for root in paths:
        if os.path.islink(root):
            found.skipped.append({"path": root, "reason": "symlink"})
            continue
        if not os.path.exists(root):
            found.skipped.append({"path": root, "reason": "not_found"})
            continue
        if os.path.isfile(root):
            rel = _relative(root, base)
            add(root, rel)
            continue
        if not os.path.isdir(root):
            found.skipped.append({"path": root, "reason": "not_a_file"})
            continue
        walk_root = root
        for dirpath, dirnames, filenames in os.walk(
            walk_root, followlinks=False
        ):
            dirnames[:] = sorted(
                d
                for d in dirnames
                if not (os.path.islink(os.path.join(dirpath, d)))
            )
            if not recursive:
                dirnames[:] = []
            for name in sorted(filenames):
                full = os.path.join(dirpath, name)
                if os.path.islink(full):
                    found.skipped.append(
                        {"path": _relative(full, base), "reason": "symlink"}
                    )
                    continue
                try:
                    mode = os.lstat(full).st_mode
                except OSError:
                    found.skipped.append(
                        {"path": _relative(full, base), "reason": "stat_failed"}
                    )
                    continue
                if not stat.S_ISREG(mode):
                    found.skipped.append(
                        {"path": _relative(full, base), "reason": "not_regular"}
                    )
                    continue
                add(full, _relative(full, base))
                if len(found.tasks) > max_files:
                    raise ScanFailure(
                        ERR_INTERNAL,
                        f"more than {max_files} files; narrow the scan root",
                        0,
                    )
    found.tasks.sort(key=lambda t: t.rel_path)
    return found


def _relative(path: str, base: str | None) -> str:
    if base:
        try:
            rel = os.path.relpath(path, base)
        except ValueError:  # pragma: no cover - different drives on Windows
            return path
        return rel.replace(os.sep, "/")
    return path


def scan_tasks(tasks: list[FileTask], limits: Limits | None = None) -> list[ScanOutcome]:
    limits = limits or Limits()
    outcomes: list[ScanOutcome] = []
    for task in tasks:
        try:
            record = extract_file(task.path, limits, display_path=task.rel_path)
            outcomes.append(ScanOutcome(task, record=record))
        except ScanFailure as exc:
            outcomes.append(ScanOutcome(task, failure=exc))
        except PermissionError as exc:
            outcomes.append(
                ScanOutcome(task, failure=ScanFailure(ERR_PERMISSION, str(exc), 0))
            )
        except OSError as exc:
            outcomes.append(
                ScanOutcome(
                    task, failure=ScanFailure("ERR_IO", f"cannot read: {exc}", 0)
                )
            )
        except Exception as exc:  # noqa: BLE001 - one file must not kill the run
            outcomes.append(
                ScanOutcome(
                    task,
                    failure=ScanFailure(
                        ERR_INTERNAL, f"internal error: {type(exc).__name__}: {exc}"
                    ),
                )
            )
    return outcomes
