"""Stable, auditable summaries for inspect / remux / verify.

Every command renders the same underlying dictionary either as JSON or as
deterministic plain text. Field names are part of the tool's contract and
must not drift between releases without a version bump.
"""

from __future__ import annotations

from . import ebml
from .ebml import EbmlParser, Element, Limits
from .errors import InputFormatError
from .matroska import (MatroskaFile, SEGMENT_CHILD_IDS,
                       CLUSTER_CHILD_IDS, ID_TRACK_ENTRY, ID_TRACKS,
                       ID_TRACK_NUMBER, ID_CLUSTER, ID_SIMPLE_BLOCK,
                       ID_BLOCK_GROUP, ID_BLOCK)

TREE_MAX_DEPTH = 6


def _element_name(element_id: int) -> str:
    names = {
        ebml.ID_EBML_HEADER: "EBML",
        ebml.ID_SEGMENT: "Segment",
        0x1549A966: "Info",
        0x1654AE6B: "Tracks",
        ID_TRACK_ENTRY: "TrackEntry",
        ID_CLUSTER: "Cluster",
        0x1C53BB6B: "Cues",
        ID_SIMPLE_BLOCK: "SimpleBlock",
        ID_BLOCK_GROUP: "BlockGroup",
        ID_BLOCK: "Block",
        0x1A45DFA3: "EBML",
    }
    return names.get(element_id, f"0x{element_id:X}")


def _walk(parser: EbmlParser, element: Element, depth: int,
          counters: dict, path_ids: frozenset | None) -> dict:
    node = {
        "id": f"0x{element.id:X}",
        "name": _element_name(element.id),
        "offset": element.position,
        "size": element.size,
        "unknown_length": not element.known_length,
    }
    if not element.known_length:
        counters["unknown"] += 1
    counters["count"] += 1
    if depth >= TREE_MAX_DEPTH:
        node["children"] = []
        return node
    if element.known_length:
        try:
            kids = parser.iter_children(element.body_start, element.body_end)
        except InputFormatError:
            node["children"] = []
            return node
    else:
        ids = path_ids or frozenset()
        kids = ebml.scan_unknown_children(parser, element, ids)
    children: list[dict] = []
    nested_ids = (CLUSTER_CHILD_IDS if element.id == ID_CLUSTER
                  else SEGMENT_CHILD_IDS if element.id == ebml.ID_SEGMENT
                  else None)
    for kid in kids:
        if kid.known_length and kid.size == 0:
            counters["count"] += 1
            children.append({"id": f"0x{kid.id:X}",
                             "name": _element_name(kid.id),
                             "offset": kid.position, "size": 0,
                             "unknown_length": False, "children": []})
            continue
        # Only recurse into structural containers; blocks stay leaf nodes.
        if kid.id in (ebml.ID_EBML_HEADER, ebml.ID_SEGMENT, 0x1549A966,
                      ID_TRACKS, ID_TRACK_ENTRY, ID_CLUSTER, 0x1C53BB6B,
                      ID_BLOCK_GROUP):
            children.append(_walk(parser, kid, depth + 1, counters,
                                  nested_ids))
        else:
            counters["count"] += 1
            children.append({"id": f"0x{kid.id:X}",
                             "name": _element_name(kid.id),
                             "offset": kid.position,
                             "size": kid.size,
                             "unknown_length": not kid.known_length,
                             "children": []})
    node["children"] = children
    return node


def element_tree(data: bytes, limits: Limits | None = None) -> dict:
    """Return a bounded EBML element tree and aggregate counters."""
    parser = EbmlParser(data, limits)
    roots = parser.iter_children(0, len(data))
    counters = {"count": 0, "unknown": 0}
    tree = [_walk(parser, root, 1, counters, SEGMENT_CHILD_IDS)
            for root in roots]
    return {
        "tree": tree,
        "element_count": counters["count"],
        "unknown_length_count": counters["unknown"],
    }


def _track_endpoints(mkv: MatroskaFile) -> dict[int, dict]:
    endpoints: dict[int, dict] = {}
    per_track: dict[int, list[int]] = {}
    for cluster in mkv.clusters:
        for block in cluster.blocks:
            per_track.setdefault(block.track_number, []).append(block.pts_ns)
    for number, pts_list in per_track.items():
        endpoints[number] = {
            "first_pts_ms": pts_list[0] / 1_000_000.0,
            "last_pts_ms": pts_list[-1] / 1_000_000.0,
            "block_count": len(pts_list),
        }
    return endpoints


def inspect_summary(mkv: MatroskaFile, tree_info: dict) -> dict:
    endpoints = _track_endpoints(mkv)
    tracks = []
    for track in mkv.tracks:
        endpoint = endpoints.get(track.number,
                                 {"first_pts_ms": None, "last_pts_ms": None,
                                  "block_count": 0})
        tracks.append({
            "track_number": track.number,
            "track_uid": track.uid,
            "track_type": track.type,
            "codec_id": track.codec_id,
            "language": track.language or "und",
            "flag_default": track.flag_default,
            "default_duration_ns": track.default_duration_ns,
            "first_pts_ms": endpoint["first_pts_ms"],
            "last_pts_ms": endpoint["last_pts_ms"],
            "block_count": endpoint["block_count"],
        })
    return {
        "path": mkv.path,
        "doc": "matroska",
        "element_count": tree_info["element_count"],
        "unknown_length_count": tree_info["unknown_length_count"],
        "cues_present": mkv.cues_present,
        "segment_unknown_length": mkv.segment_unknown_length,
        "timestamp_scale_ns": mkv.timestamp_scale_ns,
        "duration_ms": (mkv.duration_ns / 1_000_000.0
                        if mkv.duration_ns is not None else None),
        "tracks": tracks,
        "element_tree": tree_info["tree"],
    }


# --- rendering --------------------------------------------------------------

def _fmt_ms(value) -> str:
    return "n/a" if value is None else f"{value:.3f}"


def render_text(summary: dict) -> str:
    lines = [
        f"file:            {summary['path']}",
        f"element_count:   {summary['element_count']}",
        f"unknown_length:  {summary['unknown_length_count']}",
        f"cues_present:    {summary['cues_present']}",
        f"timescale(ns):   {summary['timestamp_scale_ns']}",
        f"duration_ms:     {_fmt_ms(summary.get('duration_ms'))}",
        "tracks:",
    ]
    for track in summary["tracks"]:
        lines.append(
            f"  #{track['track_number']} uid={track['track_uid']} "
            f"type={track['track_type']} codec={track['codec_id']} "
            f"lang={track['language']} default={track['flag_default']} "
            f"blocks={track['block_count']} "
            f"pts_ms=[{_fmt_ms(track['first_pts_ms'])}.."
            f"{_fmt_ms(track['last_pts_ms'])}]")
    lines.append("element_tree:")
    for node in summary["element_tree"]:
        _render_node(node, 1, lines)
    return "\n".join(lines)


def _render_node(node: dict, depth: int, lines: list[str]) -> None:
    indent = "  " * depth
    size = "unknown" if node["unknown_length"] else str(node["size"])
    lines.append(f"{indent}{node['name']} ({node['id']}) "
                 f"@0x{node['offset']:X} size={size}")
    for kid in node.get("children", []):
        _render_node(kid, depth + 1, lines)


def render_remux_text(result: dict) -> str:
    lines = [
        f"mode:            {result['mode']}",
        f"output:          {result['output']}",
        f"size_bytes:      {result['size']}",
        f"segments:        {result['segments']}",
        f"co64:            {result['co64']}",
        f"element_count:   {result['element_count']}",
        f"unknown_length:  {result['unknown_length_count']}",
        "track_map:",
    ]
    for entry in result["track_map"]:
        lines.append(
            f"  mkv#{entry['source_track_number']} -> "
            f"mp4#{entry['mp4_track_id']} {entry['sample_entry']} "
            f"({entry['handler_type']}) timescale={entry['timescale']} "
            f"samples={entry['sample_count']} "
            f"rewritten_pts_ms=["
            f"{entry['rewritten_first_pts_ms']:.3f}.."
            f"{entry['rewritten_last_pts_ms']:.3f}]")
    verification = result["verification"]
    lines.append(
        f"verify:          index_valid={verification['index_valid']} "
        f"sample_count={verification['sample_count']}")
    return "\n".join(lines)


def render_verify_text(result: dict) -> str:
    lines = [
        f"index_valid:     {result['index_valid']}",
        f"track_count:     {result['track_count']}",
        f"sample_count:    {result['sample_count']}",
        f"mdat_size:       {result['mdat_size']}",
        "tracks:",
    ]
    for track in result["tracks"]:
        lines.append(
            f"  mp4#{track['mp4_track_id']} {track['handler_type']} "
            f"timescale={track['timescale']} samples={track['sample_count']} "
            f"span=0x{track['offset_start']:X}..0x{track['offset_end']:X} "
            f"pts_ms=[{track['first_pts_ms']:.3f}.."
            f"{track['last_pts_ms']:.3f}]")
    if "manifest" in result:
        lines.append("manifest:        present and consistent")
    return "\n".join(lines)
