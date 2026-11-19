"""Terminal and JSON reporting.

The terminal view leads with the counters required by the delivery check:
files_scanned, tracks_total, duration deltas, codec_summary and the
inconsistency list. JSON output carries the same data plus full detail.
"""

from __future__ import annotations

import json
import os
from pathlib import Path
from typing import Any, TextIO


class ReportError(Exception):
    def __init__(self, message: str, *, path: str | None = None):
        super().__init__(message)
        self.message = message
        self.path = path

    def to_dict(self) -> dict:
        return {"path": self.path, "code": "report_error", "message": self.message}


def _fmt(value: Any) -> str:
    if value is None:
        return "null"
    if isinstance(value, float):
        return f"{value:.3f}"
    if isinstance(value, (list, tuple)):
        return "[" + ", ".join(_fmt(v) for v in value) + "]"
    return str(value)


def render_scan(summary: dict, stream: TextIO) -> None:
    stream.write(f"scan: {summary.get('root', '?')}\n")
    stream.write(f"  files_scanned: {summary.get('files_scanned', 0)}\n")
    stream.write(f"  files_ok:      {summary.get('files_ok', 0)}\n")
    stream.write(f"  files_failed:  {summary.get('files_failed', 0)}\n")
    stream.write(f"  tracks_total:  {summary.get('tracks_total', 0)}\n")
    stream.write(f"  codec_summary: {_fmt(summary.get('codec_summary', []))}\n")
    stream.write(f"  duration_delta_seconds: {_fmt(summary.get('duration_delta_seconds'))}\n")
    stream.write(f"  duration_delta_ratio:   {_fmt(summary.get('duration_delta_ratio'))}\n")
    stream.write(f"  inconsistency_count:    {summary.get('inconsistency_count', 0)}\n")
    for item in summary.get("media", []):
        stream.write(
            f"  ok  {_display_path(item):<40} {item.get('container', '?'):>4} "
            f"{item.get('track_count', 0)} track(s) "
            f"{_fmt(item.get('duration_seconds'))}s "
            f"{_fmt(item.get('codec_summary', []))}\n")
    failures = summary.get("failures", [])
    if failures:
        stream.write("  failed files:\n")
        for failure in failures:
            where = ""
            if failure.get("offset") is not None:
                where = f" @byte {failure['offset']}"
            box = f" box={failure['box_path']}" if failure.get("box_path") else ""
            stream.write(f"  ERR {failure.get('path', '?')}: "
                         f"{failure.get('code', 'error')}: "
                         f"{failure.get('message', '')}{where}{box}\n")
    issues = summary.get("issues", [])
    if issues:
        stream.write("  inconsistencies:\n")
        for issue in issues:
            stream.write(_issue_line(issue))
    else:
        stream.write("  inconsistencies: none\n")


def render_compare(result: dict, stream: TextIO) -> None:
    stream.write(f"compare: {result.get('reference', {}).get('label', '?')} "
                 f"-> {result.get('target', {}).get('label', '?')}\n")
    stream.write(f"  files_compared: {result.get('files_compared', 0)}\n")
    stream.write(f"  tracks_compared: {len(result.get('pairs', []))}\n")
    stream.write(f"  duration_delta_seconds: {_fmt(result.get('duration_delta_seconds'))}\n")
    stream.write(f"  duration_delta_ratio:   {_fmt(result.get('duration_delta_ratio'))}\n")
    stream.write(f"  codec_summary: {_fmt(_codec_summary(result))}\n")
    counts = result.get("counts", {})
    stream.write(f"  inconsistency_count: {result.get('inconsistency_count', 0)} "
                 f"(error={counts.get('error', 0)}, "
                 f"warning={counts.get('warning', 0)}, "
                 f"info={counts.get('info', 0)})\n")
    issues = result.get("issues", [])
    if issues:
        stream.write("  inconsistencies:\n")
        for issue in issues:
            stream.write(_issue_line(issue))
    else:
        stream.write("  inconsistencies: none\n")
    stream.write(f"  result: {'OK' if result.get('ok') else 'HARD_INCONSISTENCIES'}\n")


def _codec_summary(result: dict) -> list[str]:
    """Codes compared in this run, read straight from the spans metadata."""
    return list(result.get("codec_summary", []))


def _issue_line(issue: dict) -> str:
    scope = issue.get("scope", "")
    path = issue.get("path") or ""
    location = f"{scope}:{path}" if path else scope
    return (f"  [{issue.get('severity', '?').upper():<7}] "
            f"{issue.get('code', '?')} ({location}) {issue.get('message', '')}\n")


def _display_path(item: dict) -> str:
    return item.get("relative_path") or item.get("name") or item.get("path") or "?"


def write_json(payload: dict, out: str | Path) -> Path:
    """Write JSON atomically; raise ReportError with a diagnostic on failure."""
    target = Path(out)
    tmp = target.with_name(target.name + ".tmp")
    try:
        target.parent.mkdir(parents=True, exist_ok=True)
        text = json.dumps(payload, ensure_ascii=False, indent=2) + "\n"
        with open(tmp, "w", encoding="utf-8") as handle:
            handle.write(text)
            handle.flush()
            os.fsync(handle.fileno())
        os.replace(tmp, target)
    except OSError as exc:
        try:
            tmp.unlink(missing_ok=True)
        except OSError:
            pass
        raise ReportError(f"cannot write JSON to {target}: {exc}",
                          path=str(target)) from None
    return target


# A/V drift beyond this is reported as a scan-time hard inconsistency.
SCAN_AV_TOLERANCE_SECONDS = 0.5
SCAN_AV_TOLERANCE_RATIO = 0.02


def _av_drift(media: dict) -> tuple[float | None, float | None, float | None]:
    """Difference between the longest audio and longest video track."""
    audio = [t["duration_seconds"] for t in media.get("tracks", [])
             if t.get("kind") == "audio" and t.get("duration_seconds") is not None]
    video = [t["duration_seconds"] for t in media.get("tracks", [])
             if t.get("kind") == "video" and t.get("duration_seconds") is not None]
    if not audio or not video:
        return None, None, None
    a, v = max(audio), max(video)
    delta = a - v
    ratio = (delta / v) if v else None
    return delta, ratio, v


def _av_drift_exceeds(delta: float, duration: float | None,
                      tolerance_seconds: float = SCAN_AV_TOLERANCE_SECONDS
                      ) -> bool:
    allowed = tolerance_seconds
    if duration:
        allowed = max(allowed, SCAN_AV_TOLERANCE_RATIO * abs(duration))
    return abs(delta) > allowed + 1e-9


def build_scan_summary(root: str, summaries: list[dict], failures: list[dict],
                       batch: dict | None = None,
                       av_tolerance_seconds: float = SCAN_AV_TOLERANCE_SECONDS
                       ) -> dict:
    """Assemble the scan result document (terminal keys + JSON detail).

    ``duration_delta_seconds``/``_ratio`` report the worst audio-vs-video
    duration drift within a single media file, since scan has no second
    package to compare against.
    """
    tracks_total = sum(s.get("track_count", 0) for s in summaries)
    durations = [s.get("duration_seconds") for s in summaries
                 if s.get("duration_seconds") is not None]
    codecs = sorted({c for s in summaries for c in s.get("codec_summary", [])})
    issues = []
    worst_delta = 0.0
    worst_ratio = 0.0
    for s in summaries:
        delta, ratio, video_duration = _av_drift(s)
        if delta is not None and abs(delta) > abs(worst_delta):
            worst_delta = delta
            worst_ratio = ratio or 0.0
        if not any(t.get("kind") == "audio" for t in s.get("tracks", [])) \
                and any(t.get("kind") == "video" for t in s.get("tracks", [])):
            issues.append({
                "severity": "warning",
                "code": "no_audio_track",
                "scope": "file",
                "path": s.get("name"),
                "message": "file has video but no audio track",
                "detail": {"track_kinds": s.get("track_kinds", [])},
            })
        if delta is not None and _av_drift_exceeds(delta, video_duration,
                                                   av_tolerance_seconds):
            issues.append({
                "severity": "error",
                "code": "av_duration_mismatch",
                "scope": "file",
                "path": s.get("name"),
                "message": f"audio/video durations differ by {delta:+.3f}s "
                           f"({(ratio or 0.0) * 100:+.2f}%)",
                "detail": {"delta_seconds": round(delta, 6),
                           "delta_ratio": round(ratio, 6) if ratio else None},
            })
        for track in s.get("tracks", []):
            missing = sorted(track.get("reasons", {}))
            if missing:
                issues.append({
                    "severity": "warning",
                    "code": "missing_metadata",
                    "scope": "track",
                    "path": s.get("name"),
                    "message": f"track {track.get('track_id')} has null fields: "
                               f"{', '.join(missing)}",
                    "detail": {"fields": missing},
                })
        for warning in s.get("warnings", []):
            issues.append({
                "severity": "info",
                "code": warning.get("code", "warning"),
                "scope": "file",
                "path": s.get("name"),
                "message": warning.get("message", ""),
                "detail": {"offset": warning.get("offset"),
                           "box_path": warning.get("box_path")},
            })
    for failure in failures:
        issues.append({
            "severity": "error",
            "code": failure.get("code", "error"),
            "scope": "file",
            "path": failure.get("path"),
            "message": failure.get("message", ""),
            "detail": {"offset": failure.get("offset"),
                       "box_path": failure.get("box_path")},
        })
    hard = sum(1 for i in issues if i["severity"] == "error")
    return {
        "kind": "mediacheck.scan",
        "root": root,
        "batch": batch,
        "files_scanned": len(summaries) + len(failures),
        "files_ok": len(summaries),
        "files_failed": len(failures),
        "tracks_total": tracks_total,
        "duration_seconds_max": round(max(durations), 6) if durations else None,
        "duration_delta_seconds": round(worst_delta, 6),
        "duration_delta_ratio": round(worst_ratio, 6),
        "codec_summary": codecs,
        "inconsistency_count": len(issues),
        "hard_inconsistency_count": hard,
        "media": summaries,
        "failures": failures,
        "issues": issues,
    }
