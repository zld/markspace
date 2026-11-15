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
from .errors import (ConversionError, InputFormatError, VerificationError,
                     MkvMp4Error)
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
    chosen, selected_numbers = _select_tracks(mkv, track_selection)

    timelines = build_all(chosen, mkv.clusters)
    outputs: list[TrackOutput] = []
    for timeline in timelines:
        mapping = map_codec(timeline.track)
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
        "element_count": mkv.element_count,
        "unknown_length_count": mkv.unknown_length_count,
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

