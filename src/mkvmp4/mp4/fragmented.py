"""Fragmented MP4 (fMP4) exporter: moof, tfdt, trun and sidx.

The same :class:`~mkvmp4.timeline.Timeline` used by the flat exporter is
split into fragments. Every fragment is a ``moof`` + ``mdat`` pair and lists
its samples in one ``trun`` per track; ``tfdt`` carries the absolute base
decode time. A top-level ``sidx`` indexes every fragment (reference_count ==
fragment count) for byte-range seeking.

Sample data lengths inside ``trun`` are patched after the ``moof`` size is
known, and the assembled image is re-parsed before the atomic rename so a
bad offset never reaches the target path.
"""

from ..errors import CorruptMKVError, IndexValidationError
from ..timeline import MOVIE_TIMESCALE, TimedSample, Timeline, TrackTimeline
from .atoms import (box, full_box, i32, u16, u32, u64, u8,
                    validate_structure, write_file_atomic)
from .flat import _avc_config_box, _esds_box

#: trun flags: data-offset-present | first-sample-flags | sample-duration
#: | sample-size | sample-flags | sample-ctts.
TRUN_DATA_OFFSET = 0x000001
TRUN_FIRST_FLAGS = 0x000004
TRUN_SAMPLE_DURATION = 0x000100
TRUN_SAMPLE_SIZE = 0x000200
TRUN_SAMPLE_FLAGS = 0x000400
TRUN_SAMPLE_CTTS = 0x000800
TRUN_BASE_FLAGS = (TRUN_DATA_OFFSET | TRUN_FIRST_FLAGS |
                   TRUN_SAMPLE_DURATION | TRUN_SAMPLE_SIZE |
                   TRUN_SAMPLE_FLAGS | TRUN_SAMPLE_CTTS)

#: tfhd flags: default-base-is-moof | default-sample-flags-present.
TFHD_DEFAULT_BASE = 0x020000
TFHD_DEFAULT_FLAGS = 0x000008


class FragmentBuildResult:
    """Summary returned by :func:`export_fragmented`."""

    def __init__(self, path: str, fragment_count: int, sidx_refs: int,
                 total_samples: int):
        self.path = path
        self.fragment_count = fragment_count
        self.sidx_refs = sidx_refs
        self.total_samples = total_samples


def _sample_flags(sample: TimedSample) -> int:
    """ISO sample flags: bit 16 marks a non-sync sample; 0x01000000 = key."""
    return 0x02000000 if not sample.keyframe else 0x01000000


def _media_header_box(track: TrackTimeline) -> bytes:
    info = track.track
    if info.mp4_codec == "avc1":
        return box("vmhd", full_box("vmhd", 0, 1, b"\x00" * 8))
    return box("smhd", full_box("smhd", 0, 0, b"\x00" * 4))


def _sample_description(track: TrackTimeline) -> bytes:
    info = track.track
    if info.mp4_codec == "avc1":
        body = (b"\x00" * 6 + u16(1) + b"\x00" * 16 +
                u16(info.pixel_width or 0) + u16(info.pixel_height or 0) +
                b"\x00" * 14 + u16(0x0018) + b"\x00" * 32 +
                _avc_config_box(info.codec_private))
        entry = box("avc1", body)
    else:
        body = (b"\x00" * 6 + u16(1) + b"\x00" * 16 + u16(2) + u16(16) +
                b"\x00" * 14 + _esds_box(info.codec_private))
        entry = box("mp4a", body)
    return full_box("stsd", 0, 0, u32(1) + entry)


def _build_init_moov(timeline: Timeline) -> bytes:
    duration = max(
        (t.samples[-1].pts_ms + t.samples[-1].duration_ms
         for t in timeline.tracks if t.samples), default=0)
    mvhd = full_box(
        "mvhd", 0, 0,
        u32(0) + u32(0) + u32(MOVIE_TIMESCALE) + u32(duration) +
        u32(0x00010000) + u16(0x0100) + b"\x00" * 10 +
        b"\x00\x01\x00\x00" + b"\x00" * 24 + b"\x00\x01\x00\x00" +
        b"\x00" * 24 + b"\x40\x00\x00\x00" + b"\x00" * 23 + u16(2))
    traks = b""
    for track_id, track in enumerate(timeline.tracks, start=1):
        info = track.track
        tkhd = full_box(
            "tkhd", 0, 0x07,
            u32(0) + u32(0) + u32(MOVIE_TIMESCALE) + u32(duration) +
            b"\x00" * 8 + u16(0) * 4 + b"\x00\x01\x00\x00" + b"\x00" * 24 +
            u32(info.pixel_width or 0) + u32(info.pixel_height or 0))
        mdhd = full_box(
            "mdhd", 0, 0,
            u32(0) + u32(0) + u32(MOVIE_TIMESCALE) + u32(duration) +
            u16(0x55C4) + u16(0))
        hdlr = full_box(
            "hdlr", 0, 0, b"\x00" * 4 +
            (b"vide" if info.track_type == 1 else b"soun") +
            b"\x00" * 12 + b"mkvmp4 remux\x00")
        # Fragmented tracks use an empty stbl (only stsd); tables live in
        # the moof fragments, and mvex marks the track as fragmented.
        stbl = box("stbl", _sample_description(track))
        minf = box("minf", _media_header_box(track) +
                   box("dinf", box("dref", full_box(
                       "dref", 0, 0, u32(1) +
                       box("url ", full_box("url ", 0, 1, b""))))) + stbl)
        traks += box("trak", tkhd + box("mdia", mdhd + hdlr + minf))
    # mvex: one trex per track with sensible defaults.
    trex_boxes = b"".join(
        full_box("trex", 0, 0,
                 u32(track_id) + u32(1) + u32(0) + u32(0) + u32(0))
        for track_id in range(1, len(timeline.tracks) + 1))
    mvex = box("mvex", trex_boxes)
    return box("moov", mvhd + traks + mvex)


def _build_traf(track_id: int, track: TrackTimeline, samples: list[TimedSample],
                base_dts: int) -> bytes:
    tfhd = full_box("tfhd", 0, TFHD_DEFAULT_BASE | TFHD_DEFAULT_FLAGS,
                    u32(track_id) + u32(_sample_flags(samples[0])))
    tfdt = full_box("tfdt", 1, 0, u64(base_dts))
    first_flags = _sample_flags(samples[0])
    entries = b""
    for sample in samples:
        entries += (u32(sample.duration_ms) + u32(len(sample.data)) +
                    u32(_sample_flags(sample)) +
                    i32(sample.composition_offset_ms))
    # trun payload without the data-offset field; patched by the caller once
    # the whole moof size is known.
    trun_payload = (u32(len(samples)) + u32(0) + u32(first_flags) + entries)
    trun = full_box("trun", 0, TRUN_BASE_FLAGS, trun_payload)
    return box("traf", tfhd + tfdt + trun)


def _patch_trun_data_offset(moof: bytes, offset: int) -> bytes:
    """Write the mdat-relative data offset into the moof's first trun."""
    trun_pos = moof.find(b"trun")
    if trun_pos < 0:
        raise IndexValidationError("moof contains no trun box")
    # Layout at trun fourcc: [trun][ver/flags 4][sample_count 4][data_off 4]
    field = trun_pos + 4 + 4 + 4
    return moof[:field] + u32(offset) + moof[field + 4:]


def _split_fragments(timeline: Timeline) -> list[dict]:
    """Group samples into fragments; all tracks of a time span share one moof.

    Samples are split on track boundaries into per-track ``traf`` boxes but a
    fragment is emitted whenever any track crosses the fragment size target.
    """
    target = max(1, min(8, max(len(t.samples) for t in timeline.tracks)))
    fragments: list[dict] = []
    cursors = {t.track.number: 0 for t in timeline.tracks}
    while any(cursors[t.track.number] < len(t.samples) for t in timeline.tracks):
        group: list[tuple[int, TrackTimeline, list[TimedSample]]] = []
        for track_id, track in enumerate(timeline.tracks, start=1):
            start = cursors[track.track.number]
            end = min(start + target, len(track.samples))
            chunk = track.samples[start:end]
            # A track whose samples are exhausted contributes no traf in this
            # fragment; empty groups would break the moof/trun invariants.
            if chunk:
                group.append((track_id, track, chunk))
            cursors[track.track.number] = end
        if group:
            fragments.append({"tracks": group})
    return fragments


def export_fragmented(timeline: Timeline, output_path: str) -> FragmentBuildResult:
    """Build, validate and atomically write a fragmented MP4 file."""
    ftyp = box("ftyp", b"iso5" + u32(0x200) + b"iso5dashmp41")
    moov = _build_init_moov(timeline)

    fragments = _split_fragments(timeline)
    if not fragments:
        raise IndexValidationError("no fragments to export")

    body = bytearray()
    sidx_entries: list[tuple[int, int, int]] = []
    total_samples = 0
    # sidx references are relative to the first fragment's byte offset.
    data_origin = len(ftyp) + len(moov)
    # sidx is a full box: 8 header + 4 ver/flags + 16 fixed + 2 reserved +
    # 2 reference_count + 12 bytes per reference.
    sidx_placeholder_size = 8 + 4 + 16 + 4 + len(fragments) * 12
    first_fragment_offset = data_origin + sidx_placeholder_size
    running_offset = first_fragment_offset

    for fragment in fragments:
        trafs = b""
        mdat_payload = b""
        earliest_dts = None
        frag_duration = 0
        for track_id, track, samples in fragment["tracks"]:
            if not samples:
                continue
            base_dts = samples[0].dts_ms
            if earliest_dts is None or base_dts < earliest_dts:
                earliest_dts = base_dts
            traf = _build_traf(track_id, track, samples, base_dts)
            trafs += traf
            mdat_payload += b"".join(s.data for s in samples)
            frag_duration = max(
                frag_duration,
                samples[-1].dts_ms + samples[-1].duration_ms - base_dts)
            total_samples += len(samples)
        if earliest_dts is None:
            raise IndexValidationError("fragment contains no samples")
        mfhd = full_box("mfhd", 0, 0, u32(len(sidx_entries) + 1))
        moof = box("moof", mfhd + trafs)
        # trun data offset is relative to the moof start and points at the
        # first media byte inside the following mdat.
        data_offset = len(moof) + 8
        moof = _patch_trun_data_offset(moof, data_offset)
        mdat = box("mdat", mdat_payload)
        fragment_bytes = moof + mdat
        sidx_entries.append((running_offset, len(fragment_bytes), frag_duration))
        body += fragment_bytes
        running_offset += len(fragment_bytes)

    sidx = _build_sidx(timeline, sidx_entries, first_fragment_offset)
    image = ftyp + moov + sidx + bytes(body)
    _post_validate(image, timeline, fragments, first_fragment_offset)
    write_file_atomic(output_path, image)
    return FragmentBuildResult(
        path=output_path,
        fragment_count=len(fragments),
        sidx_refs=len(sidx_entries),
        total_samples=total_samples,
    )


def _build_sidx(timeline: Timeline, entries: list[tuple[int, int, int]],
                first_fragment_offset: int) -> bytes:
    # One reference type 0 (media) per fragment; SAP flags mark every
    # fragment start as a stream access point (keyframe-aligned).
    payload = (u32(0) + u32(MOVIE_TIMESCALE) + u32(0) + u32(0) +
               u16(0) + u16(len(entries)))
    for referenced_offset, size, duration in entries:
        sap_flags = 0x90000000  # reference_type=media, SAP type 1
        payload += (u32(sap_flags) + u32(size) + u32(duration) +
                    u32(referenced_offset - first_fragment_offset))
    return full_box("sidx", 1, 0, payload)


def _post_validate(image: bytes, timeline: Timeline, fragments: list,
                   first_fragment_offset: int) -> None:
    index = validate_structure(
        image, expected=["ftyp", "moov", "sidx", "moof", "mdat"])
    for required in ("mvex", "trex", "tfhd", "tfdt", "trun"):
        if required not in index:
            raise IndexValidationError(f"fragmented file missing {required}")
    # Every fragment must pair one moof with one mdat at top level.
    from .atoms import parse_boxes
    top = parse_boxes(image, 0, len(image))
    fragment_tops = [b for b in top if b.box_type in ("moof", "mdat")]
    moof_count = sum(1 for b in fragment_tops if b.box_type == "moof")
    mdat_count = sum(1 for b in fragment_tops if b.box_type == "mdat")
    if moof_count != len(fragments) or mdat_count != len(fragments):
        raise IndexValidationError(
            f"fragment box mismatch: {moof_count} moof, {mdat_count} mdat, "
            f"expected {len(fragments)} of each")
    # The pairs must interleave strictly: moof then its mdat.
    for pair_index, box in enumerate(fragment_tops):
        expected_type = "moof" if pair_index % 2 == 0 else "mdat"
        if box.box_type != expected_type:
            raise IndexValidationError(
                f"fragment {pair_index // 2} is not ordered moof->mdat")
    # payload_start includes the 4-byte full-box header. Body layout:
    # ref_ID(4) timescale(4) earliest_pts(8) earliest_dts(8) = 24 bytes,
    # then reserved(2) at [20:22] and reference_count(2) at [22:24].
    sidx = index["sidx"]
    payload = image[sidx.payload_start:sidx.payload_end]
    ref_count = int.from_bytes(payload[22:24], byteorder="big")
    if ref_count != len(fragments):
        raise IndexValidationError(
            f"sidx references {ref_count} fragments, expected {len(fragments)}")
