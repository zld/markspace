"""Hand-written EBML parser: element IDs, VINT sizes, unknown-length scanning.

The parser is intentionally strict about structure:

* truncated VINTs raise :class:`EbmlError` (category ``ebml``);
* a child whose declared end crosses its parent boundary is rejected rather
  than silently truncated;
* unknown-length elements (all VINT length bytes set to 0xFF) are supported
  and scanned to the next valid upper-level boundary;
* every read is bounded by a maximum byte count, element count and, at the
  block layer, maximum sample length (see :class:`Limits`).
"""

from __future__ import annotations

from dataclasses import dataclass

from .errors import EbmlError, LimitExceededError

# A VINT is at most 8 octets long (EBML spec, RFC 8794 section 4).
MAX_VINT_WIDTH = 8

# Well-known element IDs that may legally appear at the EBML document root.
ID_EBML_HEADER = 0x1A45DFA3
ID_SEGMENT = 0x18538067

# Void (discardable filler) and CRC-32 are structural, not media elements.
ID_VOID = 0xEC
ID_CRC32 = 0xBF


@dataclass(frozen=True)
class Limits:
    """Safety caps applied while parsing an input file."""

    max_bytes: int = 512 * 1024 * 1024
    max_elements: int = 1_000_000
    max_sample_bytes: int = 64 * 1024 * 1024


@dataclass
class Element:
    """One EBML element header plus its payload span inside the file."""

    id: int
    position: int          # absolute file offset of the ID's first byte
    header_size: int       # ID width + size VINT width
    size: int | None       # None means an unknown-length element
    body_start: int        # absolute offset where the payload begins
    body_end: int | None   # absolute declared end; None for unknown length

    @property
    def known_length(self) -> bool:
        return self.size is not None


@dataclass
class LimitsState:
    limits: Limits
    element_count: int = 0

    def tick(self) -> None:
        self.element_count += 1
        if self.element_count > self.limits.max_elements:
            raise LimitExceededError(
                f"element count exceeded limit of {self.limits.max_elements}")


def _is_all_ones(data: bytes) -> bool:
    return all(b == 0xFF for b in data)


def read_vint(buf: bytes, pos: int, *, is_id: bool,
              unknown_allowed: bool) -> tuple[int, int, bool]:
    """Read one EBML variable-length integer at ``pos``.

    Returns ``(value, width, unknown)``.  For size VINTs an all-ones payload
    means "unknown length" (only legal when ``unknown_allowed``); for ID
    VINTs the all-ones form is reserved and rejected.
    """
    if pos >= len(buf):
        raise EbmlError("truncated VINT: no header byte", offset=pos)
    first = buf[pos]
    if first == 0:
        raise EbmlError(
            "invalid VINT: leading byte 0x00 has no length marker",
            offset=pos)
    # Leading byte 0xFF cannot begin a shorter VINT: its marker only exists
    # at the maximum (8-byte) width, where it doubles as the unknown-length
    # encoding.
    if first == 0xFF:
        width = MAX_VINT_WIDTH
    else:
        width = 8 - first.bit_length() + 1  # leading zero bits + 1
    if width > MAX_VINT_WIDTH:
        raise EbmlError(f"VINT width {width} exceeds {MAX_VINT_WIDTH}",
                        offset=pos)
    if pos + width > len(buf):
        raise EbmlError(
            f"truncated VINT: need {width} bytes, "
            f"{len(buf) - pos} available", offset=pos)
    raw = buf[pos:pos + width]
    if is_id:
        if _is_all_ones(raw):
            raise EbmlError("reserved all-ones EBML ID VINT", offset=pos)
        return int.from_bytes(raw, "big"), width, False
    # An all-ones VINT marks unknown length. The width comes from the
    # leading byte's marker (1..8); both 0xFF and an 8-byte 0xFF*8 run are
    # legal unknown-length encodings (Matroska writes the 8-byte form).
    if _is_all_ones(raw):
        if not unknown_allowed:
            raise EbmlError("unknown-length VINT where a size is required",
                            offset=pos)
        mask = (1 << (8 * width - width)) - 1
        return int.from_bytes(raw, "big") & mask, width, True
    mask = (1 << (8 * width - width)) - 1
    payload = int.from_bytes(raw, "big") & mask
    return payload, width, False


class EbmlParser:
    """Cursor-based EBML reader over an in-memory byte buffer."""

    def __init__(self, buf: bytes, limits: Limits | None = None) -> None:
        self.buf = buf
        self.limits = LimitsState(limits or Limits())
        self.file_size = len(buf)
        if self.file_size > self.limits.limits.max_bytes:
            raise LimitExceededError(
                f"input size {self.file_size} exceeds limit of "
                f"{self.limits.limits.max_bytes} bytes")

    def read_element(self, pos: int, parent_end: int | None) -> Element:
        """Read an element header at ``pos`` without consuming its body."""
        eid, id_w, _ = read_vint(self.buf, pos, is_id=True,
                                 unknown_allowed=False)
        size_pos = pos + id_w
        size, size_w, unknown = read_vint(
            self.buf, size_pos, is_id=False, unknown_allowed=True)
        header_size = id_w + size_w
        body_start = pos + header_size
        if unknown:
            element = Element(eid, pos, header_size, None, body_start, None)
        else:
            body_end = body_start + size
            # parent_end is a hard boundary only for a known-length parent;
            # an unknown-length parent passes None and is bounded by EOF.
            if parent_end is not None and body_end > parent_end:
                raise EbmlError(
                    f"element 0x{eid:X} end 0x{body_end:X} crosses parent "
                    f"boundary 0x{parent_end:X}",
                    offset=pos, element_id=eid)
            if body_end > self.file_size:
                raise EbmlError(
                    f"element 0x{eid:X} end 0x{body_end:X} crosses EOF "
                    f"0x{self.file_size:X}",
                    offset=pos, element_id=eid)
            element = Element(eid, pos, header_size, size, body_start,
                              body_end)
        self.limits.tick()
        return element

    def iter_children(self, body_start: int,
                      body_end: int | None) -> list[Element]:
        """Parse all known-length children of a parent payload.

        A child that is itself unknown length is returned as-is; the caller
        resolves it with :func:`scan_unknown_children` using its own
        vocabulary of legal upper-level IDs.
        """
        elements: list[Element] = []
        pos = body_start
        end = body_end if body_end is not None else self.file_size
        while pos < end:
            if pos >= self.file_size:
                raise EbmlError("truncated parent: child header past EOF",
                                offset=pos)
            element = self.read_element(pos, end)
            elements.append(element)
            if element.known_length:
                pos = element.body_end  # type: ignore[assignment]
            else:
                # Unknown-length child: caller scans it; nothing else can be
                # located until its boundary is resolved, so stop here.
                return elements
        return elements

    def payload(self, element: Element) -> bytes:
        if not element.known_length:
            raise EbmlError("cannot read payload of unknown-length element",
                            offset=element.position, element_id=element.id)
        return self.buf[element.body_start:element.body_end]

    def read_uint(self, element: Element) -> int:
        data = self.payload(element)
        return int.from_bytes(data, "big") if data else 0

    def read_sint(self, element: Element) -> int:
        data = self.payload(element)
        if not data:
            return 0
        value = int.from_bytes(data, "big", signed=True)
        return value

    def read_float(self, element: Element) -> float:
        data = self.payload(element)
        if len(data) == 4:
            import struct
            return struct.unpack(">f", data)[0]
        if len(data) == 8:
            import struct
            return struct.unpack(">d", data)[0]
        raise EbmlError(f"unsupported float width {len(data)}",
                        offset=element.position, element_id=element.id)

    def read_string(self, element: Element) -> str:
        data = self.payload(element)
        return data.rstrip(b"\x00").decode("utf-8", errors="replace")


def scan_unknown_children(parser: EbmlParser, element: Element,
                          valid_child_ids: frozenset[int],
                          nested_vocab: dict[int, frozenset[int]] | None = None,
                          stop_ids: frozenset[int] | None = None
                          ) -> list[Element]:
    """Scan the body of an unknown-length element by child boundaries.

    Every candidate header must name an ID from ``valid_child_ids``. A nested
    unknown-length child (e.g. an unknown Cluster inside an unknown Segment)
    is resolved recursively with ``nested_vocab[child_id]`` and stops at the
    first header from ``stop_ids`` (the upper-level sibling vocabulary).
    When no valid boundary can be established the input is rejected -
    arbitrary bytes are never swallowed into a media block.
    """
    if element.known_length:
        raise EbmlError("element has a known length; use iter_children",
                        offset=element.position, element_id=element.id)
    nested_vocab = nested_vocab or {}
    stop_ids = stop_ids or frozenset()
    children: list[Element] = []
    pos = element.body_start
    while pos < parser.file_size:
        # An unknown-length parent has no hard child boundary: read_element
        # enforces the EOF check itself when parent_end is None.
        try:
            candidate = parser.read_element(pos, None)
        except EbmlError:
            raise EbmlError(
                "undecidable boundary inside unknown-length element "
                f"0x{element.id:X}",
                offset=pos, element_id=element.id)
        # Reached the next upper-level sibling: this unknown element ends.
        if candidate.id in stop_ids:
            break
        if candidate.id not in valid_child_ids:
            raise EbmlError(
                f"unexpected ID 0x{candidate.id:X} while scanning "
                f"unknown-length element 0x{element.id:X}",
                offset=pos, element_id=candidate.id)
        children.append(candidate)
        if candidate.known_length:
            pos = candidate.body_end  # type: ignore[assignment]
            continue
        vocab = nested_vocab.get(candidate.id)
        if vocab is None:
            raise EbmlError(
                "nested unknown-length element has no scanning "
                f"vocabulary: 0x{candidate.id:X}",
                offset=candidate.position, element_id=candidate.id)
        # The nested element ends at the next upper-level sibling header,
        # i.e. anything outside its own vocabulary that the current parent
        # is allowed to contain (including another instance of the nested
        # element, such as the next Cluster).
        sibling_ids = valid_child_ids - vocab
        if candidate.id in valid_child_ids:
            sibling_ids = sibling_ids | {candidate.id}
        nested_stop = stop_ids | sibling_ids
        nested = scan_unknown_children(
            parser, candidate, vocab, nested_vocab,
            stop_ids=nested_stop)
        # Resume after the nested element's last known child. The sibling
        # header that terminated the recursion sits at exactly this offset.
        known_ends = [c.body_end for c in nested if c.known_length]
        pos = max(known_ends, default=candidate.body_start)
    return children
