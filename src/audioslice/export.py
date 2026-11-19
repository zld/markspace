"""JSONL export and terminal summary rendering.

Each input file becomes one JSON object per line: successful files carry
their activity segment coordinates and total silence duration; failed files
carry a ``failures`` list instead. The export target must never be an input
WAV and must be writable; either problem raises :class:`ExportError` with a
clear, user-facing message rather than an OSError traceback.
"""

from __future__ import annotations

import json
import os
from dataclasses import dataclass, field
from pathlib import Path
from typing import Sequence


class ExportError(RuntimeError):
    """Raised when an export target is invalid or cannot be written."""


@dataclass
class FileExport:
    """Serializable per-file result handed to the exporter."""

    path: str
    status: str  # "ok", "failed", or "skipped"
    sha256: str = ""
    sample_rate: int | None = None
    channels: int | None = None
    n_samples: int | None = None
    duration_seconds: float = 0.0
    silence_total_seconds: float = 0.0
    segments: list[dict] = field(default_factory=list)
    failures: list[str] = field(default_factory=list)

    def to_json(self) -> dict:
        data = {
            "path": self.path,
            "status": self.status,
            "sha256": self.sha256,
            "sample_rate": self.sample_rate,
            "channels": self.channels,
            "n_samples": self.n_samples,
            "duration_seconds": round(float(self.duration_seconds), 6),
            "silence_total_seconds": round(float(self.silence_total_seconds), 6),
            "segments": self.segments,
            "failures": self.failures,
        }
        return data


def validate_export_target(export_path: str, input_paths: Sequence[str]) -> None:
    """Reject targets that point at input WAVs or live in unwritable places."""
    target = Path(export_path).expanduser()
    try:
        target_resolved = target.resolve(strict=False)
    except OSError as exc:
        raise ExportError(f"invalid export path {export_path!r}: {exc}") from exc

    for raw in input_paths:
        try:
            input_resolved = Path(raw).resolve(strict=False)
        except OSError:
            continue
        if target_resolved == input_resolved:
            raise ExportError(
                f"export path {export_path!r} would overwrite input WAV {raw!r}; "
                "choose a separate .jsonl file"
            )

    parent = target_resolved.parent
    if not parent.is_dir():
        raise ExportError(f"export directory does not exist: {parent}")
    if not os.access(parent, os.W_OK):
        raise ExportError(f"export directory is not writable: {parent}")
    if target_resolved.exists() and not os.access(target_resolved, os.W_OK):
        raise ExportError(f"export file is not writable: {target_resolved}")


def export_jsonl(export_path: str, records: Sequence[FileExport],
                 input_paths: Sequence[str]) -> None:
    """Write one JSON object per line; atomically replace via temp file."""
    validate_export_target(export_path, input_paths)
    target = Path(export_path)
    tmp = target.with_name(target.name + ".tmp")
    try:
        with open(tmp, "w", encoding="utf-8") as fh:
            for rec in records:
                fh.write(json.dumps(rec.to_json(), ensure_ascii=False, sort_keys=True))
                fh.write("\n")
            fh.flush()
            os.fsync(fh.fileno())
        os.replace(tmp, target)
    except OSError as exc:
        try:
            tmp.unlink(missing_ok=True)
        except OSError:
            pass
        raise ExportError(f"failed writing export {export_path!r}: {exc}") from exc


def render_summary(records: Sequence[FileExport], db_path: str,
                   export_path: str | None) -> str:
    """Build the human-readable terminal summary."""
    ok = [r for r in records if r.status == "ok"]
    failed = [r for r in records if r.status == "failed"]
    skipped = [r for r in records if r.status == "skipped"]
    n_segments = sum(len(r.segments) for r in ok)

    lines = [
        f"files_ok: {len(ok)}",
        f"files_failed: {len(failed)}",
        f"files_skipped: {len(skipped)}",
        f"segments: {n_segments}",
        f"database: {db_path}",
    ]
    if export_path:
        lines.append(f"export: {export_path}")
    for rec in records:
        if rec.status != "ok":
            lines.append(f"- {rec.path}: {rec.status} ({'; '.join(rec.failures) or 'n/a'})")
            continue
        lines.append(f"- {rec.path}: {len(rec.segments)} segments, "
                     f"silence_total_seconds={rec.silence_total_seconds:.6f}")
        for idx, seg in enumerate(rec.segments):
            lines.append(
                f"    [{idx}] start_sample={seg['start_sample']} "
                f"end_sample={seg['end_sample']} kind={seg['kind']} reason={seg['reason']}"
            )
    return "\n".join(lines)
