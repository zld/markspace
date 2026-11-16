"""Matroska block decoding: SimpleBlock/Block headers and all lacing modes.

A Matroska block body is::

    TrackNumber (VINT) | RelativeTimecode (int16 BE) | Flags (uint8)
    [lacing header]    | frame payload(s)

Lacing stores several consecutive samples of *one* track in a single block.
Three modes are supported, exactly as the spec defines them:

* Xiph lacing: 255-terminated byte counts plus one trailing remainder frame;
* fixed-size lacing: every frame shares the first frame's size;
* EBML lacing: unsigned VINT deltas with signed (zig-zag) first value.

Relative timecodes are signed and may be negative; the absolute PTS is
``Cluster.Timestamp + relative``. De-interlacing yields one
:class:`Sample` per encoded frame with key-frame flags preserved.
"""

from dataclasses import dataclass

from ..ebml.vint import decode_vint
from ..errors import CorruptMKVError, LimitExceededError
from .segment import MatroskaFile, RawBlock, TrackInfo

#: Flag bits inside a block's flags octet.
FLAG_KEYFRAME = 0x80
LACING_MASK = 0x06
LACING_SHIFT = 1
LACING_NONE = 0
LACING_XIPH = 1
LACING_FIXED = 2
LACING_EBML = 3


@dataclass(slots=True)
class Sample:
    """One de-interlaced encoded sample (NAL units or an AAC frame)."""

    track_number: int
    pts_ticks: int           # absolute, in TimestampScale ticks
    data: bytes
    keyframe: bool
    duration_ticks: int | None = None  # only known for BlockGroup blocks


def _read_track_vint(body: bytes, offset: int) -> tuple[int, int]:
    value, width, next_offset = decode_vint(body, offset)
    if value < 0:
        raise CorruptMKVError("block track number uses unknown-length VINT",
                              offset=offset)
    return value, next_offset


def _parse_header(body: bytes) -> tuple[int, int, int, int]:
    """Return (track_number, relative_tc, flags, payload_offset)."""
    track_number, pos = _read_track_vint(body, 0)
    if len(body) < pos + 3:
        raise CorruptMKVError(
            f"block body truncated: need 3 header bytes at offset {pos}")
    relative_tc = int.from_bytes(body[pos:pos + 2], byteorder="big", signed=True)
    flags = body[pos + 2]
    return track_number, relative_tc, flags, pos + 3


def _xiph_sizes(body: bytes, offset: int, frame_count: int
                ) -> tuple[list[int], int]:
    sizes: list[int] = []
    pos = offset
    for _ in range(frame_count - 1):
        total = 0
        while True:
            if pos >= len(body):
                raise CorruptMKVError("Xiph lacing size field truncated",
                                      offset=pos)
            octet = body[pos]
            pos += 1
            total += octet
            if octet != 255:
                break
        sizes.append(total)
    return sizes, pos


def _ebml_sizes(body: bytes, offset: int, frame_count: int
                ) -> tuple[list[int], int]:
    # First value is a signed EBML lacing integer (zig-zag encoding).
    first_unsigned, width, pos = decode_vint(body, offset)
    if first_unsigned < 0:
        raise CorruptMKVError("EBML lacing uses unknown-length VINT",
                              offset=offset)
    first_size = (first_unsigned >> 1) ^ -(first_unsigned & 1)
    if first_size < 0:
        raise CorruptMKVError("EBML lacing first frame size is negative",
                              offset=offset)
    sizes = [first_size]
    previous = first_size
    for _ in range(frame_count - 2):
        unsigned, width, pos = decode_vint(body, pos)
        if unsigned < 0:
            raise CorruptMKVError("EBML lacing uses unknown-length VINT",
                                  offset=pos - width)
        delta = (unsigned >> 1) ^ -(unsigned & 1)
        previous += delta
        if previous < 0:
            raise CorruptMKVError("EBML lacing produced negative frame size",
                                  offset=pos - width)
        sizes.append(previous)
    return sizes, pos


def _split_frames(body: bytes, payload_offset: int, flags: int
                  ) -> list[bytes]:
    lacing = (flags & LACING_MASK) >> LACING_SHIFT
    if lacing == LACING_NONE:
        return [body[payload_offset:]]

    if payload_offset >= len(body):
        raise CorruptMKVError("laced block missing frame-count byte")
    frame_count = body[payload_offset] + 1
    if frame_count < 2:
        raise CorruptMKVError("laced block declares fewer than 2 frames")
    size_header_offset = payload_offset + 1

    if lacing == LACING_XIPH:
        sizes, data_start = _xiph_sizes(body, size_header_offset, frame_count)
    elif lacing == LACING_FIXED:
        data_start = size_header_offset
        payload_len = len(body) - data_start
        if payload_len % frame_count:
            raise CorruptMKVError(
                "fixed-size lacing payload is not a multiple of frame count")
        fixed = payload_len // frame_count
        sizes = [fixed] * (frame_count - 1)
    elif lacing == LACING_EBML:
        sizes, data_start = _ebml_sizes(body, size_header_offset, frame_count)
    else:
        raise CorruptMKVError(f"reserved lacing mode {lacing}")

    # The last frame gets whatever payload remains, per all three modes.
    frames: list[bytes] = []
    pos = data_start
    for size in sizes:
        end = pos + size
        if end > len(body):
            raise CorruptMKVError(
                "laced frame runs past block payload "
                f"(need {end} bytes, have {len(body)})", offset=pos)
        frames.append(body[pos:end])
        pos = end
    if pos > len(body):
        raise CorruptMKVError("laced frames overflow block payload",
                              offset=pos)
    frames.append(body[pos:])
    if len(frames) != frame_count:
        raise CorruptMKVError("lacing frame count mismatch")
    return frames


def deinterlace(mkv: MatroskaFile, *, max_samples: int,
                max_packet_size: int) -> list[Sample]:
    """Read every block body and expand it into per-track samples."""
    samples: list[Sample] = []
    track_cache: dict[int, TrackInfo] = {t.number: t for t in mkv.tracks}
    for block in mkv.blocks:
        body = _read_block_body(mkv, block, max_packet_size)
        track_number, relative_tc, flags, payload_offset = _parse_header(body)
        if track_number not in track_cache:
            raise CorruptMKVError(
                f"block references undeclared track number {track_number}",
                offset=block.element_offset)
        block.track_number = track_number
        absolute_tc = block.cluster_timecode + relative_tc
        frames = _split_frames(body, payload_offset, flags)
        for index, frame in enumerate(frames):
            if len(frame) == 0:
                raise CorruptMKVError(
                    "zero-length laced frame",
                    offset=block.data_offset)
            if len(frame) > max_packet_size:
                raise LimitExceededError(
                    f"sample of {len(frame)} bytes exceeds packet limit "
                    f"{max_packet_size}", offset=block.element_offset)
            if len(samples) >= max_samples:
                raise LimitExceededError(
                    f"sample count limit ({max_samples}) exceeded",
                    offset=block.element_offset)
            samples.append(Sample(
                track_number=track_number,
                pts_ticks=absolute_tc,
                data=bytes(frame),
                keyframe=block.keyframe,
                duration_ticks=block.duration_ticks,
            ))
    return samples


def _read_block_body(mkv: MatroskaFile, block: RawBlock,
                     max_packet_size: int) -> bytes:
    if block.size > max_packet_size:
        raise LimitExceededError(
            f"block of {block.size} bytes exceeds packet limit "
            f"{max_packet_size}", offset=block.element_offset)
    # Local import: the reader owns the file handle; blocks stay pure logic
    # so they can be unit-tested with in-memory bodies.
    from ..ebml.reader import EbmlReader

    with EbmlReader(mkv.path, max_element_size=max_packet_size) as reader:
        return reader.read_range(block.data_offset, block.size)
