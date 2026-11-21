"""Byte-level rewriting of a rebuilt MP4.

The assembly helpers are intentionally dumb: strip every top-level sidx,
insert the replacement after ftyp, and shift the chunk offset tables by the net
byte movement.  Higher-level policy (what to index, what to preserve) lives in
``rebuild.py``.
"""

from __future__ import annotations

import os
import tempfile

from .box_reader import Box, BoxReader
from .errors import ConsistencyError, StructureError
from .model import Track


# --------------------------------------------------------------------------
# Byte-level rewriting
# --------------------------------------------------------------------------


def sidx_insert_at(roots: list[Box]) -> int:
    """Absolute offset where the replacement sidx is placed (after ftyp)."""

    ftyp = next((box for box in roots if box.type == b"ftyp"), None)
    return ftyp.end if ftyp is not None else 0


def assemble(
    data: bytes,
    roots: list[Box],
    index_bytes: bytes,
    *,
    drop_trailing: bool = False,
) -> bytes:
    """Remove all top-level sidx boxes and insert the new one after ftyp.

    ``drop_trailing`` is used when the reader had to stop at a truncated
    trailing sidx: those unparsed bytes are the damaged index and must not be
    copied into the rebuilt file.
    """

    insert_at = sidx_insert_at(roots)
    out = bytearray()
    for box in roots:
        if box.type == b"sidx":
            continue
        out += data[box.start : box.end]
        if box.end == insert_at and insert_at > 0:
            out += index_bytes
    if insert_at == 0:
        out = bytearray(index_bytes) + out
    # Any bytes between / after boxes (not expected for well-formed input) are
    # preserved so the rewrite is byte-faithful outside the index region.
    if not drop_trailing and roots and roots[-1].end < len(data):
        out += data[roots[-1].end :]
    return bytes(out)


def patch_chunk_offsets(
    data: bytes, track: Track, delta: int
) -> bytes:
    """Add ``delta`` to every stco/co64 entry in the rebuilt byte stream."""

    view = memoryview(data)
    new_roots = BoxReader(view).parse_top_level()
    stbls = [box for root in new_roots for box in root.walk() if box.type == b"stbl"]
    if len(stbls) != 1:
        raise StructureError(
            f"expected exactly one stbl after rewriting, found {len(stbls)}",
            box_type=b"stbl",
        )
    stbl = stbls[0]
    tables = [child for child in stbl.children if child.type in (b"stco", b"co64")]
    if len(tables) != 1:
        raise StructureError(
            "expected exactly one stco/co64 after rewriting",
            box_type=b"stbl",
            offset=stbl.start,
        )
    table = tables[0]
    width = 4 if table.type == b"stco" else 8
    mutable = bytearray(data)
    count_at = table.payload_start + 4
    count = int.from_bytes(mutable[count_at : count_at + 4], "big")
    if count != len(track.chunks):
        raise ConsistencyError(
            f"offset table has {count} entries but the track has "
            f"{len(track.chunks)} chunks",
            box_type=table.type,
            offset=table.start,
        )
    entries_at = count_at + 4
    if entries_at + count * width > table.end:
        raise StructureError(
            "offset table overruns its box while patching",
            box_type=table.type,
            offset=table.start,
        )
    for index in range(count):
        position = entries_at + index * width
        value = int.from_bytes(mutable[position : position + width], "big")
        updated = value + delta
        if updated < 0 or updated >= 1 << (8 * width):
            raise ConsistencyError(
                f"chunk {index + 1} offset {value} does not fit after shifting "
                f"by {delta}",
                box_type=table.type,
                offset=position,
            )
        mutable[position : position + width] = updated.to_bytes(width, "big")
    return bytes(mutable)


# --------------------------------------------------------------------------
# Output and orchestration
# --------------------------------------------------------------------------


def atomic_write(output: str, data: bytes) -> None:
    directory = os.path.dirname(os.path.abspath(output)) or "."
    fd, temp_path = tempfile.mkstemp(prefix=".mediidx-", suffix=".mp4", dir=directory)
    try:
        with os.fdopen(fd, "wb") as handle:
            handle.write(data)
            handle.flush()
            os.fsync(handle.fileno())
        os.replace(temp_path, output)
    except BaseException:
        try:
            os.unlink(temp_path)
        except OSError:
            pass
        raise
