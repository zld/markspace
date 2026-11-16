"""Flat (non-fragmented) MP4 exporter with rebuilt stts/ctts/stsc/stsz/stco.

One shared :class:`~mkvmp4.timeline.Timeline` feeds both exporters; this one
emits the classic sample-table layout with all media bytes in one ``mdat``.
Chunk offsets are rebuilt from real byte positions and every index is
validated in memory (exit code 5) and again by re-parsing the assembled byte
image before the atomic rename.
"""

from ..errors import CorruptMKVError, IndexValidationError
from ..timeline import MOVIE_TIMESCALE, TimedSample, Timeline, TrackTimeline
from .atoms import (box, full_box, i32, u16, u32, u64, u8,
                    validate_structure, write_file_atomic)

#: Above this absolute byte offset stco cannot address the file, so co64
#: (64-bit chunk offsets) is emitted instead.
STCO_LIMIT = 0xFFFFFFFF


class FlatBuildResult:
    """Summary returned by :func:`export_flat` for the terminal report."""

    def __init__(self, path: str, total_samples: int,
                 offset_box: str, offset_entries: int,
                 chunks_per_track: list[int]):
        self.path = path
        self.total_samples = total_samples
        self.offset_box = offset_box
        self.offset_entries = offset_entries
        self.chunks_per_track = chunks_per_track


# ---------------------------------------------------------------------------
# Sample-table run-length encoders
# ---------------------------------------------------------------------------
def _stts_entries(samples: list[TimedSample]) -> list[tuple[int, int]]:
    entries: list[tuple[int, int]] = []
    for sample in samples:
        if entries and entries[-1][1] == sample.duration_ms:
            count, delta = entries[-1]
            entries[-1] = (count + 1, delta)
        else:
            entries.append((1, sample.duration_ms))
    return entries


def _ctts_entries(samples: list[TimedSample]) -> list[tuple[int, int]]:
    entries: list[tuple[int, int]] = []
    for sample in samples:
        offset = sample.composition_offset_ms
        if entries and entries[-1][1] == offset:
            count, value = entries[-1]
            entries[-1] = (count + 1, value)
        else:
            entries.append((1, offset))
    return entries


def _stsc_entries(chunk_sample_counts: list[int]) -> list[tuple[int, int, int]]:
    # One sample description; describe runs of chunks sharing a sample count.
    entries: list[tuple[int, int, int]] = []
    previous = None
    for index, count in enumerate(chunk_sample_counts, start=1):
        if previous is not None and previous[2] == count:
            continue
        entries.append((index, 1, count))
        previous = entries[-1]
    return entries


# ---------------------------------------------------------------------------
# Codec configuration boxes built from Matroska CodecPrivate data.
# ---------------------------------------------------------------------------
def _avc_config_box(codec_private: bytes) -> bytes:
    if len(codec_private) < 7 or codec_private[0] != 1:
        raise CorruptMKVError(
            "AVC CodecPrivate is not a valid AVCDecoderConfigurationRecord")
    return box("avcC", codec_private)


def _esds_box(codec_private: bytes) -> bytes:
    """Build a minimal ESDescriptor chain wrapping an AAC AudioSpecificConfig."""
    if not codec_private:
        raise CorruptMKVError("AAC track has no AudioSpecificConfig in CodecPrivate")

    def tag(tag_id: int, content: bytes) -> bytes:
        # ES descriptor sizes use EBML-style multi-byte size encoding.
        size = len(content)
        header = bytearray([tag_id])
        for shift in (21, 14, 7):
            header.append(0x80 | ((size >> shift) & 0x7F))
        header.append(size & 0x7F)
        return bytes(header) + content

    decoder_specific = tag(0x05, codec_private)
    decoder = tag(0x04,
                  u8(0x40) +          # object type indicator: ISO audio
                  u8(0x15) +          # stream type: audio, upstream present
                  b"\x00\x00\x00" +   # buffer size
                  u32(0) + u32(0) +   # max/avg bitrate unknown
                  decoder_specific)
    slc = tag(0x06, u8(0x02))         # SLConfigDescriptor
    es = tag(0x03, u16(1) + u8(0x00) + decoder + slc)
    return full_box("esds", 0, 0, es)


def _sample_description(track: TrackTimeline) -> bytes:
    info = track.track
    if info.mp4_codec == "avc1":
        width = info.pixel_width or 0
        height = info.pixel_height or 0
        body = (b"\x00" * 6 + u16(1) + b"\x00" * 16 +
                u16(width) + u16(height) + b"\x00" * 14 +
                u16(0x0018) + b"\x00" * 32 +
                _avc_config_box(info.codec_private))
        entry = box("avc1", body)
    else:
        body = (b"\x00" * 6 + u16(1) + b"\x00" * 16 +
                u16(2) + u16(16) + b"\x00" * 14 +
                _esds_box(info.codec_private))
        entry = box("mp4a", body)
    return full_box("stsd", 0, 0, u32(1) + entry)


def _build_stbl(track: TrackTimeline, chunk_offsets: list[int],
                chunk_sample_counts: list[int], use_co64: bool) -> bytes:
    samples = track.samples
    stts = full_box("stts", 0, 0,
                    u32(len(_stts_entries(samples))) +
                    b"".join(u32(c) + u32(d) for c, d in _stts_entries(samples)))
    ctts = full_box("ctts", 1, 0,
                    u32(len(_ctts_entries(samples))) +
                    b"".join(u32(c) + i32(v)
                             for c, v in _ctts_entries(samples)))
    stsc_entries = _stsc_entries(chunk_sample_counts)
    stsc = full_box("stsc", 0, 0,
                    u32(len(stsc_entries)) +
                    b"".join(u32(fc) + u32(spc) + u32(sdi)
                             for fc, spc, sdi in stsc_entries))
    stsz = full_box("stsz", 0, 0, u32(0) + u32(len(samples)) +
                    b"".join(u32(len(s.data)) for s in samples))
    if use_co64:
        offset_box = full_box("co64", 0, 0,
                              u32(len(chunk_offsets)) +
                              b"".join(u64(o) for o in chunk_offsets))
        offset_name = "co64"
    else:
        offset_box = full_box("stco", 0, 0,
                              u32(len(chunk_offsets)) +
                              b"".join(u32(o) for o in chunk_offsets))
        offset_name = "stco"
    return (box("stbl", _sample_description(track) + stts + ctts + stsc +
                stsz + offset_box), offset_name, len(chunk_offsets))


def _build_moov(timeline: Timeline, stbl_parts: list[tuple]) -> bytes:
    # mvhd: version 0, modest fixed fields; duration from the longest track.
    duration = max(
        (t.samples[-1].pts_ms + t.samples[-1].duration_ms
         for t in timeline.tracks if t.samples), default=0)
    mvhd = full_box("mvhd", 0, 0,
                    u32(0) + u32(0) + u32(MOVIE_TIMESCALE) + u32(duration) +
                    u32(0x00010000) + u16(0x0100) + b"\x00" * 10 +
                    b"\x00\x01\x00\x00" + b"\x00" * 24 +
                    b"\x00\x01\x00\x00" + b"\x00" * 24 +
                    b"\x40\x00\x00\x00" + b"\x00" * 23 + u16(2))
    traks = b""
    for track, _stbl, _off_name, _off_count in stbl_parts:
        info = track.track
        tkhd = full_box(
            "tkhd", 0, 0x07,
            u32(0) + u32(0) + u32(MOVIE_TIMESCALE) + u32(duration) +
            b"\x00" * 8 + u16(0) + u16(0) + u16(0) + u16(0) +
            b"\x00\x01\x00\x00" + b"\x00" * 24 +
            u32(info.pixel_width or 0) + u32(info.pixel_height or 0))
        # mdhd carries the same millisecond timescale for both media kinds.
        mdhd = full_box("mdhd", 0, 0,
                        u32(0) + u32(0) + u32(MOVIE_TIMESCALE) +
                        u32(duration) + u16(0x55C4) + u16(0))
        hdlr = full_box(
            "hdlr", 0, 0, b"\x00" * 4 +
            (b"vide" if info.track_type == 1 else b"soun") +
            b"\x00" * 12 + b"mkvmp4 remux\x00")
        minf = box("minf",
                   box("vmhd" if info.track_type == 1 else "smhd",
                       full_box("vmhd", 0, 1, b"\x00" * 8)
                       if info.track_type == 1
                       else full_box("smhd", 0, 0, b"\x00" * 4)) +
                   box("dinf", box("dref", full_box(
                       "dref", 0, 0, u32(1) +
                       box("url ", full_box("url ", 0, 1, b""))))) +
                   _stbl)
        traks += box("trak", tkhd + box("mdia", mdhd + hdlr + minf))
    return box("moov", mvhd + traks)


def export_flat(timeline: Timeline, output_path: str) -> FlatBuildResult:
    """Build, validate and atomically write a flat MP4 file."""
    # One chunk per sample keeps stsc/stco reconstruction trivial and makes
    # offset verification exact; chunk counts stay bounded by the sample
    # limit enforced during parsing.
    mdat_blobs: list[bytes] = []
    per_track: list[tuple[TrackTimeline, list[int], list[int]]] = []
    total_samples = 0
    for track in timeline.tracks:
        offsets: list[int] = []
        chunk_counts: list[int] = []
        for sample in track.samples:
            offsets.append(0)  # patched once mdat's position is known
            chunk_counts.append(1)
            mdat_blobs.append(sample.data)
            total_samples += 1
        per_track.append((track, offsets, chunk_counts))

    mdat_payload = b"".join(mdat_blobs)
    ftyp = box("ftyp", b"isom" + u32(0x200) + b"isomiso2mp41")
    # First pass: build moov with zero offsets to measure its size.
    placeholder = [
        (track, _build_stbl(track, off, counts, False)[0], "stco", len(off))
        for track, off, counts in per_track]  # type: ignore[var-annotated]
    moov_placeholder = _build_moov(timeline, placeholder)
    mdat_offset = len(ftyp) + len(moov_placeholder)
    if mdat_offset + 8 + len(mdat_payload) > STCO_LIMIT:
        use_co64 = True
    else:
        use_co64 = False

    # Patch real chunk offsets: each sample is its own chunk.
    cursor = mdat_offset + 8
    stbl_parts = []
    for track, offsets, chunk_counts in per_track:
        for i, sample in enumerate(track.samples):
            offsets[i] = cursor
            cursor += len(sample.data)
        stbl, offset_name, offset_count = _build_stbl(
            track, offsets, chunk_counts, use_co64)
        stbl_parts.append((track, stbl, offset_name, offset_count))
    moov = _build_moov(timeline, stbl_parts)

    # In-memory validation before touching the file system.
    _pre_validate(timeline, per_track, mdat_offset, len(mdat_payload),
                  use_co64)

    image = ftyp + moov + box("mdat", mdat_payload)
    _post_validate(image, timeline, per_track, mdat_offset,
                   len(mdat_payload))
    write_file_atomic(output_path, image)

    offset_box = "co64" if use_co64 else "stco"
    return FlatBuildResult(
        path=output_path,
        total_samples=total_samples,
        offset_box=offset_box,
        offset_entries=sum(len(off) for _, off, _ in per_track),
        chunks_per_track=[len(off) for _, off, _ in per_track],
    )


def _pre_validate(timeline: Timeline, per_track, mdat_offset: int,
                  mdat_payload_len: int, use_co64: bool) -> None:
    if not timeline.tracks:
        raise IndexValidationError("no tracks to export")
    expected = sum(len(t.samples) for t in timeline.tracks)
    counted = sum(len(t.samples) for t, _, _ in per_track)
    if expected != counted:
        raise IndexValidationError(
            f"sample count mismatch: timeline={expected}, tables={counted}")
    mdat_end = mdat_offset + 8 + mdat_payload_len
    for track, offsets, _counts in per_track:
        if not offsets:
            raise IndexValidationError(
                f"track {track.track.number} has no chunks")
        if any(not mdat_offset + 8 <= off < mdat_end for off in offsets):
            raise IndexValidationError(
                f"track {track.track.number} chunk offset outside mdat")
        if not use_co64 and any(off > STCO_LIMIT for off in offsets):
            raise IndexValidationError(
                "stco offset exceeds 32-bit range but co64 was not selected")


def _post_validate(image: bytes, timeline: Timeline, per_track,
                   mdat_offset: int, mdat_payload_len: int) -> None:
    index = validate_structure(image, expected=["ftyp", "moov", "mdat"])
    mdat = index["mdat"]
    if mdat.payload_start != mdat_offset + 8:
        raise IndexValidationError("mdat payload offset drifted after layout")
    if mdat.payload_end != len(image):
        raise IndexValidationError("mdat does not end the file as planned")
    # Re-derive every sample's byte span and confirm it lands inside mdat.
    for track, offsets, _counts in per_track:
        for i, sample in enumerate(track.samples):
            start = offsets[i]
            if not (mdat.payload_start <= start and
                    start + len(sample.data) <= mdat.payload_end):
                raise IndexValidationError(
                    f"sample {i} of track {track.track.number} escapes mdat")
