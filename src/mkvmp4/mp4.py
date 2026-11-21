"""Shared integer helpers for MP4 box construction.

Kept separate from :mod:`mkvmp4.mp4` so the temporal structure (boxes, sample
tables) can be reasoned about independently of Matroska adaptation.
"""

from __future__ import annotations

from dataclasses import dataclass, field

from .errors import OutputError


@dataclass
class Box:
    """A fully encoded ISOBMFF box: ``size|type|payload``."""

    box_type: str
    payload: bytes

    @property
    def box_type_bytes(self) -> bytes:
        raw = self.box_type.encode("latin-1")
        if len(raw) != 4:
            raise OutputError(f"box type {self.box_type!r} is not 4 bytes")
        return raw

    def encode(self) -> bytes:
        body = self.box_type_bytes + self.payload
        size = len(body) + 4
        if size > 0xFFFFFFFF:
            raise OutputError(
                f"box {self.box_type} is {size} bytes and needs a 64-bit size field"
            )
        return size.to_bytes(4, "big") + body


def box(box_type: str, *children: bytes) -> bytes:
    """Encode a container box from already-encoded children."""
    return Box(box_type, b"".join(children)).encode()


def full_box(box_type: str, version: int, flags: int, payload: bytes = b"") -> bytes:
    """Encode a full box: version byte + 24-bit flags + payload."""
    if not 0 <= version <= 0xFF:
        raise OutputError(f"box {box_type}: version {version} out of range")
    if not 0 <= flags <= 0xFFFFFF:
        raise OutputError(f"box {box_type}: flags {flags} out of range")
    header = bytes([version]) + flags.to_bytes(3, "big")
    return Box(box_type, header + payload).encode()


def u8(value: int) -> bytes:
    return _uint(value, 1, 0xFF)


def u16(value: int) -> bytes:
    return _uint(value, 2, 0xFFFF)


def u24(value: int) -> bytes:
    return _uint(value, 3, 0xFFFFFF)


def u32(value: int) -> bytes:
    return _uint(value, 4, 0xFFFFFFFF)


def u64(value: int) -> bytes:
    return _uint(value, 8, 0xFFFFFFFFFFFFFFFF)


def _uint(value: int, width: int, maximum: int) -> bytes:
    if not isinstance(value, int) or isinstance(value, bool):
        raise OutputError(f"expected an integer, got {type(value).__name__}")
    if value < 0:
        raise OutputError(f"negative value {value} cannot be encoded as u{width * 8}")
    if value > maximum:
        raise OutputError(f"value {value} does not fit in u{width * 8}")
    return value.to_bytes(width, "big")


def fourcc(code: str) -> bytes:
    raw = code.encode("latin-1")
    if len(raw) > 4:
        raise OutputError(f"fourcc {code!r} longer than 4 bytes")
    return raw.ljust(4, b" ")


MATRIX_IDENTITY = (
    u32(0x00010000) + u32(0) + u32(0)
    + u32(0) + u32(0x00010000) + u32(0)
    + u32(0) + u32(0) + u32(0x40000000)
)


@dataclass
class BoxIndex:
    """Byte ranges of top-level boxes, for later index verification."""

    entries: list[dict[str, object]] = field(default_factory=list)

    def add(self, box_type: str, offset: int, size: int) -> None:
        self.entries.append({"type": box_type, "offset": offset, "size": size})


# ---------------------------------------------------------------------------
# Sample entries (codec configuration recorded from Matroska CodecPrivate)
# ---------------------------------------------------------------------------

SUPPORTED_MP4_CODECS: dict[str, str] = {
    "V_MPEG4/ISO/AVC": "avc1",
    "A_AAC": "mp4a",
    "S_TEXT/UTF8": "tx3g",
}


def _descriptor_length(length: int) -> bytes:
    """MPEG-4 descriptor length: 7 bits per byte, high bit = continuation."""
    if length < 0:
        raise OutputError("descriptor length must be non-negative")
    chunks = [length & 0x7F]
    length >>= 7
    while length:
        chunks.append(0x80 | (length & 0x7F))
        length >>= 7
    return bytes(reversed(chunks))


def _descriptor(tag: int, payload: bytes) -> bytes:
    return bytes([tag]) + _descriptor_length(len(payload)) + payload


def _esds_payload(audio_specific_config: bytes, channels: int, sample_rate: int) -> bytes:
    if not audio_specific_config:
        raise OutputError("AAC track has no CodecPrivate (AudioSpecificConfig)")
    object_type = 0x40  # MPEG-4 audio
    stream_type = 0x15  # audio stream
    decoder_specific = _descriptor(0x05, audio_specific_config)
    decoder_config = (
        bytes([object_type, stream_type])
        + u24(0)  # bufferSizeDB
        + u32(0)  # maxBitrate (unknown, not fabricated)
        + u32(0)  # avgBitrate
        + decoder_specific
    )
    sl_config = _descriptor(0x06, b"\x02")
    es = (
        u16(1)  # ES_ID
        + b"\x00"  # flags
        + _descriptor(0x04, decoder_config)
        + sl_config
    )
    return _descriptor(0x03, es)


def _visual_sample_entry(
    *,
    width: int,
    height: int,
    codec_box: bytes,
) -> bytes:
    compressor = b"\x00" * 32
    body = (
        b"\x00" * 6  # reserved
        + u16(1)  # data_reference_index
        + b"\x00" * 16  # pre_defined / reserved
        + u16(width)
        + u16(height)
        + u32(0x00480000)  # 72 dpi
        + u32(0x00480000)
        + u32(0)
        + u16(1)  # frame_count
        + compressor
        + u16(0x0018)  # depth
        + (0xFFFF).to_bytes(2, "big", signed=False)  # pre_defined = -1
        + codec_box
    )
    return Box("avc1", body).encode()


def _audio_sample_entry(
    *, channels: int, sample_rate: int, esds: bytes
) -> bytes:
    body = (
        b"\x00" * 6
        + u16(1)
        + b"\x00" * 8
        + u16(channels)
        + u16(16)  # samplesize
        + u16(0)
        + u16(0)
        + u32(sample_rate << 16)
        + esds
    )
    return Box("mp4a", body).encode()


def _tx3g_sample_entry(width: int, height: int) -> bytes:
    body = (
        b"\x00" * 6
        + u16(1)
        + u32(0)  # displayFlags
        + b"\x01"  # horizontal justification: center
        + b"\xff"  # vertical justification: bottom
        + b"\x00\x00\x00\x00"  # background colour
        + b"\x00\x00\x00\x00\x00\x00\x00\x00"  # BoxRecord
        + u16(0) + u16(0) + u16(1) + b"\x00" + u8(12) + b"\xff\xff\xff\xff"
    )
    del width, height  # tx3g has no fixed raster dimensions
    return Box("tx3g", body).encode()


def build_sample_entry(
    codec_id: str,
    *,
    codec_private: bytes,
    width: int = 0,
    height: int = 0,
    channels: int = 2,
    sample_rate: int = 48000,
) -> bytes:
    """Build the MP4 sample entry for a supported Matroska codec.

    Configuration is copied verbatim where MP4 and Matroska share the same
    record (AVC) and wrapped where a descriptor is required (AAC).  Nothing is
    transcoded.
    """
    entry = SUPPORTED_MP4_CODECS.get(codec_id)
    if entry is None:
        raise OutputError(f"codec {codec_id!r} has no MP4 sample entry mapping")
    if entry == "avc1":
        if len(codec_private) < 7 or codec_private[0] != 1:
            raise OutputError(
                "AVC CodecPrivate is not a valid AVCDecoderConfigurationRecord"
            )
        return _visual_sample_entry(
            width=width, height=height, codec_box=Box("avcC", codec_private).encode()
        )
    if entry == "mp4a":
        return _audio_sample_entry(
            channels=channels,
            sample_rate=sample_rate,
            esds=full_box("esds", 0, 0, _esds_payload(codec_private, channels, sample_rate)),
        )
    if entry == "tx3g":
        return _tx3g_sample_entry(width, height)
    raise OutputError(f"unhandled sample entry {entry}")


# ---------------------------------------------------------------------------
# Sample tables
# ---------------------------------------------------------------------------
