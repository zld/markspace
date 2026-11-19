"""Terminal summary and atomic JSON output.

Both formatters consume the same :class:`~frameaudit.model.AuditStats`
instance produced by :func:`frameaudit.pipeline.run_audit`, guaranteeing that
the human-readable summary and the JSON report always agree.
"""

from __future__ import annotations

import json
import os
import tempfile
from pathlib import Path
from typing import Optional

from .errors import OutputWriteError
from .model import AuditStats


def render_summary(stats: AuditStats) -> str:
    """Render the single-line-per-metric terminal summary."""
    frame_types = ",".join(
        f"{label}:{stats.frame_type_counts.get(label, 0)}"
        for label in stats.frame_type_counts
    )
    lines = [
        f"input={stats.input_path}",
        f"container={stats.container_format} codec={stats.video_codec}",
        f"time_base={stats.time_base} average_rate={stats.average_rate}",
        f"decoded_frames={stats.decoded_frames}",
        f"frame_types={{{frame_types}}}",
        f"repaired_timestamps={stats.repaired_timestamps}",
        f"max_jump_seconds={stats.max_jump_seconds:.4f}",
        f"reorder_overflows={stats.reorder_overflows}",
    ]
    return os.linesep.join(lines)


def build_report(stats: AuditStats) -> dict:
    """Return the JSON-serializable report shared by all file/stream writes."""
    return stats.to_dict()


def write_json_report(stats: AuditStats, output_path: str | os.PathLike[str]) -> None:
    """Write the report via a temp file followed by an atomic replace.

    Any IO failure is raised as :class:`OutputWriteError`; partial files are
    removed and success is never reported when the final replace fails.
    """
    target = Path(output_path)
    if target.parent and not target.parent.exists():
        raise OutputWriteError(
            f"JSON output directory does not exist: {target.parent}"
        )
    report = build_report(stats)
    tmp_path: Optional[Path] = None
    try:
        with tempfile.NamedTemporaryFile(
            mode="w",
            encoding="utf-8",
            dir=target.parent or Path("."),
            prefix=f".{target.name}.",
            suffix=".tmp",
            delete=False,
        ) as handle:
            json.dump(report, handle, indent=2, sort_keys=False)
            handle.write("\n")
            handle.flush()
            os.fsync(handle.fileno())
            tmp_path = Path(handle.name)
        os.replace(tmp_path, target)
    except OSError as exc:
        if tmp_path is not None:
            try:
                tmp_path.unlink()
            except OSError:
                pass
        raise OutputWriteError(
            f"failed to write JSON report to {target}: {exc}"
        ) from exc
