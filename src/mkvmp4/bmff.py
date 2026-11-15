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

