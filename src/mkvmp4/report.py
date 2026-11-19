"""Stable, machine-readable summaries for inspect / remux / inspect-mp4.

Every report is an ordered ``dict`` (built in a fixed key order) so JSON
output is byte-stable across runs. Seconds are rendered with at least six
fractional digits.
"""

from __future__ import annotations

from .mapping import TrackMapping
from .matroska import MatroskaFile
from .mux import MuxResult
from .timeline import TrackTimeline

SECONDS_FMT = "{:.6f}"


def _seconds(value: float) -> str:
    return SECONDS_FMT.format(value)


def element_tree_summary(tree: list[dict]) -> list[dict]:
    return [
        {"name": node["name"], "depth": node["depth"], "count": node["count"]}
        for node in tree
    ]


def mapping_report(mapping: TrackMapping) -> list[dict]:
    return [
        {
            "mkv_track_number": e.mkv_track_number,
            "mp4_track_id": e.mp4_track_id,
            "decision": e.decision,
            "reason": e.reason,
            "codec_id": e.codec_id,
            "track_type": e.track_type,
        }
        for e in mapping.entries
    ]


def track_timeline_report(
    mapping: TrackMapping, timelines: dict[int, TrackTimeline]
) -> list[dict]:
    rows: list[dict] = []
    for number in mapping.accepted_numbers:
        tl = timelines[number]
        rows.append({
            "mkv_track_number": number,
            "mp4_track_id": mapping.accepted[number],
            "timescale": tl.timescale,
            "sample_count": tl.sample_count,
            "first_timestamp_seconds": _seconds(tl.first_seconds),
            "last_timestamp_seconds": _seconds(tl.last_seconds),
            "end_timestamp_seconds": _seconds(
                (tl.sample_times[-1] + tl.sample_durations[-1])
                / tl.timescale),
            "last_duration_estimated": tl.last_duration_estimated,
        })
    return rows


def build_mkv_report(
    mkv: MatroskaFile,
    mapping: TrackMapping,
    timelines: dict[int, TrackTimeline],
    shift_ns: int,
    extra_warnings: list[str],
    mux: MuxResult | None = None,
) -> dict:
    """Assemble the canonical inspect/remux JSON object (fixed key order)."""
    warnings = list(mkv.warnings)
    for tl in timelines.values():
        warnings.extend(tl.warnings)
    warnings.extend(extra_warnings)
    report = {
        "status": "ok",
        "element_tree": element_tree_summary(mkv.tree),
        "element_count": sum(node["count"] for node in mkv.tree),
        "segment": {
            "cluster_count": mkv.cluster_count,
            "cue_count": mkv.cue_count,
            "timecode_scale_ns": mkv.timecode_scale_ns,
            "duration_ns": mkv.duration_ns,
        },
        "track_mapping": mapping_report(mapping),
        "skipped_tracks": [
            {
                "mkv_track_number": e.mkv_track_number,
                "reason": e.reason,
                "codec_id": e.codec_id,
            }
            for e in mapping.skipped
        ],
        "tracks": track_timeline_report(mapping, timelines),
        "timestamp_shift_ns": shift_ns,
        "warning_count": len(warnings),
        "warnings": warnings,
    }
    if mux is not None:
        report["mp4"] = {
            "box_count": mux.total_boxes,
            "movie_timescale": mux.movie_timescale,
            "movie_duration_units": mux.movie_duration_units,
            "mdat_data_start": mux.mdat_data_start,
            "mdat_data_end": mux.mdat_data_end,
            "uses_co64": any(t.uses_co64 for t in mux.muxed_tracks),
            "tracks": [
                {
                    "mp4_track_id": t.mp4_track_id,
                    "mkv_track_number": t.mkv_track_number,
                    "sample_count": t.sample_count,
                    "first_timestamp_seconds": _seconds(t.first_seconds),
                    "last_timestamp_seconds": _seconds(t.last_seconds),
                }
                for t in mux.muxed_tracks
            ],
        }
    return report
