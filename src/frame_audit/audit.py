"""Everything the CLI needs to audit one file, in one place.

The pipeline is deliberately linear and shared:

    probe -> decode -> build_timeline -> AuditResult -> sink

Both output sinks (terminal table and JSON Lines) render the same ``AuditResult``
object, so the two cannot diverge on counts or repaired timestamps.
"""

from __future__ import annotations

from pathlib import Path
from typing import Sequence

from .decode import decode_frames
from .errors import AuditError, DecodeError, InputError
from .model import AuditResult, Policy
from .probe import probe
from .timeline import build_timeline

__all__ = ["audit_file", "audit_many", "DISPLAY_ROOT"]


#: Directory used to derive the relative names shown in reports.  Set by the
#: CLI when it scans a directory; ``None`` keeps the display path as given.
DISPLAY_ROOT: Path | None = None


def _display_path(path: Path) -> str:
    root = DISPLAY_ROOT
    if root is not None:
        try:
            return str(path.resolve().relative_to(root.resolve()))
        except ValueError:
            pass
    return str(path)


def audit_file(
    path: str | Path,
    *,
    max_frames: int | None = None,
    policy: Policy = Policy.REPAIR,
    display_path: str | None = None,
) -> AuditResult:
    """Audit a single file, converting every failure into a result.

    A failure is recorded on the returned result (``error``/``error_reason``)
    instead of raising, so one bad file in a batch cannot erase the others.
    """
    source = Path(path)
    shown = display_path if display_path is not None else _display_path(source)

    try:
        info = probe(source)
    except AuditError as exc:
        return AuditResult(
            path=str(source),
            display_path=shown,
            stream=None,
            max_frames=max_frames,
            policy=policy,
            error=exc.message,
            error_reason=exc.reason,
        )

    try:
        outcome = decode_frames(source, time_base=info.time_base, max_frames=max_frames)
    except AuditError as exc:
        return AuditResult(
            path=str(source),
            display_path=shown,
            stream=info,
            max_frames=max_frames,
            policy=policy,
            error=exc.message,
            error_reason=exc.reason,
        )

    timeline = build_timeline(
        outcome.frames,
        time_base=info.time_base,
        average_rate=info.average_rate,
        policy=policy,
    )

    warnings: list[str] = []
    if outcome.truncated:
        warnings.append(
            f"decode stopped at the {max_frames}-frame limit; this is a partial scan"
        )
    if outcome.decode_errors:
        warnings.append(
            f"{len(outcome.decode_errors)} packet(s) failed to decode"
        )
    if not outcome.frames:
        warnings.append("no frames decoded")

    error = None
    reason = None
    if not outcome.frames and outcome.decode_errors:
        error = "stream produced no decodable frames"
        reason = "decode_failed"

    return AuditResult(
        path=str(source),
        display_path=shown,
        stream=info,
        frames=timeline.frames,
        decoded_frames=timeline.count,
        truncated=outcome.truncated,
        max_frames=max_frames,
        policy=policy,
        decode_errors=tuple(outcome.decode_errors),
        warnings=tuple(warnings),
        error=error,
        error_reason=reason,
    )


def audit_many(
    paths: Sequence[str | Path],
    *,
    max_frames: int | None = None,
    policy: Policy = Policy.REPAIR,
) -> list[AuditResult]:
    """Audit several files; a failure in one never suppresses the others."""
    results: list[AuditResult] = []
    for path in paths:
        results.append(audit_file(path, max_frames=max_frames, policy=policy))
    return results
