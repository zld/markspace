"""ISO-BMFF (MP4) box primitives: construction, parsing and validation.

Only the boxes the remuxer emits are modelled. Every box is the canonical
``size (uint32) | type (fourcc) | payload`` form; the parser here exists to
validate the files we write back from disk (requirement 7), not to parse
arbitrary foreign MP4s.
"""

import struct
from dataclasses import dataclass

from ..errors import IndexValidationError, IOFailure


def fourcc(value: str) -> bytes:
    """Encode a four-character code, rejecting anything else than 4 bytes."""
    raw = value.encode("ascii")
    if len(raw) != 4:
        raise ValueError(f"fourcc must be exactly 4 bytes, got {value!r}")
    return raw


def box(box_type: str, payload: bytes = b"") -> bytes:
    """Wrap ``payload`` in a box header. Sizes stay under 32 bits here."""
    if len(payload) + 8 > 0xFFFFFFFF:
        raise IndexValidationError(
            f"box {box_type} too large for a 32-bit size")
    return struct.pack(">I4s", len(payload) + 8, fourcc(box_type)) + payload


def full_box(box_type: str, version: int, flags: int, payload: bytes = b"") -> bytes:
    """Wrap a full box (1-byte version, 3-byte flags)."""
    head = bytes([version]) + flags.to_bytes(3, byteorder="big")
    return box(box_type, head + payload)


@dataclass(slots=True)
class ParsedBox:
    """One box parsed back from an emitted file."""

    box_type: str
    start: int          # header offset
    header_size: int    # always 8 in our output
    payload_start: int
    size: int           # total box size including header
    depth: int

    @property
    def payload_end(self) -> int:
        return self.payload_start + (self.size - self.header_size)


def parse_boxes(data: bytes, start: int, end: int, depth: int = 0,
                max_depth: int = 8) -> list[ParsedBox]:
    """Parse sibling boxes in ``data[start:end]`` and validate their sizes."""
    boxes: list[ParsedBox] = []
    pos = start
    while pos < end:
        if end - pos < 8:
            raise IndexValidationError(
                f"truncated box header at byte {pos} ({end - pos} bytes left)")
        size, btype = struct.unpack(">I4s", data[pos:pos + 8])
        if size < 8:
            raise IndexValidationError(
                f"box at byte {pos} has illegal size {size}")
        if pos + size > end:
            raise IndexValidationError(
                f"box {btype.decode('ascii', 'replace')} at byte {pos} "
                f"extends past container end ({pos + size} > {end})")
        parsed = ParsedBox(
            box_type=btype.decode("ascii", "replace"),
            start=pos,
            header_size=8,
            payload_start=pos + 8,
            size=size,
            depth=depth,
        )
        boxes.append(parsed)
        pos += size
    if pos != end:
        raise IndexValidationError(
            f"container ends at {end} but boxes consume up to {pos}")
    return boxes


#: Box types whose payload is a plain sequence of child boxes (containers).
#: ``stsd`` is handled separately (entry-count prefix), and sample entries
#: (avc1/mp4a) carry fixed binary fields before their nested config box.
CONTAINER_BOXES = frozenset({
    "moov", "trak", "mdia", "minf", "stbl", "dinf", "edts",
    "mvex", "moof", "traf", "mfra", "stsd",
})

#: Sample-entry box types and the size of their fixed (non-box) prefix.
#: Values match the exact byte layout emitted by the flat/fragmented writers.
SAMPLE_ENTRY_PREFIX = {
    "avc1": 76,   # bytes before the trailing avcC config box
    "mp4a": 42,   # bytes before the trailing esds config box
}


def validate_structure(data: bytes, expected: list[str] | None = None
                       ) -> dict[str, ParsedBox]:
    """Recursively validate an emitted MP4 and index its boxes by type.

    ``expected`` optionally lists the mandatory top-level box types. The
    returned map keeps the first occurrence of each box type; nested boxes
    are checked for container consistency at every depth.
    """
    index: dict[str, ParsedBox] = {}

    def walk(start: int, end: int, depth: int, skip_prefix: int) -> None:
        if depth > max_depth_local:
            raise IndexValidationError(
                f"box nesting exceeds depth {max_depth_local}")
        # ``skip_prefix`` handles stsd's 4-byte entry count: both bounds move
        # past it so the remaining range is exactly the child box sequence.
        child_start = start + skip_prefix
        for parsed in parse_boxes(data, child_start, end, depth):
            index.setdefault(parsed.box_type, parsed)
            if parsed.box_type in SAMPLE_ENTRY_PREFIX:
                # Sample entries carry fixed binary fields; only the trailing
                # config box region (avcC/esds...) is itself box-structured.
                inner_start = (parsed.payload_start +
                               SAMPLE_ENTRY_PREFIX[parsed.box_type])
                for nested in parse_boxes(data, inner_start,
                                          parsed.payload_end, depth + 1):
                    index.setdefault(nested.box_type, nested)
            elif parsed.box_type in CONTAINER_BOXES:
                # stsd is a full box: skip its 4 version/flags bytes plus its
                # 4-byte sample-entry count to reach the child box sequence.
                # Each recursion is bounded by the box's own payload end.
                prefix = 8 if parsed.box_type == "stsd" else 0
                walk(parsed.payload_start, parsed.payload_end,
                     depth + 1, prefix)
    max_depth_local = 8
    top = parse_boxes(data, 0, len(data))
    top_types = [b.box_type for b in top]
    if expected is not None:
        missing = [name for name in expected if name not in top_types]
        if missing:
            raise IndexValidationError(
                f"missing mandatory top-level box(s): {', '.join(missing)}")
    for parsed in top:
        index.setdefault(parsed.box_type, parsed)
        if parsed.box_type in CONTAINER_BOXES:
            walk(parsed.payload_start, parsed.payload_end, 1, 0)
    return index


# ---------------------------------------------------------------------------
# Small big-endian writers reused by both exporters.
# ---------------------------------------------------------------------------
def u8(value: int) -> bytes:
    return struct.pack(">B", value)


def u16(value: int) -> bytes:
    return struct.pack(">H", value)


def u32(value: int) -> bytes:
    return struct.pack(">I", value)


def u64(value: int) -> bytes:
    return struct.pack(">Q", value)


def i32(value: int) -> bytes:
    return struct.pack(">i", value)


def write_file_atomic(path: str, payload: bytes) -> None:
    """Write ``payload`` to a temp file then atomically replace ``path``.

    The temporary file lives next to the target so ``os.replace`` stays on
    the same file system. Any failure raises :class:`IOFailure` and leaves
    the original target untouched.
    """
    import os
    import tempfile

    directory = os.path.dirname(os.path.abspath(path)) or "."
    tmp_path = None
    try:
        with tempfile.NamedTemporaryFile(
                prefix=".mkvmp4-", suffix=".tmp", dir=directory,
                delete=False) as tmp:
            tmp_path = tmp.name
            tmp.write(payload)
            tmp.flush()
            os.fsync(tmp.fileno())
        os.replace(tmp_path, path)
    except OSError as exc:
        if tmp_path is not None:
            try:
                os.unlink(tmp_path)
            except OSError:
                pass
        raise IOFailure(f"failed to write {path!r}: {exc}") from exc
