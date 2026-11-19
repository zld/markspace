"""MP4 muxing: sample tables, media data and container index rebuild.

The output is written to a seekable stream in this order: ``ftyp`` then a
streamed ``mdat`` (frame payloads copied byte-for-byte, never transcoded) and
finally the ``moov`` index whose sample tables describe the data just
written. Every recorded chunk offset is checked against the real ``mdat``
data region before the file is accepted.
"""

from __future__ import annotations

from dataclasses import dataclass, field

from . import boxes
from .boxes import BoxWriter, make_box
from .mapping import TrackMapping
from .matroska import MatroskaError, MatroskaFile, Sample, Track
from .timeline import TrackTimeline

MDAT_TYPE = b"mdat"


class MuxError(MatroskaError):
    """Raised when a valid MP4 cannot be produced from the parsed input."""


@dataclass(slots=True)
class MuxedTrack:
    mkv_track_number: int
    mp4_track_id: int
    timescale: int
    sample_count: int
    chunk_offsets: list[int]
    sample_sizes: list[int]
    first_seconds: float
    last_seconds: float
    end_seconds: float
    uses_co64: bool


@dataclass(slots=True)
class MuxResult:
    muxed_tracks: list[MuxedTrack]
    movie_timescale: int
    movie_duration_units: int
    mdat_data_start: int
    mdat_data_end: int
    box_tree: list[dict] = field(default_factory=list)
    total_boxes: int = 0


def _frames_for(mkv: MatroskaFile, number: int) -> list[Sample]:
    return [s for s in mkv.samples if s.track_number == number]


def _write_mdat(
    writer: BoxWriter,
    src_stream,
    mkv: MatroskaFile,
    mapping: TrackMapping,
    timelines: dict[int, TrackTimeline],
) -> tuple[list[MuxedTrack], int, int]:
    writer.open_box(MDAT_TYPE)
    data_start = writer.tell()
    muxed: list[MuxedTrack] = []
    for number in mapping.accepted_numbers:
        tl = timelines[number]
        frames = _frames_for(mkv, number)
        if len(frames) != tl.sample_count:
            raise MuxError(
                f"track {number}: sample count mismatch "
                f"({len(frames)} frames vs {tl.sample_count} timeline entries)")
        offsets: list[int] = []
        sizes: list[int] = []
        for frame in frames:
            offset = writer.tell()
            src_stream.seek(frame.payload_offset)
            payload = src_stream.read(frame.payload_size)
            if len(payload) != frame.payload_size:
                raise MuxError(
                    f"truncated frame payload in source at "
                    f"offset {frame.payload_offset}")
            writer.write_raw(payload)
            offsets.append(offset)
            sizes.append(frame.payload_size)
        end_seconds = (tl.sample_times[-1] + tl.sample_durations[-1]) \
            / tl.timescale
        muxed.append(MuxedTrack(
            mkv_track_number=number,
            mp4_track_id=mapping.accepted[number],
            timescale=tl.timescale,
            sample_count=len(frames),
            chunk_offsets=offsets,
            sample_sizes=sizes,
            first_seconds=tl.first_seconds,
            last_seconds=tl.last_seconds,
            end_seconds=end_seconds,
            uses_co64=any(o > 0xFFFFFFFF for o in offsets),
        ))
    data_end = writer.tell()
    writer.end_box()
    return muxed, data_start, data_end


def _build_stbl(track: Track, mt: MuxedTrack, tl: TrackTimeline,
                frames: list[Sample]) -> bytes:
    if track.type == 1:
        if not track.codec_private:
            raise MuxError(f"video track {track.number} lacks AVC config")
        entry = boxes.avc1_box(track.pixel_width or 0,
                               track.pixel_height or 0,
                               track.codec_private)
        media_header = boxes.vmhd_box()
    else:
        if not track.codec_private:
            raise MuxError(
                f"audio track {track.number} lacks AudioSpecificConfig")
        entry = boxes.mp4a_box(track.channels or 2, track.bit_depth or 16,
                               track.sampling_frequency or 48000.0,
                               track.codec_private)
        media_header = boxes.smhd_box()

    parts = [
        boxes.stsd_box(entry),
        boxes.stts_box(tl.sample_durations),
        boxes.stsc_box(1),
        boxes.stsz_box(mt.sample_sizes),
        boxes.chunk_offset_box(mt.chunk_offsets),
    ]
    if track.type == 1:
        key_numbers = [i + 1 for i, f in enumerate(frames) if f.keyframe]
        if key_numbers:
            parts.append(boxes.stss_box(key_numbers))
    return make_box(b"stbl", b"".join(parts))


def _build_trak(track: Track, mt: MuxedTrack, tl: TrackTimeline,
                frames: list[Sample], movie_timescale: int) -> bytes:
    media_duration = tl.sample_times[-1] + tl.sample_durations[-1]
    track_duration = round(mt.end_seconds * movie_timescale)
    tkhd = boxes.tkhd_box(mt.mp4_track_id, track_duration,
                          track.pixel_width or 0, track.pixel_height or 0,
                          is_audio=track.type != 1)
    mdhd = boxes.mdhd_box(tl.timescale, media_duration)
    handler = b"vide" if track.type == 1 else b"soun"
    hdlr = boxes.hdlr_box(handler, f"{track.codec_id} handler")
    minf = make_box(b"minf", b"".join([
        boxes.vmhd_box() if track.type == 1 else boxes.smhd_box(),
        boxes.dinf_dref_box(),
        _build_stbl(track, mt, tl, frames),
    ]))
    mdia = make_box(b"mdia", mdhd + hdlr + minf)
    # The edit list maps movie time 0 onto the track's original first sample
    # position, preserving inter-track alignment after per-track zeroing.
    edts = boxes.elst_box(tl.media_start_units, media_duration, tl.timescale)
    return make_box(b"trak", tkhd + edts + mdia)


def _write_moov(writer: BoxWriter, mkv: MatroskaFile,
                mapping: TrackMapping, timelines: dict[int, TrackTimeline],
                muxed: list[MuxedTrack]) -> tuple[int, int]:
    movie_timescale = max(m.timescale for m in muxed)
    movie_duration = max(round(m.end_seconds * movie_timescale)
                         for m in muxed)
    writer.open_box(b"moov")
    writer.write_raw(
        boxes.mvhd_box(movie_timescale, movie_duration, len(muxed) + 1))
    tracks_by_num = {t.number: t for t in mkv.tracks}
    for mt in muxed:
        track = tracks_by_num[mt.mkv_track_number]
        tl = timelines[mt.mkv_track_number]
        frames = _frames_for(mkv, mt.mkv_track_number)
        writer.write_raw(
            _build_trak(track, mt, tl, frames, movie_timescale))
    writer.end_box()
    return movie_timescale, movie_duration


def remux_to_stream(src_stream, dst_stream, mkv: MatroskaFile,
                    mapping: TrackMapping,
                    timelines: dict[int, TrackTimeline]) -> MuxResult:
    """Write ftyp+mdat+moov into ``dst_stream`` and return run statistics."""
    writer = BoxWriter(dst_stream)
    writer.write_raw(boxes.ftyp_box())
    muxed, data_start, data_end = _write_mdat(
        writer, src_stream, mkv, mapping, timelines)
    movie_timescale, movie_duration = _write_moov(
        writer, mkv, mapping, timelines, muxed)
    writer.close()
    return MuxResult(
        muxed_tracks=muxed,
        movie_timescale=movie_timescale,
        movie_duration_units=movie_duration,
        mdat_data_start=data_start,
        mdat_data_end=data_end,
    )


# --------------------------------------------------------------------------
# Independent output verification: generic ISO BMFF box scan
# --------------------------------------------------------------------------

_CONTAINER_BOXES = {
    b"moov", b"trak", b"mdia", b"minf", b"stbl", b"edts", b"dinf", b"udta",
}


def _scan_boxes(stream) -> tuple[list[dict], list[dict], list[tuple[int, int]]]:
    """Walk every box, checking declared sizes against stream boundaries.

    Returns (flat box records, outline tree, mdat data ranges).
    """
    stream.seek(0, 2)
    file_size = stream.tell()
    stream.seek(0)
    flat: list[dict] = []
    tree: list[dict] = []
    mdats: list[tuple[int, int]] = []

    def walk(end: int, depth: int) -> None:
        while stream.tell() < end:
            start = stream.tell()
            header = stream.read(8)
            if len(header) < 8:
                raise MuxError("truncated box header")
            size = int.from_bytes(header[:4], "big")
            box_type = header[4:8]
            header_len = 8
            if size == 1:
                large = stream.read(8)
                if len(large) < 8:
                    raise MuxError("truncated largesize field")
                size = int.from_bytes(large, "big")
                header_len = 16
            elif size == 0:
                size = end - start  # box extends to end of stream
            if size < header_len or start + size > end:
                raise MuxError(
                    f"box {box_type!r} at {start} has invalid size {size}")
            payload_start = start + header_len
            payload_end = start + size
            record = {"type": box_type.decode("latin-1"),
                      "start": start, "end": payload_end, "depth": depth}
            flat.append(record)
            if tree and tree[-1]["name"] == record["type"] \
                    and tree[-1]["depth"] == depth:
                tree[-1]["count"] += 1
            else:
                tree.append({"name": record["type"], "depth": depth,
                             "count": 1})
            if box_type == MDAT_TYPE:
                mdats.append((payload_start, payload_end))
            if box_type in _CONTAINER_BOXES:
                stream.seek(payload_start)
                walk(payload_end, depth + 1)
            stream.seek(payload_end)

    walk(file_size, 0)
    if stream.tell() != file_size:
        raise MuxError("box scan did not consume the whole file")
    return flat, tree, mdats


def verify_mp4(stream, result: MuxResult) -> tuple[int, list[dict]]:
    """Re-parse the produced file and validate indices against the mdat."""
    flat, tree, mdats = _scan_boxes(stream)
    types = {b["type"] for b in flat}
    for required in ("ftyp", "moov", "mvhd", "mdat", "trak", "mdhd",
                     "stsd", "stts", "stsc", "stsz"):
        if required not in types:
            raise MuxError(f"output missing required box {required!r}")
    if not mdats:
        raise MuxError("output contains no mdat box")
    mdat_start, mdat_end = mdats[0]
    if (mdat_start, mdat_end) != (result.mdat_data_start,
                                  result.mdat_data_end):
        raise MuxError("mdat data region differs from write-time record")
    total_samples = 0
    for mt in result.muxed_tracks:
        if len(mt.chunk_offsets) != mt.sample_count or \
                len(mt.sample_sizes) != mt.sample_count:
            raise MuxError(
                f"track {mt.mkv_track_number}: table length mismatch")
        for off, size in zip(mt.chunk_offsets, mt.sample_sizes):
            if not (mdat_start <= off and off + size <= mdat_end):
                raise MuxError(
                    f"track {mt.mkv_track_number}: sample at {off} size "
                    f"{size} falls outside mdat [{mdat_start}, {mdat_end})")
        total_samples += mt.sample_count
    if total_samples == 0:
        raise MuxError("output contains no samples")
    return len(flat), tree
