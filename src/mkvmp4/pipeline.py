"""Remux pipeline: parse -> map -> rewrite -> write -> validate -> atomic rename."""
from __future__ import annotations

import os
import tempfile

from . import elements, index, mapping, matroska, mp4, mp4inspect, timestamps

MAX_INPUT_BYTES = 8 << 30  # 8 GiB
PTS_TOLERANCE_MS = 1.0


class UsageError(Exception):
    """Bad arguments or exceeded limits (exit code 2)."""


class UnsupportedTrackError(Exception):
    """No MP4-compatible tracks (exit code 4)."""


class WriteFailedError(Exception):
    """Output write or atomic replace failed (exit code 5)."""


class ValidationError(Exception):
    """Written output failed structure/timestamp validation (exit code 5)."""


def _collect_samples(seg: matroska.Segment, track_number: int):
    ts_units, sizes, keyframes, payloads = [], [], [], []
    for block in seg.blocks:
        if block.track_number != track_number:
            continue
        for frame in block.frames:
            ts_units.append(block.timestamp)
            sizes.append(len(frame))
            keyframes.append(block.keyframe)
            payloads.append(frame)
    return ts_units, sizes, keyframes, payloads


def _build_meta(entry: dict, track: matroska.Track, seg: matroska.Segment):
    ts_units, sizes, keyframes, payloads = _collect_samples(seg, track.number)
    timescale = timestamps.choose_timescale(track.type, track.sampling_freq)
    pts, durations = timestamps.expand_timestamps(
        ts_units, seg.timestamp_scale, timescale, track.default_duration_ns)
    duration_units = int(pts[-1] + durations[-1])
    meta = {
        "mp4_track_id": entry["mp4_track_id"],
        "kind": entry["type"],
        "codec": entry["codec"],
        "codec_private": track.codec_private,
        "language": track.language,
        "timescale": timescale,
        "duration_units": duration_units,
        "duration_ms": duration_units * 1000 // timescale,
        "width": track.width,
        "height": track.height,
        "channels": track.channels,
        "sizes": sizes,
        "stts": index.build_stts(durations),
        "ctts": index.build_ctts([0] * len(sizes)),
        "stsc": index.build_stsc(),
        "stss": index.build_stss(keyframes) if entry["type"] == "video" else [],
    }
    stats = {
        "mp4_track_id": entry["mp4_track_id"],
        "type": entry["type"],
        "codec": entry["codec"],
        "timescale": timescale,
        "sample_count": len(sizes),
        "keyframe_count": len(meta["stss"]) if meta["stss"] else len(sizes),
        "first_pts_ms": timestamps.pts_ms(int(pts[0]), timescale),
        "last_pts_ms": timestamps.pts_ms(int(pts[-1]), timescale),
        "duration_ms": duration_units * 1000.0 / timescale,
    }
    return meta, b"".join(payloads), stats


def _validate_output(path: str, stats: list[dict]) -> None:
    try:
        check = mp4inspect.inspect_mp4(path)
    except mp4inspect.Mp4Error as exc:
        raise ValidationError(f"output failed structure check: {exc}") from exc
    if len(check["tracks"]) != len(stats):
        raise ValidationError("output track count mismatch")
    for got, want in zip(check["tracks"], stats):
        if got["sample_count"] != want["sample_count"]:
            raise ValidationError(
                f"track {want['mp4_track_id']}: sample count "
                f"{got['sample_count']} != {want['sample_count']}")
        for key in ("first_pts_ms", "last_pts_ms"):
            if abs(got[key] - want[key]) > PTS_TOLERANCE_MS:
                raise ValidationError(
                    f"track {want['mp4_track_id']}: {key} "
                    f"{got[key]:.3f} != {want[key]:.3f}")


def remux(input_path: str, output_path: str) -> dict:
    input_path = os.path.realpath(input_path)
    output_path = os.path.abspath(output_path)
    if not os.path.isfile(input_path):
        raise UsageError(f"input file not found: {input_path}")
    if input_path == output_path:
        raise UsageError("input and output paths are identical")
    size = os.path.getsize(input_path)
    if size > MAX_INPUT_BYTES:
        raise UsageError(f"input size {size} exceeds 8 GiB limit")
    out_dir = os.path.dirname(output_path) or "."
    if not os.path.isdir(out_dir):
        raise UsageError(f"output directory does not exist: {out_dir}")

    with open(input_path, "rb") as fh:
        data = fh.read()
    tops = elements.parse_file(data)
    summary = elements.summarize(tops)
    seg = matroska.parse_segment(data, tops)
    tmap = mapping.build_track_map(seg.tracks)
    if not tmap["tracks"]:
        raise UnsupportedTrackError(
            "no MP4-compatible tracks (need AVC, HEVC or AAC)")

    metas, media, stats = [], [], []
    for entry in tmap["tracks"]:
        track = seg.track_by_number(entry["track_number"])
        meta, blob, stat = _build_meta(entry, track, seg)
        metas.append(meta)
        media.append(blob)
        stats.append(stat)

    fd, tmp = tempfile.mkstemp(prefix=".mkvmp4-", suffix=".tmp", dir=out_dir)
    try:
        try:
            with os.fdopen(fd, "wb") as fh:
                layout = mp4.mux(fh.write, metas, media)
        except OSError as exc:
            raise WriteFailedError(f"failed writing temporary file: {exc}") from exc
        _validate_output(tmp, stats)
        try:
            os.replace(tmp, output_path)
        except OSError as exc:
            raise WriteFailedError(f"failed to replace output: {exc}") from exc
    except Exception:
        try:
            os.unlink(tmp)
        except OSError:
            pass
        raise

    index_entries = sum(
        len(m["stts"]) + len(m["sizes"]) + len(m["sizes"]) + len(m["stss"])
        for m in metas)
    return {
        "input": input_path,
        "output": output_path,
        "element_count": summary["element_count"],
        "block_elements": summary["block_elements"],
        "track_map": tmap["tracks"],
        "skipped_tracks": tmap["skipped"],
        "sample_count": sum(s["sample_count"] for s in stats),
        "tracks": stats,
        "index_entries": index_entries,
        "co64": layout["co64"],
        "file_size": layout["file_size"],
    }
