"""Re-serialize moov with rewritten stsc/stco/co64 after sample rearrangement.

Leaf boxes other than the chunk-mapping sample-table boxes are copied byte for
byte from the source container, so fields this tool does not model (codec
specific configuration, udta, ...) are preserved exactly.  Chunk offsets above
the 32-bit range transparently promote stco to co64 and all affected parent box
sizes are recomputed.
"""

from __future__ import annotations

import struct
from typing import BinaryIO

from .boxes import Box, FULL_CONTAINER_TYPES, HEADER_SIZE, read_payload
from .errors import FormatError

STSC_TYPES = frozenset({"stsc", "stco", "co64"})


class MoovRewriter:
    """Holds planned per-track chunks and renders a replacement moov box."""

    def __init__(self, moov: Box, stream: BinaryIO, plans: dict[int, list[tuple[int, int]]]):
        self._moov = moov
        self._stream = stream
        # plans[track_id] = ordered list of (absolute_offset, sample_count).
        self._plans = plans

    def render(self) -> bytes:
        return self._render_container(self._moov)

    # -- box traversal -----------------------------------------------------

    def _render_container(self, box: Box) -> bytes:
        pieces: list[bytes] = []
        for child in box.children:
            pieces.append(self._render_box(child))
        payload = b"".join(pieces)
        if box.type in FULL_CONTAINER_TYPES:
            original = read_payload(self._stream, box)
            payload = original[:4] + payload
        return struct.pack(">I4s", HEADER_SIZE + len(payload), box.type.encode("ascii")) + payload

    def _render_box(self, box: Box) -> bytes:
        if box.type == "trak":
            return self._render_trak(box)
        if box.children:
            return self._render_container(box)
        return self._render_leaf(box)

    def _render_trak(self, trak: Box) -> bytes:
        track_id = self._track_id(trak)
        if track_id is None:
            return self._render_container(trak)
        pieces: list[bytes] = []
        for child in trak.children:
            if child.type == "mdia":
                pieces.append(self._render_mdia(child, track_id))
            elif child.children:
                pieces.append(self._render_container(child))
            else:
                pieces.append(self._render_leaf(child))
        return struct.pack(">I4s", HEADER_SIZE + sum(len(p) for p in pieces), b"trak") + b"".join(pieces)

    def _render_mdia(self, mdia: Box, track_id: int) -> bytes:
        pieces: list[bytes] = []
        for child in mdia.children:
            if child.type == "minf":
                pieces.append(self._render_minf(child, track_id))
            elif child.children:
                pieces.append(self._render_container(child))
            else:
                pieces.append(self._render_leaf(child))
        return struct.pack(">I4s", HEADER_SIZE + sum(len(p) for p in pieces), b"mdia") + b"".join(pieces)

    def _render_minf(self, minf: Box, track_id: int) -> bytes:
        pieces: list[bytes] = []
        for child in minf.children:
            if child.type == "stbl":
                pieces.append(self._render_stbl(child, track_id))
            elif child.children:
                pieces.append(self._render_container(child))
            else:
                pieces.append(self._render_leaf(child))
        return struct.pack(">I4s", HEADER_SIZE + sum(len(p) for p in pieces), b"minf") + b"".join(pieces)

    def _render_stbl(self, stbl: Box, track_id: int) -> bytes:
        pieces: list[bytes] = []
        for child in stbl.children:
            if child.type in STSC_TYPES:
                continue  # replaced below
            if child.children:
                pieces.append(self._render_container(child))
            else:
                pieces.append(self._render_leaf(child))
        pieces.extend(self._chunk_boxes(track_id))
        return struct.pack(">I4s", HEADER_SIZE + sum(len(p) for p in pieces), b"stbl") + b"".join(pieces)

    # -- helpers -----------------------------------------------------------

    def _render_leaf(self, box: Box) -> bytes:
        payload = read_payload(self._stream, box)
        return struct.pack(">I4s", HEADER_SIZE + len(payload), box.type.encode("ascii")) + payload

    def _track_id(self, trak: Box) -> int | None:
        tkhd = trak.find_path("tkhd")
        if tkhd is None:
            return None
        version, _, data = _fullbox(self._stream, tkhd)
        offset = 16 if version == 1 else 8
        return struct.unpack(">I", data[offset:offset + 4])[0]

    def _chunk_boxes(self, track_id: int) -> list[bytes]:
        plan = self._plans[track_id]
        if not plan:
            raise FormatError(f"track {track_id}: no chunks planned")
        offsets = [offset for offset, _ in plan]
        use_co64 = any(offset > 0xFFFFFFFF for offset in offsets)
        # stsc: one run, first_chunk=1, samples_per_chunk from the plan.
        samples_per_chunk = plan[0][1]
        if any(count != samples_per_chunk for _, count in plan):
            entries = b"".join(
                struct.pack(">III", index + 1, count, 1) for index, (_, count) in enumerate(plan)
            )
            stsc_payload = struct.pack(">I", len(plan)) + entries
        else:
            stsc_payload = struct.pack(">I", 1) + struct.pack(">III", 1, samples_per_chunk, 1)
        boxes_out = [
            struct.pack(">I4s", HEADER_SIZE + 4 + len(stsc_payload), b"stsc")
            + b"\x00\x00\x00\x00"
            + stsc_payload
        ]
        if use_co64:
            payload = struct.pack(">I", len(offsets)) + b"".join(struct.pack(">Q", o) for o in offsets)
            boxes_out.append(
                struct.pack(">I4s", HEADER_SIZE + 4 + len(payload), b"co64") + b"\x00\x00\x00\x00" + payload
            )
        else:
            payload = struct.pack(">I", len(offsets)) + b"".join(struct.pack(">I", o) for o in offsets)
            boxes_out.append(
                struct.pack(">I4s", HEADER_SIZE + 4 + len(payload), b"stco") + b"\x00\x00\x00\x00" + payload
            )
        return boxes_out


def _fullbox(stream: BinaryIO, box: Box) -> tuple[int, int, bytes]:
    from .boxes import read_fullbox_payload

    return read_fullbox_payload(stream, box)
