"""Element-tree, track-mapping and timestamp reporting."""

from __future__ import annotations

import json
from dataclasses import asdict, dataclass

from .ebml import Element, count_elements, element_name
from .timeline import NormalizedTrack


@dataclass
class TrackReport:
    input_track: int
    output_track: int
    type: str
    codec_id: str
    sample_count: int
    first_dts_ns: int
    last_dts_ns: int
    first_pts_ns: int
    last_pts_ns: int
    timescale: int
    warnings: list[str]
    interval_stats: dict[str, float]


def element_tree_summary(roots: list[Element], max_depth: int = 2
                         ) -> list[dict]:
    """Produce a compact depth-limited summary of the EBML element tree."""
    summary: list[dict] = []

    def walk(elements: list[Element], depth: int, path: str) -> None:
        for element in elements:
            node_path = f"{path}/{element.name}"
            entry = {
                "name": element.name,
                "id": f"0x{element.element_id:X}",
                "offset": element.start,
                "size": element.size,
                "unknown_length": element.size is None,
                "path": node_path,
            }
            summary.append(entry)
            if depth < max_depth:
                walk(element.children, depth + 1, node_path)

    walk(roots, 0, "")
    return summary


def build_report(roots: list[Element], tracks: list[NormalizedTrack],
                 index_entries: list[dict], output_path: str) -> dict:
    report = {
        "output": output_path,
        "elements": count_elements(roots),
        "tracks": len(tracks),
        "samples": sum(len(t.samples) for t in tracks),
        "index_entries": len(index_entries),
        "element_tree": element_tree_summary(roots),
        "track_mapping": [],
        "timestamps": [],
        "warnings": {},
    }
    for track in tracks:
        mapping = {
            "input_track": track.input_number,
            "output_track": track.output_number,
            "type": track.kind_name,
            "codec_id": track.codec_id,
        }
        report["track_mapping"].append(mapping)
        report["timestamps"].append(asdict(TrackReport(
            track.input_number, track.output_number, track.kind_name,
            track.codec_id, len(track.samples),
            track.first_dts_ns, track.last_dts_ns,
            track.first_pts_ns, track.last_pts_ns, track.timescale,
            track.warnings, track.interval_stats())))
        if track.warnings:
            report["warnings"][str(track.input_number)] = track.warnings
    return report


def render_text(report: dict) -> str:
    lines = [
        f"elements={report['elements']}",
        f"tracks={report['tracks']}",
        f"samples={report['samples']}",
        f"index_entries={report['index_entries']}",
        "",
        "track mapping (input -> output):",
    ]
    for mapping in report["track_mapping"]:
        lines.append(
            f"  in={mapping['input_track']} -> out={mapping['output_track']}"
            f"  type={mapping['type']}  codec={mapping['codec_id']}")
    lines.append("")
    lines.append("rewritten timestamps (integer nanoseconds):")
    for ts in report["timestamps"]:
        lines.append(
            f"  track in={ts['input_track']} out={ts['output_track']} "
            f"({ts['type']}): PTS {ts['first_pts_ns']}..{ts['last_pts_ns']} "
            f"DTS {ts['first_dts_ns']}..{ts['last_dts_ns']} "
            f"samples={ts['sample_count']}")
        for warning in ts["warnings"]:
            lines.append(f"    ! {warning}")
    return "\n".join(lines)


def write_json_report(report: dict, path: str) -> None:
    with open(path, "w", encoding="utf-8") as handle:
        json.dump(report, handle, indent=2, sort_keys=True)
        handle.write("\n")


__all__ = ["element_name", "build_report", "render_text",
           "write_json_report"]
