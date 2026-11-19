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
            "media_start_units": tl.media_start_units,
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


# --------------------------------------------------------------------------
# Independent MP4 inspection (re-parses the produced file)
# --------------------------------------------------------------------------

from dataclasses import dataclass  # noqa: E402

_MP4_CONTAINERS = {
    b"moov", b"trak", b"mdia", b"minf", b"stbl", b"edts", b"dinf", b"udta",
}


class Mp4InspectionError(ValueError):
    """Raised when the MP4 cannot be parsed for inspection."""


@dataclass(slots=True)
class _BoxNode:
    box_type: bytes
    start: int
    payload_start: int
    end: int
    depth: int
    children: list["_BoxNode"]


def _parse_mp4_boxes(stream, end: int, depth: int) -> list[_BoxNode]:
    nodes: list[_BoxNode] = []
    while stream.tell() < end:
        start = stream.tell()
        header = stream.read(8)
        if len(header) < 8:
            raise Mp4InspectionError("truncated MP4 box header")
        size = int.from_bytes(header[:4], "big")
        box_type = header[4:8]
        header_len = 8
        if size == 1:
            size = int.from_bytes(stream.read(8), "big")
            header_len = 16
        elif size == 0:
            size = end - start
        if size < header_len or start + size > end:
            raise Mp4InspectionError(
                f"invalid box {box_type!r} size {size} at {start}")
        payload_start = start + header_len
        payload_end = start + size
        children: list[_BoxNode] = []
        if box_type in _MP4_CONTAINERS:
            stream.seek(payload_start)
            children = _parse_mp4_boxes(stream, payload_end, depth + 1)
        stream.seek(payload_end)
        nodes.append(_BoxNode(box_type, start, payload_start,
                              payload_end, depth, children))
    return nodes


def _find(node: _BoxNode, box_type: bytes) -> _BoxNode | None:
    for child in node.children:
        if child.box_type == box_type:
            return child
    return None


def _find_all(node: _BoxNode, box_type: bytes) -> list[_BoxNode]:
    return [c for c in node.children if c.box_type == box_type]


def _read_payload(stream, node: _BoxNode) -> bytes:
    stream.seek(node.payload_start)
    return stream.read(node.end - node.payload_start)


def _parse_mdhd(payload: bytes) -> tuple[int, int]:
    # Payload starts after the 4-byte fullbox version/flags header.
    version = payload[0]
    if version == 1:
        # creation(8) + modification(8), then timescale and duration
        timescale = int.from_bytes(payload[20:24], "big")
        duration = int.from_bytes(payload[24:32], "big")
    else:
        # creation(4) + modification(4), then timescale and duration
        timescale = int.from_bytes(payload[12:16], "big")
        duration = int.from_bytes(payload[16:20], "big")
    return timescale, duration


def _parse_stts(payload: bytes) -> list[tuple[int, int]]:
    count = int.from_bytes(payload[4:8], "big")
    runs: list[tuple[int, int]] = []
    pos = 8
    for _ in range(count):
        runs.append((int.from_bytes(payload[pos:pos + 4], "big"),
                     int.from_bytes(payload[pos + 4:pos + 8], "big")))
        pos += 8
    return runs


def _sample_times(runs: list[tuple[int, int]], count: int) -> list[int]:
    times: list[int] = []
    clock = 0
    produced = 0
    for run_count, delta in runs:
        for _ in range(run_count):
            if produced >= count:
                break
            times.append(clock)
            clock += delta
            produced += 1
    return times


def inspect_mp4(stream) -> dict:
    stream.seek(0, 2)
    file_size = stream.tell()
    stream.seek(0)
    roots = _parse_mp4_boxes(stream, file_size, 0)
    if stream.tell() != file_size:
        raise Mp4InspectionError("MP4 boxes do not cover the whole file")

    flat: list[_BoxNode] = []
    outline: list[dict] = []

    def flatten(nodes: list[_BoxNode]) -> None:
        for node in nodes:
            flat.append(node)
            name = node.box_type.decode("latin-1")
            if outline and outline[-1]["name"] == name \
                    and outline[-1]["depth"] == node.depth:
                outline[-1]["count"] += 1
            else:
                outline.append({"name": name, "depth": node.depth,
                                "count": 1})
            flatten(node.children)

    flatten(roots)
    root_types = [n.box_type for n in roots]
    if b"ftyp" not in root_types or b"moov" not in root_types:
        raise Mp4InspectionError("MP4 missing top-level ftyp/moov")
    moov = next(n for n in roots if n.box_type == b"moov")
    mvhd = _find(moov, b"mvhd")
    if mvhd is None:
        raise Mp4InspectionError("moov missing mvhd")
    mvhd_payload = _read_payload(stream, mvhd)
    movie_ts, movie_dur = _parse_mdhd(mvhd_payload)

    track_reports: list[dict] = []
    for trak in _find_all(moov, b"trak"):
        tkhd = _find(trak, b"tkhd")
        mdia = _find(trak, b"mdia")
        if tkhd is None or mdia is None:
            raise Mp4InspectionError("trak missing tkhd/mdia")
        tkhd_payload = _read_payload(stream, tkhd)
        version = tkhd_payload[0]
        if version == 1:
            # after fullbox(4) + creation(8) + modification(8)
            track_id = int.from_bytes(tkhd_payload[20:24], "big")
        else:
            # after fullbox(4) + creation(4) + modification(4)
            track_id = int.from_bytes(tkhd_payload[12:16], "big")
        mdhd = _find(mdia, b"mdhd")
        hdlr = _find(mdia, b"hdlr")
        minf = _find(mdia, b"minf")
        if None in (mdhd, hdlr, minf):
            raise Mp4InspectionError("trak missing mdhd/hdlr/minf")
        timescale, mdhd_duration = _parse_mdhd(_read_payload(stream, mdhd))
        handler = _read_payload(stream, hdlr)[8:12].decode("latin-1")
        stbl = _find(minf, b"stbl")
        if stbl is None:
            raise Mp4InspectionError("minf missing stbl")
        stts_node = _find(stbl, b"stts")
        stsz_node = _find(stbl, b"stsz")
        stsc_node = _find(stbl, b"stsc")
        if None in (stts_node, stsz_node, stsc_node):
            raise Mp4InspectionError("stbl missing required sample table")
        stts_payload = _read_payload(stream, stts_node)
        stsz_payload = _read_payload(stream, stsz_node)
        sample_size = int.from_bytes(stsz_payload[4:8], "big")
        sample_count = int.from_bytes(stsz_payload[8:12], "big")
        if sample_size == 0 and len(stsz_payload) != 12 + 4 * sample_count:
            raise Mp4InspectionError("stsz size array length mismatch")
        runs = _parse_stts(stts_payload)
        media_times = _sample_times(runs, sample_count)
        # An optional edit list records the original first-sample media time
        # that per-track zeroing moved out of the sample table.
        edts = _find(trak, b"edts")
        media_start = 0
        if edts is not None:
            elst = _find(edts, b"elst")
            if elst is not None:
                elst_payload = _read_payload(stream, elst)
                elst_version = elst_payload[0]
                if elst_version == 1:
                    # fullbox(4) + entry_count(4) + segment_duration(8)
                    # + media_time(8) -> media_time starts at offset 16
                    media_start = int.from_bytes(
                        elst_payload[16:24], "big", signed=True)
                else:
                    # fullbox(4) + entry_count(4) + segment_duration(4)
                    # + media_time(4) -> media_time starts at offset 12
                    media_start = int.from_bytes(
                        elst_payload[12:16], "big", signed=True)
        stco = _find(stbl, b"stco")
        co64 = _find(stbl, b"co64")
        if (stco is None) == (co64 is None):
            raise Mp4InspectionError("exactly one of stco/co64 is required")
        offset_node = co64 if co64 is not None else stco
        offset_payload = _read_payload(stream, offset_node)
        chunk_count = int.from_bytes(offset_payload[4:8], "big")
        stss = _find(stbl, b"stss")
        sync_count = 0
        if stss is not None:
            sync_count = int.from_bytes(
                _read_payload(stream, stss)[4:8], "big")
        # Sample tables are zero-based per track; movie time equals media time.
        first_s = (media_times[0] if media_times else 0) / timescale
        last_s = (media_times[-1] if media_times else 0) / timescale
        end_s = mdhd_duration / timescale
        track_reports.append({
            "mp4_track_id": track_id,
            "handler": handler,
            "timescale": timescale,
            "sample_count": sample_count,
            "chunk_count": chunk_count,
            "uses_co64": co64 is not None,
            "sync_sample_count": sync_count,
            "edit_list_media_start_units": max(0, media_start),
            "first_timestamp_seconds": _seconds(first_s),
            "last_timestamp_seconds": _seconds(last_s),
            "end_timestamp_seconds": _seconds(end_s),
        })

    return {
        "status": "ok",
        "file_size": file_size,
        "box_count": len(flat),
        "box_tree": outline,
        "movie_timescale": movie_ts,
        "movie_duration_units": movie_dur,
        "track_count": len(track_reports),
        "tracks": track_reports,
        "warning_count": 0,
        "warnings": [],
    }
