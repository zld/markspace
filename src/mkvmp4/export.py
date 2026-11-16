"""Remux orchestration and read-back verification.

Both export modes (``single`` and ``segmented``) share the same EBML ->
Matroska -> timeline -> codec pipeline; only the mdat placement strategy
inside :class:`~mkvmp4.bmff.Mp4Writer` differs. All outputs are written to
a same-directory temp file and atomically replaced only after the verify
pass succeeds, so a corrupt input can never overwrite the destination and
the source is never touched.
"""

from __future__ import annotations

import json
import os
import tempfile
from dataclasses import asdict

from .bmff import Mp4Writer, TrackOutput, read_mp4_index
from .codec import map_codec, validate_video_block
from .ebml import Limits
from .errors import (ConversionError, InputFormatError, VerificationError)
from .matroska import parse_matrouska
from .timeline import build_all, TrackTimeline

NS_PER_MS = 1_000_000


def _build_manifest(mkv, timelines: list[TrackTimeline],
                    selected: list[int]) -> bytes:
    """Audit trail embedded in the output: track mapping and corrections."""
    manifest = {
        "source": os.path.basename(mkv.path),
        "timestamp_scale_ns": mkv.timestamp_scale_ns,
        "selected_tracks": selected,
        "tracks": [],
    }
    for timeline in timelines:
        stats = timeline.stats
        track = timeline.track
        manifest["tracks"].append({
            "source_track_number": track.number,
            "track_uid": track.uid,
            "codec_id": track.codec_id,
            "language": track.language,
            "flag_default": track.flag_default,
            "timescale": stats.timescale,
            "sample_count": stats.sample_count,
            "first_pts_ms": stats.first_pts_ms,
            "last_pts_ms": stats.last_pts_ms,
            "rewritten_first_pts_ms": stats.rewritten_first_pts_ms,
            "rewritten_last_pts_ms": stats.rewritten_last_pts_ms,
            "negative_start_shift_ms": stats.negative_start_shift_ms,
            "rounding_residual_ns": stats.rounding_residual_ns,
            "rounding_affected_samples":
                stats.rounding_affected_samples,
            "duplicate_pts_corrections":
                stats.duplicate_pts_corrections,
            "reorder_samples": stats.reorder_samples,
        })
    return json.dumps(manifest, sort_keys=True,
                      ensure_ascii=False).encode("utf-8")


def _select_tracks(mkv, track_selection: list[int] | None):
    if track_selection is None:
        return list(mkv.tracks), [t.number for t in mkv.tracks]
    if not track_selection:
        raise InputFormatError("track selection is empty")
    known = {t.number for t in mkv.tracks}
    unknown = [n for n in track_selection if n not in known]
    if unknown:
        raise InputFormatError(
            f"unknown track number(s) in selection: {unknown}")
    selected = [t for t in mkv.tracks if t.number in track_selection]
    return selected, [t.number for t in selected]


def remux(input_path: str, output_path: str, *, mode: str,
          limits: Limits | None = None,
          track_selection: list[int] | None = None) -> dict:
    if mode not in ("single", "segmented"):
        raise InputFormatError(f"unknown export mode {mode!r}")
    if os.path.abspath(input_path) == os.path.abspath(output_path):
        raise InputFormatError("output path must differ from the source")
    with open(input_path, "rb") as handle:
        data = handle.read()
    mkv = parse_matrouska(data, input_path, limits)
    # Use the same bounded full-tree walk as inspect so element_count and
    # unknown_length_count are stable across commands for a given input.
    from .report import element_tree
    tree_info = element_tree(data, limits)
    chosen, selected_numbers = _select_tracks(mkv, track_selection)

    # Reject any unrepresentable codec (subtitles, unknown encoders, ...)
    # before touching the timeline so users get a codec-scoped category-2
    # error instead of a later conversion failure.
    mappings = {track.number: map_codec(track) for track in chosen}

    timelines = build_all(chosen, mkv.clusters)
    outputs: list[TrackOutput] = []
    for timeline in timelines:
        mapping = mappings[timeline.track.number]
        if mapping.timescale and mapping.timescale != timeline.stats.timescale:
            raise ConversionError(
                f"track {timeline.track.number}: codec timescale "
                f"{mapping.timescale} != normalized timeline timescale "
                f"{timeline.stats.timescale}")
        output = TrackOutput(
            track_id=len(outputs) + 1,
            mapping=mapping,
            timescale=timeline.stats.timescale,
            language=timeline.track.language or "und")
        for sample in timeline.samples:
            payload = sample.block.data
            if mapping.handler_type == "vide":
                validate_video_block(mapping, payload)
            output.add_sample(payload, sample)
        outputs.append(output)

    manifest = _build_manifest(mkv, timelines, selected_numbers)
    output_dir = os.path.dirname(os.path.abspath(output_path)) or "."
    fd, temp_path = tempfile.mkstemp(
        prefix=f".tmp-{os.path.basename(output_path)}-", dir=output_dir)
    os.close(fd)
    writer = Mp4Writer(outputs, temp_path, mode == "segmented", manifest)
    try:
        write_info = writer.write()
        with open(temp_path, "rb") as handle:
            written = handle.read()
        verify_result = verify_bytes(
            written, mkv_path=input_path, expected_tracks=len(outputs))
        os.replace(temp_path, output_path)
    except BaseException:
        writer.cleanup_parts()
        try:
            os.remove(temp_path)
        except FileNotFoundError:
            pass
        raise
    else:
        writer.cleanup_parts()
    return {
        "mode": mode,
        "output": output_path,
        "size": write_info["size"],
        "segments": write_info["segments"],
        "co64": write_info["co64"],
        "track_map": _track_map(outputs, timelines),
        "timelines": [asdict(t.stats) for t in timelines],
        "verification": verify_result,
        "element_count": tree_info["element_count"],
        "unknown_length_count": tree_info["unknown_length_count"],
    }


def _track_map(outputs: list[TrackOutput],
               timelines: list[TrackTimeline]) -> list[dict]:
    result = []
    for output, timeline in zip(outputs, timelines):
        stats = timeline.stats
        result.append({
            "source_track_number": timeline.track.number,
            "mp4_track_id": output.track_id,
            "codec_id": timeline.track.codec_id,
            "sample_entry": output.mapping.entry_type.decode("latin-1"),
            "handler_type": output.mapping.handler_type,
            "timescale": output.timescale,
            "sample_count": stats.sample_count,
            "rewritten_first_pts_ms": stats.rewritten_first_pts_ms,
            "rewritten_last_pts_ms": stats.rewritten_last_pts_ms,
        })
    return result


# --- verification -----------------------------------------------------------

def verify_bytes(data: bytes, *, mkv_path: str | None = None,
                 expected_tracks: int | None = None) -> dict:
    """Read the MP4 index back and prove it against the bytes on disk."""
    indexes, mdat = read_mp4_index(data)
    if expected_tracks is not None and len(indexes) != expected_tracks:
        raise VerificationError(
            f"track count {len(indexes)} != expected {expected_tracks}")
    track_reports = []
    total_samples = 0
    for index in indexes:
        start, end = index.sample_span()
        if start < mdat.payload_start or end > mdat.payload_end:
            raise VerificationError(
                f"track {index.track_id}: sample span 0x{start:X}..0x{end:X} "
                f"escapes mdat 0x{mdat.payload_start:X}.."
                f"0x{mdat.payload_end:X}")
        if any(size <= 0 for size in index.sample_sizes):
            raise VerificationError(
                f"track {index.track_id}: non-positive sample size")
        timing = index.timing_line()  # decode order
        dts_values = [dts for dts, _pts in timing]
        pts_values = [pts for _dts, pts in timing]
        if any(b < a for a, b in zip(dts_values, dts_values[1:])):
            raise VerificationError(
                f"track {index.track_id}: DTS is not monotonic in decode "
                "order")
        presentation_order = sorted(pts_values)
        if any(b < a for a, b in zip(presentation_order,
                                     presentation_order[1:])):
            raise VerificationError(
                f"track {index.track_id}: PTS is not monotonic in "
                "presentation order")
        if index.stss_entries and (
                index.stss_entries[0] != 1
                or any(n < 1 or n > index.sample_count
                       for n in index.stss_entries)):
            raise VerificationError(
                f"track {index.track_id}: stss entries out of range")
        first_ms = presentation_order[0] / index.timescale * 1000.0
        last_ms = presentation_order[-1] / index.timescale * 1000.0
        track_reports.append({
            "mp4_track_id": index.track_id,
            "handler_type": index.handler_type,
            "timescale": index.timescale,
            "sample_count": index.sample_count,
            "offset_start": start,
            "offset_end": end,
            "first_pts_ms": first_ms,
            "last_pts_ms": last_ms,
        })
        total_samples += index.sample_count
    return {
        "index_valid": True,
        "track_count": len(indexes),
        "sample_count": total_samples,
        "tracks": track_reports,
        "mdat_size": mdat.payload_end - mdat.payload_start,
    }


def verify_file(path: str, limits: Limits | None = None) -> dict:
    cap = (limits.max_bytes if limits is not None
           else Limits().max_bytes)
    if os.path.getsize(path) > cap:
        raise VerificationError(
            f"input size exceeds limit of {cap} bytes")
    with open(path, "rb") as handle:
        data = handle.read()
    result = verify_bytes(data)
    # The optional manifest companion box is the auditable track map; when
    # present it must agree with the indexes read back from the sample tables.
    from .bmff import iter_boxes, find_box
    if b"mkvm" in [raw.type for raw in iter_boxes(data, 0, len(data))]:
        manifest_box = find_box(data, 0, len(data), b"mkvm")
        payload = data[manifest_box.payload_start:manifest_box.payload_end]
        manifest = json.loads(payload.decode("utf-8"))
        result["manifest"] = manifest
        if len(manifest["tracks"]) != result["track_count"]:
            raise VerificationError(
                "manifest track count disagrees with moov index")
        manifest_counts = [t["sample_count"] for t in manifest["tracks"]]
        index_counts = [t["sample_count"] for t in result["tracks"]]
        if manifest_counts != index_counts:
            raise VerificationError(
                "manifest sample counts disagree with stsz indexes")
    return result
