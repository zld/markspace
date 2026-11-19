"""Self-contained container sniffing for ISO BMFF (MP4) and RIFF/WAVE.

Only header-level metadata is read. Every read goes through :class:`Reader`,
which enforces file bounds so malformed sizes can never trigger an
out-of-bounds read. Structural problems are raised as :class:`SniffError`
with a byte offset and a box path; harmless oddities are recorded as
warnings on the sniff context.
"""

from __future__ import annotations

import struct
from dataclasses import dataclass, field
from typing import BinaryIO, Iterator

MAX_FILE_BYTES = 2 * 1024 * 1024 * 1024  # 2 GiB per file
MAX_BOXES = 4096
MAX_DEPTH = 12
MAX_BRANDS = 32
MAX_SAMPLE_ENTRIES = 64
MAX_EDIT_ENTRIES = 1024
MAX_WAV_CHUNKS = 4096

FTYP = b"ftyp"
MOOV = b"moov"
MDAT = b"mdat"
UUID = b"uuid"

# Boxes whose payload we descend into, mapped to their child layout.
CONTAINER_TYPES = {
    b"moov", b"trak", b"mdia", b"minf", b"stbl", b"edts", b"dinf",
}
# Boxes we understand well enough to ignore without warning.
IGNORABLE_TYPES = {
    b"free", b"skip", b"wide", b"mdat", b"styp", b"moof", b"mfra", b"pdin",
    b"udta", b"meta", b"ilst", b"iods", b"mvex", b"trex", b"mfhd", b"traf",
    b"tfhd", b"tfdt", b"trun", b"sidx", b"ssix", b"prft", b"emsg", b"btrt",
    b"pasp", b"clap", b"colr", b"gama", b"sv3d", b"proj", b"dOps", b"alac",
    b"wave", b"chan", b"srcp", b"dac3", b"dec3", b"dvcC", b"dvvC", b"hvcC",
    b"avcC", b"vpcC", b"av1C", b"esds", b"frma", b"schm", b"schi", b"sinf",
    b"stsd", b"stts", b"stsc", b"stsz", b"stz2", b"stco", b"co64", b"stss",
    b"ctts", b"sdtp", b"sbgp", b"sgpd", b"subs", b"saiz", b"saio", b"padb",
    b"stdp", b"dref", b"url ", b"urn ", b"vmhd", b"smhd", b"hmhd", b"nmhd",
    b"elst", b"tkhd", b"mdhd", b"hdlr", b"mvhd", b"ftyp",
}
# Sample entry formats we accept as a codec identifier.
VIDEO_CODECS = {b"avc1", b"avc3", b"hvc1", b"hev1", b"vp09", b"av01", b"mp4v"}
AUDIO_CODECS = {b"mp4a", b"opus", b"alac", b"twos", b"sowt", b"lpcm", b"fl32", b"fl64", b"ulaw", b"alaw", b"ac-3", b"ec-3"}
SUBTITLE_CODECS = {b"tx3g", b"wvtt", b"stpp", b"c608", b"c708"}


class SniffError(Exception):
    """Fatal structural error with a locatable position."""

    def __init__(self, code: str, message: str, *, offset: int | None = None,
                 path: str | None = None):
        super().__init__(message)
        self.code = code
        self.message = message
        self.offset = offset
        self.path = path

    def to_dict(self) -> dict:
        return {
            "code": self.code,
            "message": self.message,
            "offset": self.offset,
            "path": self.path,
        }


@dataclass
class Warning:
    code: str
    message: str
    offset: int | None = None
    path: str | None = None

    def to_dict(self) -> dict:
        return {
            "code": self.code,
            "message": self.message,
            "offset": self.offset,
            "path": self.path,
        }


@dataclass
class BoxHeader:
    fourcc: bytes
    size: int
    header_size: int
    start: int
    end: int
    path: str
    uuid: bytes | None = None

    @property
    def name(self) -> str:
        return fourcc_str(self.fourcc)

    @property
    def payload_start(self) -> int:
        return self.start + self.header_size

    @property
    def payload_size(self) -> int:
        return self.end - self.payload_start


def fourcc_str(value: bytes) -> str:
    return value.decode("latin-1", "replace")


def iter_boxes(reader: "Reader", start: int, end: int, ctx: "SniffContext",
               path: str, depth: int) -> Iterator[BoxHeader]:
    """Yield validated box headers inside [start, end).

    Raises SniffError on a short header, impossible size, or overflow past
    the parent boundary. Never advances outside the parent range.
    """
    if depth > MAX_DEPTH:
        raise SniffError("depth_limit", f"box nesting exceeds {MAX_DEPTH}",
                         offset=start, path=path)
    offset = start
    while offset < end:
        if end - offset < 8:
            raise SniffError("truncated_box", "fewer than 8 bytes left for a box header",
                             offset=offset, path=path)
        header = reader.read_box_header(offset, end, path)
        ctx.boxes += 1
        if ctx.boxes > MAX_BOXES:
            raise SniffError("box_limit", f"more than {MAX_BOXES} boxes read",
                             offset=header.start, path=path)
        yield header
        if header.end <= offset:  # defensive: guarantee forward progress
            raise SniffError("invalid_size", "box does not advance the cursor",
                             offset=offset, path=path)
        offset = header.end


class Reader:
    """Bounded reader: no seek/read may escape [0, file_size]."""

    def __init__(self, handle: BinaryIO, size: int):
        self.handle = handle
        self.size = size

    def read_at(self, offset: int, count: int, *, path: str | None = None) -> bytes:
        if offset < 0 or count < 0:
            raise SniffError("invalid_offset", f"negative read offset={offset} count={count}",
                             offset=offset, path=path)
        if offset + count > self.size:
            raise SniffError(
                "out_of_bounds",
                f"read of {count} bytes at {offset} exceeds file size {self.size}",
                offset=offset, path=path,
            )
        self.handle.seek(offset)
        data = self.handle.read(count)
        if len(data) != count:
            raise SniffError(
                "truncated_read",
                f"wanted {count} bytes at {offset}, got {len(data)}",
                offset=offset, path=path,
            )
        return data

    def read_box_header(self, offset: int, parent_end: int, path: str) -> BoxHeader:
        base = self.read_at(offset, 8, path=path)
        size32, fourcc = struct.unpack(">I4s", base)
        header_size = 8
        if size32 == 1:
            if parent_end - offset < 16:
                raise SniffError("truncated_box", "largesize box header is truncated",
                                 offset=offset, path=path)
            (size,) = struct.unpack(">Q", self.read_at(offset + 8, 8, path=path))
            header_size = 16
        elif size32 == 0:
            size = parent_end - offset
        else:
            size = size32
        uuid = None
        if fourcc == UUID:
            if parent_end - offset < header_size + 16:
                raise SniffError("truncated_box", "uuid box header is truncated",
                                 offset=offset, path=path)
            uuid = self.read_at(offset + header_size, 16, path=path)
            header_size += 16
        if size < header_size:
            raise SniffError(
                "invalid_size",
                f"box {fourcc_str(fourcc)!r} size {size} is smaller than header {header_size}",
                offset=offset, path=path,
            )
        end = offset + size
        if end > parent_end:
            raise SniffError(
                "out_of_bounds",
                f"box {fourcc_str(fourcc)!r} ends at {end}, past boundary {parent_end}",
                offset=offset, path=path,
            )
        box_path = f"{path}/{fourcc_str(fourcc)}" if path else fourcc_str(fourcc)
        return BoxHeader(fourcc, size, header_size, offset, end, box_path, uuid)


@dataclass
class SniffContext:
    boxes: int = 0
    warnings: list[Warning] = field(default_factory=list)

    def warn(self, code: str, message: str, *, offset: int | None = None,
             path: str | None = None) -> None:
        self.warnings.append(Warning(code, message, offset, path))
