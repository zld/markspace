"""Minimal ISO-BMFF (MP4) writer for remuxed H.264/AAC tracks.

Compressed frames are copied byte-for-byte; nothing is decoded or re-encoded.
Samples are grouped into fixed-size chunks, ``mdat`` is 64-bit when needed, and
``co64`` is selected automatically past the 32-bit offset range.
"""

from __future__ import annotations

import os
import struct
from contextlib import contextmanager

import numpy as np

from .matroska import MAX_PACKET_BYTES, ParsedMatroska
from .timeline import Timeline, TrackTimeline, UINT32_MAX

CHUNK_SAMPLES = 1024
UNITY_MATRIX = (struct.pack(">9I", 0x00010000, 0, 0, 0, 0x00010000, 0, 0, 0,
                            0x40000000))


def _u8(value: int) -> bytes:
    return struct.pack(">B", value)


def _u16(value: int) -> bytes:
    return struct.pack(">H", value)


def _u24(value: int) -> bytes:
    return value.to_bytes(3, "big")


def _u32(value: int) -> bytes:
    return struct.pack(">I", value)


def _u64(value: int) -> bytes:
    return struct.pack(">Q", value)


def _i32(value: int) -> bytes:
    return struct.pack(">i", value)


def box(kind: bytes, payload: bytes) -> bytes:
    size = len(payload) + 8
    if size > UINT32_MAX:
        raise ValueError(f"box {kind!r} exceeds 4 GiB")
    return _u32(size) + kind + payload


def full_box(kind: bytes, version: int, flags: int, payload: bytes) -> bytes:
    return box(kind, _u8(version) + _u24(flags) + payload)


def _time_fields(duration: int) -> tuple[int, bytes]:
    """(version, creation+modification+timescale+duration) or raise."""

    if duration <= UINT32_MAX:
        return 0, _u32(0) + _u32(0)
    if duration > (1 << 63) - 1:
        raise ValueError("duration overflows signed 64-bit")
    return 1, _u64(0) + _u64(0)


def _runs(values: np.ndarray) -> list[tuple[int, int]]:
    if values.size == 0:
        return []
    changes = np.flatnonzero(values[1:] != values[:-1]) + 1
    starts = np.concatenate(([0], changes))
    ends = np.concatenate((changes, [values.size]))
    return [(int(e - s), int(values[s])) for s, e in zip(starts, ends)]


def _mvhd(duration: int, timescale: int, next_track: int) -> bytes:
    version, _ = _time_fields(duration)
    head = ((bytes(16) if version else bytes(8)) + _u32(timescale) +
            (_u64(duration) if version else _u32(duration)))
    body = (_u32(0x00010000) + _u16(0x0100) + bytes(10) + UNITY_MATRIX +
            bytes(24) + _u32(next_track))
    return full_box(b"mvhd", version, 0, head + body)


def _tkhd(duration: int, track_id: int, volume: int, width: int, height: int,
          is_video: bool) -> bytes:
    version, _ = _time_fields(duration)
    times = bytes(16) if version else bytes(8)
    head = (times + _u32(track_id) + _u32(0) +
            (_u64(duration) if version else _u32(duration)))
    body = (bytes(8) + _u16(0) + _u16(0) + _u16(volume) + _u16(0) + UNITY_MATRIX +
            _u32(min(width, 65535) << 16 if is_video else 0) +
            _u32(min(height, 65535) << 16 if is_video else 0))
    return full_box(b"tkhd", version, 0x000003, head + body)


def _elst(entries: list[tuple[int, int]]) -> bytes:
    wide = any(duration > UINT32_MAX or media_time > 0x7FFFFFFF
               for duration, media_time in entries)
    payload = _u32(len(entries))
    for duration, media_time in entries:
        if wide:
            payload += _u64(duration) + struct.pack(">q", media_time)
        else:
            payload += _u32(duration) + _i32(media_time)
        payload += _u16(1) + _u16(0)
    return full_box(b"elst", 1 if wide else 0, 0, payload)


def _mdhd(duration: int, timescale: int) -> bytes:
    version, _ = _time_fields(duration)
    head = (bytes(16) if version else bytes(8)) + _u32(timescale) + (
        _u64(duration) if version else _u32(duration))
    return full_box(b"mdhd", version, 0, head + _u16(0x55C4) + _u16(0))


def _hdlr(handler: str, name: str) -> bytes:
    return full_box(b"hdlr", 0, 0, _u32(0) + handler.encode("ascii") + bytes(12) +
                    name.encode("utf-8") + b"\x00")


def _dinf() -> bytes:
    dref = full_box(b"dref", 0, 0, _u32(1) + full_box(b"url ", 0, 1, b""))
    return box(b"dinf", dref)


def _esds(asc: bytes) -> bytes:
    specific = _u8(0x05) + _u8(len(asc)) + asc
    decoder = (_u8(0x04) + _u8(13 + len(specific)) + _u8(0x40) + _u8(0x15) +
               _u24(0) + _u32(0) + _u32(0) + specific)
    sl = _u8(0x06) + _u8(1) + _u8(0x02)
    es = _u8(0x03) + _u8(3 + len(decoder) + len(sl)) + _u16(0) + _u8(0) + decoder + sl
    if any(len(part) > 127 for part in (specific, decoder, es)):
        raise ValueError("esds descriptor exceeds one-byte length")
    return full_box(b"esds", 0, 0, es)


def _avc1(track: TrackTimeline) -> bytes:
    meta = (_u8(0) * 6 + _u16(1) + _u16(0) + _u16(0) + bytes(12) +
            _u16(track.track.width or 0) + _u16(track.track.height or 0) +
            _u32(0x00480000) + _u32(0x00480000) + _u32(0) + _u16(1) +
            bytes(32) + _u16(0x0018) + _u16(0xFFFF))
    return box(b"avc1", meta + box(b"avcC", track.track.codec_private))


def _mp4a(track: TrackTimeline) -> bytes:
    rate = int(round(track.track.sample_rate or 0))
    meta = (_u8(0) * 6 + _u16(1) + _u32(0) + _u32(0) +
            _u16(track.track.channels or 0) + _u16(16) + _u16(0) + _u16(0) +
            _u32(min(rate, 0xFFFF) << 16))
    return box(b"mp4a", meta + _esds(track.track.codec_private))


def _stsd(track: TrackTimeline) -> bytes:
    entry = _avc1(track) if track.track.handler == "vide" else _mp4a(track)
    return full_box(b"stsd", 0, 0, _u32(1) + entry)


def _stts(durations: np.ndarray) -> bytes:
    runs = _runs(durations)
    payload = _u32(len(runs)) + b"".join(_u32(c) + _u32(d) for c, d in runs)
    return full_box(b"stts", 0, 0, payload)


def _ctts(offsets: np.ndarray) -> bytes | None:
    if not np.any(offsets != 0):
        return None
    runs = _runs(offsets)
    payload = _u32(len(runs)) + b"".join(_u32(c) + _i32(o) for c, o in runs)
    return full_box(b"ctts", 1, 0, payload)


def _chunk_map(sample_count: int) -> list[tuple[int, int]]:
    chunks = max(1, (sample_count + CHUNK_SAMPLES - 1) // CHUNK_SAMPLES)
    if chunks == 1:
        return [(1, sample_count)]
    last = sample_count - (chunks - 1) * CHUNK_SAMPLES
    if last == CHUNK_SAMPLES:
        return [(1, CHUNK_SAMPLES)]
    return [(1, CHUNK_SAMPLES), (chunks, last)]


def _stsc(sample_count: int, chunk_count: int) -> bytes:
    runs = _chunk_map(sample_count) if chunk_count else []
    payload = _u32(len(runs)) + b"".join(
        _u32(first) + _u32(per) + _u32(1) for first, per in runs)
    return full_box(b"stsc", 0, 0, payload)


def _stsz(sizes: np.ndarray) -> bytes:
    data = sizes.astype(">u4", copy=False).tobytes()
    return full_box(b"stsz", 0, 0, _u32(0) + _u32(sizes.size) + data)


def _stss(keyframes: np.ndarray) -> bytes | None:
    if keyframes.size == 0 or bool(np.all(keyframes)):
        return None
    indices = (np.flatnonzero(keyframes) + 1).astype(">u4", copy=False)
    return full_box(b"stss", 0, 0, _u32(indices.size) + indices.tobytes())


def _stco(offsets: list[int]) -> bytes:
    if any(offset > UINT32_MAX for offset in offsets):
        return full_box(b"co64", 0, 0,
                        _u32(len(offsets)) + b"".join(_u64(o) for o in offsets))
    return full_box(b"stco", 0, 0,
                    _u32(len(offsets)) + b"".join(_u32(o) for o in offsets))


def build_stbl(track: TrackTimeline, chunk_offsets: list[int]) -> bytes:
    parts = [_stsd(track), _stts(track.duration)]
    ctts = _ctts(track.ctts)
    if ctts is not None:
        parts.append(ctts)
    parts += [_stsc(track.sample_count, len(chunk_offsets)), _stsz(track.size)]
    stss = _stss(track.keyframe)
    if stss is not None:
        parts.append(stss)
    parts.append(_stco(chunk_offsets))
    return box(b"stbl", b"".join(parts))


def _trak(track: TrackTimeline, chunk_offsets: list[int], timescale: int) -> bytes:
    is_video = track.track.handler == "vide"
    track_duration = track.media_base + track.duration_ticks
    entries = []
    if track.media_base > 0:
        entries.append((track.media_base, -1))
    entries.append((track.duration_ticks, 0))
    if is_video:
        media_info = box(b"vmhd", _u8(0) * 4 + bytes(8))
        media_name = "VideoHandler"
    else:
        media_info = full_box(b"smhd", 0, 0, bytes(4))
        media_name = "SoundHandler"
    stbl = build_stbl(track, chunk_offsets)
    minf = box(b"minf", media_info + _dinf() + stbl)
    mdia = box(b"mdia", _mdhd(track.duration_ticks, timescale) +
               _hdlr("vide" if is_video else "soun", media_name) + minf)
    return box(b"trak", b"".join([
        _tkhd(track_duration, track.mp4_track_id, 0 if is_video else 0x0100,
              track.track.width or 0, track.track.height or 0, is_video),
        box(b"edts", _elst(entries)),
        mdia,
    ]))


def build_moov(timeline: Timeline, chunk_offsets: dict[int, list[int]]) -> bytes:
    movie_duration = max(t.media_base + t.duration_ticks for t in timeline.tracks)
    parts = [_mvhd(movie_duration, timeline.timescale, len(timeline.tracks) + 1)]
    for track in timeline.tracks:
        parts.append(_trak(track, chunk_offsets[track.mp4_track_id], timeline.timescale))
    return box(b"moov", b"".join(parts))


def _ftyp() -> bytes:
    return box(b"ftyp", b"isom" + _u32(512) + b"isomiso2avc1mp41")


def _copy_track(source, sink, track: TrackTimeline, data_start: int) -> list[int]:
    offsets: list[int] = []
    position = data_start
    for chunk_start in range(0, track.sample_count, CHUNK_SAMPLES):
        offsets.append(position)
        chunk_end = min(track.sample_count, chunk_start + CHUNK_SAMPLES)
        for index in range(chunk_start, chunk_end):
            size = int(track.size[index])
            if size <= 0 or size > MAX_PACKET_BYTES:
                raise OSError(f"invalid frame size {size} at sample {index}")
            source.seek(int(track.offset[index]))
            payload = source.read(size)
            if len(payload) != size:
                raise OSError(f"short read for sample {index}")
            sink.write(payload)
            position += size
    return offsets


def write_mp4(source_path: str, mkv: ParsedMatroska, timeline: Timeline,
              output_path: str) -> None:
    """Write ftyp + mdat + moov; the caller owns temporary-file semantics."""

    total_bytes = sum(int(t.size.sum()) for t in timeline.tracks)
    header_size = 8 if total_bytes + 8 <= UINT32_MAX else 16
    ftyp = _ftyp()
    data_start = len(ftyp) + header_size
    with open(source_path, "rb") as source, open(output_path, "wb") as sink:
        sink.write(ftyp)
        if header_size == 8:
            sink.write(_u32(total_bytes + 8) + b"mdat")
        else:
            sink.write(_u32(1) + b"mdat" + _u64(total_bytes + 16))
        chunk_offsets: dict[int, list[int]] = {}
        for track in timeline.tracks:
            chunk_offsets[track.mp4_track_id] = _copy_track(
                source, sink, track, data_start)
        if sink.tell() != data_start + total_bytes:
            raise OSError("mdat size mismatch while copying samples")
        sink.write(build_moov(timeline, chunk_offsets))
        sink.flush()
        os.fsync(sink.fileno())


@contextmanager
def temporary_output(output_path: str):
    """Yield a sibling temp path; it is removed unless atomically replaced."""

    directory = os.path.dirname(os.path.abspath(output_path)) or "."
    os.makedirs(directory, exist_ok=True)
    temp_path = os.path.join(
        directory, f".{os.path.basename(output_path)}.{os.getpid()}.partial")
    try:
        if os.path.exists(temp_path):
            os.unlink(temp_path)
        yield temp_path
    finally:
        if os.path.exists(temp_path):
            os.unlink(temp_path)
