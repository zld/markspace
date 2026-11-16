"""ISO Base Media File Format (ISO/IEC 14496-12) box writer and reader.

Layout strategy: the index (``ftyp`` + ``moov``) is reserved first and the
``mdat`` sample offsets are back-filled. Chunk offsets use ``stco`` while
they fit in 32 bits and are automatically rebuilt as ``co64`` otherwise.

The writer keeps a per-track :class:`TrackOutput` of decode-ordered samples
and chunks; all sample-table boxes are run-length encoded from it.
"""

from __future__ import annotations

from dataclasses import dataclass, field

from .codec import CodecMapping
from .errors import ConversionError, VerificationError
from .timeline import Sample

UINT32_MAX = (1 << 32) - 1
UINT64_MAX = (1 << 64) - 1


def box(box_type: bytes, payload: bytes) -> bytes:
    if len(box_type) != 4:
        raise ConversionError(f"box type must be four bytes: {box_type!r}")
    size = 8 + len(payload)
    if size > UINT32_MAX:
        raise ConversionError(
            f"box {box_type!r} too large for 32-bit size ({size} bytes)")
    return size.to_bytes(4, "big") + box_type + payload


def fullbox(box_type: bytes, version: int, flags: int,
            payload: bytes) -> bytes:
    header = bytes([version]) + flags.to_bytes(3, "big")
    return box(box_type, header + payload)


def _runs(values: list[int]) -> list[tuple[int, int]]:
    """Run-length encode consecutive equal values -> (value, count)."""
    runs: list[tuple[int, int]] = []
    for value in values:
        if runs and runs[-1][0] == value:
            runs[-1] = (value, runs[-1][1] + 1)
        else:
            runs.append((value, 1))
    return runs


@dataclass
class Chunk:
    offset: int          # absolute file offset of the chunk's first byte
    first_sample: int    # index into TrackOutput.samples
    sample_count: int


@dataclass
class TrackOutput:
    track_id: int
    mapping: CodecMapping
    timescale: int
    language: str
    samples: list[Sample] = field(default_factory=list)
    sample_bytes: list[bytes] = field(default_factory=list)
    chunks: list[Chunk] = field(default_factory=list)

    def add_sample(self, data: bytes, sample: Sample) -> None:
        self.samples.append(sample)
        self.sample_bytes.append(data)


# --- ftyp -------------------------------------------------------------------

def build_ftyp() -> bytes:
    # isom/iso2/mp41 brands; Opus-in-MP4 additionally implies the Opus brand
    # at the moov level, but ftyp stays on the widely compatible baseline.
    payload = (b"isom" + (0x200).to_bytes(4, "big")
               + b"isom" + b"iso2" + b"mp41")
    return box(b"ftyp", payload)


# --- sample description (stsd) ---------------------------------------------

def _video_stsd_entry(mapping: CodecMapping) -> bytes:
    reserved = b"\x00" * 6
    dref_index = (1).to_bytes(2, "big")
    base = (reserved + dref_index + b"\x00" * 16
            + mapping.width.to_bytes(2, "big")
            + mapping.height.to_bytes(2, "big")
            + (0x00480000).to_bytes(4, "big")   # horiz resolution 72 dpi
            + (0x00480000).to_bytes(4, "big")   # vert resolution 72 dpi
            + (0).to_bytes(4, "big")            # reserved
            + (1).to_bytes(2, "big")            # frame count
            + b"\x00" * 32                      # compressor name
            + (0x0018).to_bytes(2, "big")       # depth
            + (0xFFFF).to_bytes(2, "big"))      # pre-defined
    config = box(mapping.config_box_type, mapping.config_payload)
    return box(mapping.entry_type, base + config)


def _audio_stsd_entry(mapping: CodecMapping) -> bytes:
    reserved = b"\x00" * 6
    dref_index = (1).to_bytes(2, "big")
    base = (reserved + dref_index + b"\x00" * 8
            + mapping.channels.to_bytes(2, "big")
            + mapping.sample_size.to_bytes(2, "big")
            + (0).to_bytes(2, "big")                     # pre-defined
            + (0).to_bytes(2, "big")                     # reserved
            + (mapping.sample_rate << 16).to_bytes(4, "big"))
    if mapping.config_box_type == b"esds":
        config = fullbox(b"esds", 0, 0, mapping.config_payload)
    else:  # dOps and similar are plain boxes, no full-box header
        config = box(mapping.config_box_type, mapping.config_payload)
    return box(mapping.entry_type, base + config)


def build_stsd(track: TrackOutput) -> bytes:
    if track.mapping.handler_type == "vide":
        entry = _video_stsd_entry(track.mapping)
    else:
        entry = _audio_stsd_entry(track.mapping)
    payload = (1).to_bytes(4, "big") + entry
    return fullbox(b"stsd", 0, 0, payload)


# --- remaining sample-table boxes ------------------------------------------

def build_stts(track: TrackOutput) -> bytes:
    runs = _runs([s.duration for s in track.samples])
    payload = len(runs).to_bytes(4, "big")
    for duration, count in runs:
        payload += count.to_bytes(4, "big") + duration.to_bytes(4, "big")
    return fullbox(b"stts", 0, 0, payload)


def build_ctts(track: TrackOutput) -> bytes | None:
    if not any(s.cts_offset for s in track.samples):
        return None
    runs = _runs([s.cts_offset for s in track.samples])
    payload = len(runs).to_bytes(4, "big")
    # Version 1 keeps offsets signed, which negative B-frame ctts requires.
    for offset, count in runs:
        payload += count.to_bytes(4, "big") + offset.to_bytes(4, "big",
                                                              signed=True)
    return fullbox(b"ctts", 1, 0, payload)


def build_stsc(track: TrackOutput) -> bytes:
    runs = _runs([c.sample_count for c in track.chunks])
    payload = len(runs).to_bytes(4, "big")
    first_chunk = 1
    for samples_per_chunk, run_count in runs:
        payload += (first_chunk.to_bytes(4, "big")
                    + samples_per_chunk.to_bytes(4, "big")
                    + (1).to_bytes(4, "big"))
        first_chunk += run_count
    return fullbox(b"stsc", 0, 0, payload)


def build_stsz(track: TrackOutput) -> bytes:
    sizes = [len(b) for b in track.sample_bytes]
    payload = (0).to_bytes(4, "big") + len(sizes).to_bytes(4, "big")
    payload += b"".join(size.to_bytes(4, "big") for size in sizes)
    return fullbox(b"stsz", 0, 0, payload)


def build_stss(track: TrackOutput) -> bytes | None:
    keyframes = [i + 1 for i, s in enumerate(track.samples) if s.keyframe]
    if not keyframes:
        return None
    payload = len(keyframes).to_bytes(4, "big")
    payload += b"".join(n.to_bytes(4, "big") for n in keyframes)
    return fullbox(b"stss", 0, 0, payload)


def build_stco(track: TrackOutput, use_co64: bool) -> bytes:
    box_type = b"co64" if use_co64 else b"stco"
    width = 8 if use_co64 else 4
    payload = len(track.chunks).to_bytes(4, "big")
    for chunk in track.chunks:
        payload += chunk.offset.to_bytes(width, "big")
    return fullbox(box_type, 0, 0, payload)


def needs_co64(tracks: list[TrackOutput]) -> bool:
    return any(c.offset > UINT32_MAX for t in tracks for c in t.chunks)


def build_stbl(track: TrackOutput, use_co64: bool) -> bytes:
    parts = [build_stsd(track), build_stts(track)]
    ctts = build_ctts(track)
    if ctts is not None:
        parts.append(ctts)
    parts.append(build_stsc(track))
    parts.append(build_stsz(track))
    stss = build_stss(track)
    if stss is not None:
        parts.append(stss)
    parts.append(build_stco(track, use_co64))
    return box(b"stbl", b"".join(parts))


# --- minimal read-back primitives used by verify ---------------------------

@dataclass
class RawBox:
    type: bytes
    start: int
    header_size: int
    payload_start: int
    payload_end: int


def iter_boxes(data: bytes, start: int, end: int) -> list[RawBox]:
    boxes: list[RawBox] = []
    pos = start
    while pos < end:
        if pos + 8 > end:
            raise VerificationError(
                f"truncated box header at offset 0x{pos:X}", offset=pos)
        size = int.from_bytes(data[pos:pos + 4], "big")
        box_type = data[pos + 4:pos + 8]
        header = 8
        if size == 1:
            if pos + 16 > end:
                raise VerificationError("truncated 64-bit box size",
                                        offset=pos)
            size = int.from_bytes(data[pos + 8:pos + 16], "big")
            header = 16
        elif size == 0:
            size = end - pos
        if size < header or pos + size > end:
            raise VerificationError(
                f"box {box_type!r} size {size} crosses parent end 0x{end:X}",
                offset=pos)
        boxes.append(RawBox(box_type, pos, header, pos + header,
                            pos + size))
        pos += size
    return boxes


def find_box(data: bytes, start: int, end: int,
             box_type: bytes) -> RawBox | None:
    return next((b for b in iter_boxes(data, start, end)
                 if b.type == box_type), None)


# --- moov / trak assembly ---------------------------------------------------

def _language_code(language: str) -> int:
    """ISO 639-2/T packed language; fall back to 'und' (0x55C4)."""
    if len(language) >= 3 and language[:3].isalpha():
        letters = [ord(c) - 0x60 for c in language[:3].lower()]
        if all(1 <= n <= 26 for n in letters):
            return (letters[0] << 10) | (letters[1] << 5) | letters[2]
    return 0x55C4


def build_mvhd(tracks: list[TrackOutput]) -> bytes:
    timescale = 1000
    duration = 0
    for track in tracks:
        if not track.samples:
            continue
        last = track.samples[-1]
        end = ((last.dts + last.duration) * timescale) // track.timescale
        duration = max(duration, end)
    payload = (
        (0).to_bytes(4, "big") + (0).to_bytes(4, "big")   # creation/mod time
        + timescale.to_bytes(4, "big")
        + duration.to_bytes(4, "big")
        + (0x00010000).to_bytes(4, "big")                  # rate 1.0
        + (0x0100).to_bytes(2, "big")                      # volume 1.0
        + b"\x00" * 10                                     # reserved
    )
    matrix = (0x00010000, 0, 0, 0, 0x00010000, 0, 0, 0, 0x40000000)
    payload += b"".join(v.to_bytes(4, "big", signed=True) for v in matrix)
    payload += b"\x00" * 24                               # pre-defined
    payload += (len(tracks)).to_bytes(4, "big")
    payload += b"".join((i + 1).to_bytes(4, "big")
                        for i in range(len(tracks)))
    return fullbox(b"mvhd", 0, 0, payload)


def build_tkhd(track: TrackOutput, duration_mvhd: int) -> bytes:
    mapping = track.mapping
    payload = (
        (0).to_bytes(4, "big") + (0).to_bytes(4, "big")
        + track.track_id.to_bytes(4, "big") + (0).to_bytes(4, "big")
        + duration_mvhd.to_bytes(4, "big")
        + b"\x00" * 8                                     # reserved
        + (0).to_bytes(2, "big")                          # layer
        + (0).to_bytes(2, "big")                          # alternate group
        + ((0x0100 if mapping.handler_type == "soun" else 0)
           ).to_bytes(2, "big")                           # volume
        + (0).to_bytes(2, "big")                          # reserved
    )
    matrix = (0x00010000, 0, 0, 0, 0x00010000, 0, 0, 0, 0x40000000)
    payload += b"".join(v.to_bytes(4, "big", signed=True) for v in matrix)
    payload += (mapping.width << 16).to_bytes(4, "big")
    payload += (mapping.height << 16).to_bytes(4, "big")
    flags = 0x03 if mapping.handler_type == "soun" else 0x01
    return fullbox(b"tkhd", 0, flags, payload)


def build_mdhd(track: TrackOutput) -> bytes:
    if not track.samples:
        duration = 0
    else:
        last = track.samples[-1]
        duration = last.dts + last.duration
    payload = (
        (0).to_bytes(4, "big") + (0).to_bytes(4, "big")
        + track.timescale.to_bytes(4, "big")
        + duration.to_bytes(4, "big")
        + _language_code(track.language).to_bytes(2, "big")
        + (0).to_bytes(2, "big")                          # pre-defined
    )
    return fullbox(b"mdhd", 0, 0, payload)


def build_hdlr(track: TrackOutput) -> bytes:
    handler = track.mapping.handler_type
    name = (f"mkvmp4 {handler} handler").encode("utf-8") + b"\x00"
    payload = (b"\x00" * 4 + handler.encode("ascii")
               + b"\x00" * 12 + name)
    return fullbox(b"hdlr", 0, 0, payload)


def build_minf(track: TrackOutput, use_co64: bool) -> bytes:
    if track.mapping.handler_type == "vide":
        media_header = fullbox(b"vmhd", 0, 1,
                               b"\x00" * 8)
    else:
        media_header = fullbox(b"smhd", 0, 0,
                               (0).to_bytes(2, "big") + (0).to_bytes(2, "big"))
    dref = fullbox(b"dref", 0, 0,
                   (1).to_bytes(4, "big")
                   + fullbox(b"url ", 0, 1, b""))
    dinf = box(b"dinf", dref)
    stbl = build_stbl(track, use_co64)
    return box(b"minf", media_header + dinf + stbl)


def build_trak(track: TrackOutput, duration_mvhd: int,
               use_co64: bool) -> bytes:
    return box(b"trak",
               build_tkhd(track, duration_mvhd)
               + box(b"mdia",
                     build_mdhd(track) + build_hdlr(track)
                     + build_minf(track, use_co64)))


def _mvhd_duration(tracks: list[TrackOutput]) -> int:
    duration = 0
    for track in tracks:
        if not track.samples:
            continue
        last = track.samples[-1]
        end = ((last.dts + last.duration) * 1000) // track.timescale
        duration = max(duration, end)
    return duration


def build_moov(tracks: list[TrackOutput], use_co64: bool,
               manifest: bytes | None = None) -> bytes:
    duration = _mvhd_duration(tracks)
    payload = build_mvhd(tracks)
    for track in tracks:
        payload += build_trak(track, duration, use_co64)
    moov = box(b"moov", payload)
    if manifest is not None:
        # Custom top-level companion box, kept adjacent to the index.
        moov += box(b"mkvm", manifest)
    return moov


# --- writing: reserved index, back-filled offsets, segmented parts ---------

SEGMENT_TARGET_BYTES = 1 * 1024 * 1024
COPY_CHUNK_BYTES = 1 * 1024 * 1024


@dataclass
class _WritePlan:
    # One entry per mdat write unit: (track index, first sample, count,
    # segment index). Consecutive entries for the same track+segment merge
    # into a single chunk.
    units: list[tuple[int, int, int, int]]
    part_paths: list[str]
    mdat_data_size: int
    mdat_header_size: int
    use_co64: bool


def _plan_writes(tracks: list[TrackOutput], temp_dir: str,
                 segmented: bool) -> _WritePlan:
    """Lay samples out into one mdat (single) or N part files (segmented)."""
    target = SEGMENT_TARGET_BYTES if segmented else None
    units: list[tuple[int, int, int, int]] = []
    part_paths: list[str] = []
    segment = 0
    segment_bytes = 0
    total = 0
    part_path = f"{temp_dir}.part0"
    part_paths.append(part_path)
    for ti, track in enumerate(tracks):
        first = 0
        count = 0
        for si, payload in enumerate(track.sample_bytes):
            size = len(payload)
            if target is not None and count > 0 \
                    and segment_bytes + size > target:
                # Close this track's run and the current segment.
                units.append((ti, first, count, segment))
                first = si
                count = 0
                segment += 1
                segment_bytes = 0
                part_paths.append(f"{temp_dir}.part{segment}")
            if count == 0:
                first = si
            count += 1
            segment_bytes += size
            total += size
        if count:
            units.append((ti, first, count, segment))
    mdat_header_size = 16 if total > UINT32_MAX - 8 else 8
    # Estimate the highest offset to fix the stco/co64 choice before the
    # index size is measured (the box widths must not change afterwards).
    ftyp_size = len(build_ftyp())
    estimated_moov = 4096 + sum(
        400 + 20 * len(t.samples) + 12 * len(t.chunks) for t in tracks)
    max_offset = (ftyp_size + estimated_moov + mdat_header_size
                  + total)
    use_co64 = max_offset > UINT32_MAX
    return _WritePlan(units, part_paths, total, mdat_header_size,
                      use_co64)


def _build_chunks(tracks: list[TrackOutput], plan: _WritePlan) -> None:
    """Populate each track's chunk list with mdat-relative offsets.

    Offsets are relative to the mdat payload start (so the first chunk is
    at 0); the writer adds the absolute payload start afterwards. Building
    structure before measuring moov keeps every sample-table box's shape
    independent of the final absolute offsets.
    """
    for track in tracks:
        track.chunks.clear()
    cursor = 0  # relative to mdat payload start
    last_key: tuple[int, int] | None = None
    for ti, first, count, segment in plan.units:
        track = tracks[ti]
        key = (ti, segment)
        run_size = sum(
            len(track.sample_bytes[si])
            for si in range(first, first + count))
        if last_key == key:
            # Continuation of the same track within one segment: merge into
            # the previous chunk instead of starting a new one.
            track.chunks[-1].sample_count += count
        else:
            track.chunks.append(
                Chunk(offset=cursor, first_sample=first,
                      sample_count=count))
        cursor += run_size
        last_key = key
    return cursor  # total mdat payload bytes, cross-checked by the writer


class Mp4Writer:
    """Writes ftyp + moov (reserved first) then mdat with back-filled offsets.

    Segmented mode streams samples into ``.partN`` temp files, then merges
    the parts and the per-track sample tables into one final file.
    """

    def __init__(self, tracks: list[TrackOutput], temp_path: str,
                 segmented: bool, manifest: bytes | None = None) -> None:
        if not tracks:
            raise ConversionError("refusing to write an MP4 with no tracks")
        if any(not t.samples for t in tracks):
            raise ConversionError("refusing to write a track with no samples")
        self.tracks = tracks
        self.temp_path = temp_path
        self.segmented = segmented
        self.manifest = manifest
        self.plan = _plan_writes(tracks, temp_path, segmented)

    def _write_parts(self) -> None:
        handles = {p: open(p, "wb") for p in self.plan.part_paths}
        try:
            for ti, first, count, segment in self.plan.units:
                handle = handles[self.plan.part_paths[segment]]
                track = self.tracks[ti]
                for si in range(first, first + count):
                    handle.write(track.sample_bytes[si])
        finally:
            for handle in handles.values():
                handle.close()

    def write(self) -> dict:
        self._write_parts()
        ftyp = build_ftyp()
        # Build chunk structure with mdat-relative offsets first; the shape
        # of every sample-table box is then independent of absolute offsets.
        planned_total = _build_chunks(self.tracks, self.plan)
        if planned_total != self.plan.mdat_data_size:
            raise ConversionError(
                "chunk planner and part writer disagree on mdat size")
        placeholder_moov = build_moov(
            self.tracks, self.plan.use_co64, self.manifest)
        mdat_payload_start = (
            len(ftyp) + len(placeholder_moov)
            + self.plan.mdat_header_size)
        # Convert relative offsets to absolute without changing cardinality,
        # so stco/co64 widths and the moov byte size stay identical.
        for track in self.tracks:
            for chunk in track.chunks:
                chunk.offset += mdat_payload_start
        moov = build_moov(self.tracks, self.plan.use_co64, self.manifest)
        if len(moov) != len(placeholder_moov):
            raise ConversionError(
                "moov size changed between reservation and back-fill")
        total_size = mdat_payload_start + self.plan.mdat_data_size
        if self.plan.mdat_header_size == 16:
            mdat_header = (1).to_bytes(4, "big") + b"mdat" \
                + total_size.to_bytes(8, "big")
        else:
            mdat_header = (
                8 + self.plan.mdat_data_size).to_bytes(4, "big") + b"mdat"
        with open(self.temp_path, "wb") as out:
            out.write(ftyp)
            out.write(moov)
            out.write(mdat_header)
            for part in self.plan.part_paths:
                with open(part, "rb") as src:
                    while True:
                        block = src.read(COPY_CHUNK_BYTES)
                        if not block:
                            break
                        out.write(block)
        return {
            "path": self.temp_path,
            "size": total_size,
            "segments": len(self.plan.part_paths),
            "co64": self.plan.use_co64,
            "mdat_payload_start": mdat_payload_start,
        }

    def cleanup_parts(self) -> None:
        import os
        for part in self.plan.part_paths:
            try:
                os.remove(part)
            except FileNotFoundError:
                pass


# --- read-back: reconstruct the index for verify ---------------------------

@dataclass
class TrackIndex:
    track_id: int
    handler_type: str
    timescale: int
    sample_count: int
    sample_sizes: list[int]
    chunk_offsets: list[int]
    chunk_samples: list[int]          # samples per chunk, expanded
    stts_entries: list[tuple[int, int]]
    ctts_entries: list[tuple[int, int]]
    stss_entries: list[int]
    duration: int

    def sample_span(self) -> tuple[int, int]:
        """Absolute (start, end) byte range covered by every sample."""
        if not self.chunk_offsets or not self.sample_sizes:
            raise VerificationError("track has no chunks or samples")
        starts: list[int] = []
        ends: list[int] = []
        sample_i = 0
        for chunk_index, offset in enumerate(self.chunk_offsets):
            per_chunk = self.chunk_samples[chunk_index]
            cursor = offset
            for _ in range(per_chunk):
                size = self.sample_sizes[sample_i]
                starts.append(cursor)
                cursor += size
                ends.append(cursor)
                sample_i += 1
        if sample_i != self.sample_count:
            raise VerificationError(
                f"stsc/stsz cover {sample_i} samples but stsz declares "
                f"{self.sample_count}")
        return min(starts), max(ends)

    def timing_line(self) -> list[tuple[int, int]]:
        """Expand stts (+ctts) into per-sample (dts, pts) in track ticks."""
        dts_list: list[int] = []
        dts = 0
        for count, delta in self.stts_entries:
            for _ in range(count):
                dts_list.append(dts)
                dts += delta
        if len(dts_list) != self.sample_count:
            raise VerificationError(
                "stts entry count does not match sample count")
        ctts_flat: list[int] = []
        for count, offset in self.ctts_entries:
            ctts_flat.extend([offset] * count)
        if ctts_flat and len(ctts_flat) != self.sample_count:
            raise VerificationError(
                "ctts entry count does not match sample count")
        pts_list = [dts_list[i] + (ctts_flat[i] if ctts_flat else 0)
                    for i in range(self.sample_count)]
        return list(zip(dts_list, pts_list))


def _read_fullbox_header(data: bytes, box_obj: RawBox
                         ) -> tuple[int, int]:
    version = data[box_obj.payload_start]
    flags = int.from_bytes(
        data[box_obj.payload_start + 1:box_obj.payload_start + 4], "big")
    return version, flags


def _read_stbl(data: bytes, stbl: RawBox) -> dict:
    boxes = {b.type: b for b in iter_boxes(
        data, stbl.payload_start, stbl.payload_end)}
    required = (b"stsd", b"stts", b"stsc", b"stsz", b"stco")
    missing = [t for t in required
               if t not in boxes and not (t == b"stco" and b"co64" in boxes)]
    if missing:
        raise VerificationError(
            f"stbl missing required boxes: {[t.decode() for t in missing]}")

    stts_box = boxes[b"stts"]
    pos = stts_box.payload_start + 4
    entry_count = int.from_bytes(data[pos:pos + 4], "big")
    pos += 4
    stts_entries: list[tuple[int, int]] = []
    for _ in range(entry_count):
        count = int.from_bytes(data[pos:pos + 4], "big")
        delta = int.from_bytes(data[pos + 4:pos + 8], "big")
        stts_entries.append((count, delta))
        pos += 8

    ctts_entries: list[tuple[int, int]] = []
    if b"ctts" in boxes:
        ctts_box = boxes[b"ctts"]
        version, _flags = _read_fullbox_header(data, ctts_box)
        pos = ctts_box.payload_start + 4
        entry_count = int.from_bytes(data[pos:pos + 4], "big")
        pos += 4
        signed = version == 1
        for _ in range(entry_count):
            count = int.from_bytes(data[pos:pos + 4], "big")
            offset = int.from_bytes(
                data[pos + 4:pos + 8], "big", signed=signed)
            ctts_entries.append((count, offset))
            pos += 8

    stsc_box = boxes[b"stsc"]
    pos = stsc_box.payload_start + 4
    entry_count = int.from_bytes(data[pos:pos + 4], "big")
    pos += 4
    stsc_raw: list[tuple[int, int, int]] = []
    for _ in range(entry_count):
        first_chunk = int.from_bytes(data[pos:pos + 4], "big")
        spc = int.from_bytes(data[pos + 4:pos + 8], "big")
        pos += 12
        stsc_raw.append((first_chunk, spc, 1))

    stsz_box = boxes[b"stsz"]
    pos = stsz_box.payload_start + 4
    pos += 4  # skip sample_size (uniform-size field, always 0 here)
    sample_count = int.from_bytes(data[pos:pos + 4], "big")
    pos += 4
    sample_sizes = [
        int.from_bytes(data[pos + 4 * i:pos + 4 * i + 4], "big")
        for i in range(sample_count)]

    offset_box = boxes.get(b"stco") or boxes[b"co64"]
    width = 4 if offset_box.type == b"stco" else 8
    pos = offset_box.payload_start + 4
    chunk_count = int.from_bytes(data[pos:pos + 4], "big")
    pos += 4
    chunk_offsets = [
        int.from_bytes(
            data[pos + width * i:pos + width * i + width], "big")
        for i in range(chunk_count)]

    # Expand stsc first-chunk run lists into per-chunk sample counts.
    chunk_samples = [0] * chunk_count
    for run_i, (first_chunk, spc, _desc) in enumerate(stsc_raw):
        end_chunk = (stsc_raw[run_i + 1][0] - 1
                     if run_i + 1 < len(stsc_raw) else chunk_count)
        for chunk_i in range(first_chunk - 1, end_chunk):
            chunk_samples[chunk_i] = spc
    if any(c == 0 for c in chunk_samples):
        raise VerificationError("stsc does not describe every chunk")

    stss_entries: list[int] = []
    if b"stss" in boxes:
        stss_box = boxes[b"stss"]
        pos = stss_box.payload_start + 4
        entry_count = int.from_bytes(data[pos:pos + 4], "big")
        pos += 4
        stss_entries = [
            int.from_bytes(data[pos + 4 * i:pos + 4 * i + 4], "big")
            for i in range(entry_count)]
    return {
        "sample_count": sample_count,
        "sample_sizes": sample_sizes,
        "chunk_offsets": chunk_offsets,
        "chunk_samples": chunk_samples,
        "stts_entries": stts_entries,
        "ctts_entries": ctts_entries,
        "stss_entries": stss_entries,
    }


def read_mp4_index(data: bytes) -> tuple[list[TrackIndex], RawBox]:
    """Parse the written MP4 far enough to reconstruct every track index."""
    top = iter_boxes(data, 0, len(data))
    top_types = [b.type for b in top]
    if b"ftyp" not in top_types or b"moov" not in top_types \
            or b"mdat" not in top_types:
        raise VerificationError(
            "top-level ftyp/moov/mdat boxes are not all present")
    moov = next(b for b in top if b.type == b"moov")
    mdat = next(b for b in top if b.type == b"mdat")
    traks = [b for b in iter_boxes(
        data, moov.payload_start, moov.payload_end) if b.type == b"trak"]
    if not traks:
        raise VerificationError("moov contains no trak boxes")
    indexes: list[TrackIndex] = []
    for trak in traks:
        tkhd = find_box(data, trak.payload_start, trak.payload_end, b"tkhd")
        if tkhd is None:
            raise VerificationError("trak missing tkhd")
        # tkhd is a full box: payload begins with a 4-byte version/flags
        # header, then creation/modification time (8) and track_ID (4).
        track_id = int.from_bytes(
            data[tkhd.payload_start + 12:tkhd.payload_start + 16], "big")
        mdia = find_box(data, trak.payload_start, trak.payload_end, b"mdia")
        if mdia is None:
            raise VerificationError("trak missing mdia")
        mdhd = find_box(data, mdia.payload_start, mdia.payload_end, b"mdhd")
        hdlr = find_box(data, mdia.payload_start, mdia.payload_end, b"hdlr")
        minf = find_box(data, mdia.payload_start, mdia.payload_end, b"minf")
        if mdhd is None or hdlr is None or minf is None:
            raise VerificationError("mdia missing mdhd/hdlr/minf")
        version = data[mdhd.payload_start]
        if version != 0:
            raise VerificationError(f"unsupported mdhd version {version}")
        timescale = int.from_bytes(
            data[mdhd.payload_start + 12:mdhd.payload_start + 16], "big")
        duration = int.from_bytes(
            data[mdhd.payload_start + 16:mdhd.payload_start + 20], "big")
        handler_type = data[hdlr.payload_start + 8:
                           hdlr.payload_start + 12]
        stbl = find_box(data, minf.payload_start, minf.payload_end, b"stbl")
        if stbl is None:
            raise VerificationError("minf missing stbl")
        parsed = _read_stbl(data, stbl)
        indexes.append(TrackIndex(
            track_id=track_id,
            handler_type=handler_type.decode("latin-1"),
            timescale=timescale,
            duration=duration,
            **parsed))
    return indexes, mdat
