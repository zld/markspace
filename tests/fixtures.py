"""Deterministic minimal ISO BMFF container fixtures.

The valid container holds 120 samples split over 4 chunks / fragments with
matching sidx references.  Every corruption helper returns a fresh byte copy
so tests never share mutated state.
"""

from __future__ import annotations

import struct

SAMPLE_COUNT = 120
CHUNK_SAMPLES = 30
FRAGMENT_COUNT = SAMPLE_COUNT // CHUNK_SAMPLES  # 4
TIMESCALE = 24000
SAMPLE_DELTA = 1001
SEED_BASE = 0x100


def _box(box_type: str, payload: bytes = b"") -> bytes:
    return struct.pack(">I4s", 8 + len(payload), box_type.encode("ascii")) + payload


def _fullbox(box_type: str, version: int, flags: int, payload: bytes) -> bytes:
    prefix = bytes([version]) + flags.to_bytes(3, "big")
    return _box(box_type, prefix + payload)


def sample_size(index: int) -> int:
    """Deterministic, non-uniform per-sample size."""
    return 64 + (index * 7) % 48


def sample_payload(index: int) -> bytes:
    size = sample_size(index)
    return bytes((SEED_BASE + (index + step) & 0xFF) for step in range(size))


def _stsz(sizes: list[int]) -> bytes:
    body = struct.pack(">II", 0, len(sizes))
    body += b"".join(struct.pack(">I", s) for s in sizes)
    return _fullbox("stsz", 0, 0, body)


def _stsc() -> bytes:
    # One run: every chunk (1-based) holds CHUNK_SAMPLES samples.
    body = struct.pack(">I", 1)  # entry_count
    body += struct.pack(">III", 1, CHUNK_SAMPLES, 1)
    return _fullbox("stsc", 0, 0, body)


def _stco(offsets: list[int], *, variant: str = "stco") -> bytes:
    if variant == "co64":
        body = struct.pack(">I", len(offsets))
        body += b"".join(struct.pack(">Q", off) for off in offsets)
        return _fullbox("co64", 0, 0, body)
    body = struct.pack(">I", len(offsets))
    body += b"".join(struct.pack(">I", off) for off in offsets)
    return _fullbox("stco", 0, 0, body)


def _stts() -> bytes:
    body = struct.pack(">I", 1)  # entry_count
    body += struct.pack(">II", SAMPLE_COUNT, SAMPLE_DELTA)
    return _fullbox("stts", 0, 0, body)


def _stbl(chunk_offsets: list[int], *, offset_variant: str = "stco") -> bytes:
    payload = (
        _stsz([sample_size(i) for i in range(SAMPLE_COUNT)])
        + _stsc()
        + _stco(chunk_offsets, variant=offset_variant)
        + _stts()
    )
    return _box("stbl", payload)


def _minf(chunk_offsets: list[int], *, offset_variant: str) -> bytes:
    return _box("minf", _stbl(chunk_offsets, offset_variant=offset_variant))


def _mdia(chunk_offsets: list[int], *, offset_variant: str) -> bytes:
    return _box("mdia", _minf(chunk_offsets, offset_variant=offset_variant))


def _trak(chunk_offsets: list[int], *, offset_variant: str) -> bytes:
    return _box("trak", _mdia(chunk_offsets, offset_variant=offset_variant))


def _moov(chunk_offsets: list[int], *, offset_variant: str) -> bytes:
    return _box("moov", _trak(chunk_offsets, offset_variant=offset_variant))


def _sidx(ref_targets: list[int], ref_sizes: list[int],
          sidx_box_offset: int,
          *, version: int = 0, size_delta: int = 0,
          first_offset_shift: int = 0) -> bytes:
    # first_offset is relative to the start of the sidx box and points at the
    # first referenced mdat box header.
    first_offset = ref_targets[0] - sidx_box_offset + first_offset_shift
    if version == 0:
        fixed = struct.pack(
            ">IIIII", 1, TIMESCALE, 0, first_offset, 0
        )
    else:
        fixed = struct.pack(">II", 1, TIMESCALE)
        fixed += struct.pack(">QQI", 0, first_offset, 0)
    body = fixed + struct.pack(">I", len(ref_sizes))
    for i, size in enumerate(ref_sizes):
        ref_size = size + (size_delta if i == 0 else 0)
        sap_word = 0x80000000 if i == 0 else 0  # starts_with_sap on first ref
        body += struct.pack(">III", ref_size, SAMPLE_DELTA * CHUNK_SAMPLES,
                            sap_word)
    return _fullbox("sidx", version, 0, body)


def build_container(
    *,
    offset_variant: str = "stco",
    sidx_version: int = 0,
    size_delta: int = 0,
    first_offset_shift: int = 0,
) -> bytes:
    """Build the valid container (or a parameter-damaged variant) as bytes."""
    sizes = [sample_size(i) for i in range(SAMPLE_COUNT)]
    chunk_sizes = [
        sum(sizes[c * CHUNK_SAMPLES:(c + 1) * CHUNK_SAMPLES])
        for c in range(FRAGMENT_COUNT)
    ]

    ftyp = _box("ftyp", b"iso5" + struct.pack(">I", 1) + b"iso5dash")
    # First pass: build a provisional sidx with a zero-relative first offset;
    # only its *size* is needed to locate the media region.
    placeholder = [0] * FRAGMENT_COUNT
    moov = _moov(placeholder, offset_variant=offset_variant)
    sidx_placeholder_offset = len(ftyp) + len(moov)
    provisional_sidx = _sidx(
        [sidx_placeholder_offset] * FRAGMENT_COUNT,
        chunk_sizes,
        sidx_placeholder_offset,
        version=sidx_version,
        size_delta=size_delta,
        first_offset_shift=0,
    )
    media_cursor = len(ftyp) + len(moov) + len(provisional_sidx)

    chunk_offsets: list[int] = []
    mdat_starts: list[int] = []
    ref_sizes: list[int] = []
    fragments: list[bytes] = []
    for c in range(FRAGMENT_COUNT):
        # Each fragment is an mdat box; stco/sidx describe its payload region.
        payload = b"".join(
            sample_payload(i)
            for i in range(c * CHUNK_SAMPLES, (c + 1) * CHUNK_SAMPLES)
        )
        mdat_starts.append(media_cursor)
        media_cursor += 8  # mdat header precedes the sample bytes
        chunk_offsets.append(media_cursor)
        fragments.append(_box("mdat", payload))
        media_cursor += len(payload)
        ref_sizes.append(8 + len(payload))

    # Second pass: rebuild moov and sidx with the resolved absolute offsets.
    moov = _moov(chunk_offsets, offset_variant=offset_variant)
    sidx = _sidx(
        mdat_starts,
        ref_sizes,
        sidx_placeholder_offset,
        version=sidx_version,
        size_delta=size_delta,
        first_offset_shift=first_offset_shift,
    )
    return ftyp + moov + sidx + b"".join(fragments)


def container_layout() -> dict[str, int]:
    """Expose structural offsets of a valid container for targeted patching."""
    data = build_container()
    sizes = [sample_size(i) for i in range(SAMPLE_COUNT)]
    chunk_sizes = [
        sum(sizes[c * CHUNK_SAMPLES:(c + 1) * CHUNK_SAMPLES])
        for c in range(FRAGMENT_COUNT)
    ]
    # Locate boxes by scanning their 4CC markers.
    positions: dict[str, int] = {}
    for marker in (b"ftyp", b"moov", b"sidx"):
        positions[marker.decode()] = data.index(marker) - 4
    return {"data": data, "positions": positions, "chunk_sizes": chunk_sizes}


# ---------------------------------------------------------------------------
# Corruption variants.  Each returns a fresh byte string.
# ---------------------------------------------------------------------------
def corrupt_truncated_header() -> bytes:
    return build_container()[:30]  # ends mid-ftyp header/payload


def corrupt_box_beyond_file() -> bytes:
    data = bytearray(build_container())
    ftyp_start = data.index(b"ftyp") - 4
    struct.pack_into(">I", data, ftyp_start, len(data) + 500)
    return bytes(data)


def corrupt_extended_size_too_small() -> bytes:
    data = bytearray(build_container())
    ftyp_start = data.index(b"ftyp") - 4
    original = struct.unpack_from(">I", data, ftyp_start)[0]
    replacement = struct.pack(">I4sQ", 1, b"ftyp", 8)
    data[ftyp_start:ftyp_start + original] = (
        replacement + bytes(data[ftyp_start + 8:ftyp_start + original])
    )
    return bytes(data)


def corrupt_sidx_unknown_version() -> bytes:
    data = bytearray(build_container())
    sidx = data.index(b"sidx") - 4
    data[sidx + 8] = 9  # version byte of the fullbox
    return bytes(data)


def corrupt_sidx_zero_timescale() -> bytes:
    data = bytearray(build_container())
    sidx_payload = data.index(b"sidx") + 4
    # fullbox prefix (4) then reference_id (4) then timescale (4).
    struct.pack_into(">I", data, sidx_payload + 8, 0)
    return bytes(data)


def corrupt_sidx_offset_mismatch() -> bytes:
    # Shift the first reference target 128 bytes away from the real media.
    return build_container(first_offset_shift=128)


def corrupt_sidx_size_mismatch() -> bytes:
    return build_container(size_delta=17)


def corrupt_trailing_data() -> bytes:
    # A well-formed free box that no sidx reference points at.
    tail_payload = b"unreferenced-tail"
    tail = struct.pack(">I4s", 8 + len(tail_payload), b"free") + tail_payload
    return build_container() + tail


def corrupt_stsz_bad_count() -> bytes:
    """Understate the stsz count while leaving all payload bytes in place.

    The table stays parseable, but its total no longer agrees with stsc/stts,
    exercising the cross-table consistency check rather than truncation.
    """
    data = bytearray(build_container())
    stsz_payload = data.index(b"stsz") + 4
    # fullbox(4) + uniform_size(4), then sample_count.
    struct.pack_into(">I", data, stsz_payload + 8, SAMPLE_COUNT - 4)
    return bytes(data)


def corrupt_stsc_bad_first_chunk() -> bytes:
    data = bytearray(build_container())
    stsc_payload = data.index(b"stsc") + 4
    # fullbox(4) + entry_count(4), then first_chunk of entry 0.
    struct.pack_into(">I", data, stsc_payload + 8, 2)
    return bytes(data)


def corrupt_truncated_stbl() -> bytes:
    data = build_container()
    moov = data.index(b"moov") - 4
    # Shrink the moov declared size so its stbl payload is cut short.
    moov_size = struct.unpack_from(">I", data, moov)[0]
    cut = bytearray(data[: moov + moov_size - 12])
    struct.pack_into(">I", cut, moov, len(cut) - moov)
    return bytes(cut)


def _patch_sidx_entry_size(entry_index: int, new_size: int) -> bytes:
    """Overwrite one sidx reference's reference_size word (v0 layout)."""
    data = bytearray(build_container())
    sidx_payload = data.index(b"sidx") + 4
    # fullbox(4) + fixed fields(20) + reference_count(4) + 12 bytes per entry.
    entry_base = sidx_payload + 4 + 20 + 4 + entry_index * 12
    struct.pack_into(">I", data, entry_base, new_size)
    return bytes(data)


def corrupt_sidx_duplicate_reference() -> bytes:
    # Reference 1 starts at the same offset as reference 0 by shrinking its
    # predecessor to zero span.
    return _patch_sidx_entry_size(0, 0)


def _patch_sidx_first_offset(relative_shift: int) -> bytes:
    """Patch the v0 sidx first_offset field by a relative shift."""
    data = bytearray(build_container())
    sidx_payload = data.index(b"sidx") + 4
    # fullbox(4), ref_id(4), timescale(4), pts(4), then first_offset(4).
    position = sidx_payload + 16
    current = struct.unpack_from(">I", data, position)[0]
    struct.pack_into(">I", data, position, current + relative_shift)
    return bytes(data)


def build_sidx_with_refs(
    entries: list[tuple[int, int]],
    *,
    sidx_offset: int = 8,
    first_offset: int = 0,
    version: int = 0,
) -> bytes:
    """Construct a standalone sidx box with hand-specified reference entries.

    Each entry is (referenced_size, subsegment_duration); target offsets are
    resolved by the parser from ``sidx_offset + first_offset`` and the running
    size sum, exactly as a real encoder would lay them out.
    """
    if version == 0:
        fixed = struct.pack(">IIIII", 1, TIMESCALE, 0, first_offset, 0)
    else:
        fixed = struct.pack(">II", 1, TIMESCALE)
        fixed += struct.pack(">QQI", 0, first_offset, 0)
    body = fixed + struct.pack(">I", len(entries))
    for i, (ref_size, duration) in enumerate(entries):
        sap_word = 0x80000000 if i == 0 else 0
        body += struct.pack(">III", ref_size, duration, sap_word)
    return _fullbox("sidx", version, 0, body)


def corrupt_sidx_overlapping_reference() -> bytes:
    # Pull the whole anchor chain backwards so references no longer line up
    # with the fragment layout covered by the sample table.
    return _patch_sidx_first_offset(-64)


def corrupt_sidx_non_monotonic_reference() -> bytes:
    # Push the anchor chain into the media region, breaking alignment with
    # the sample table and producing coverage/ordering diagnostics.
    return _patch_sidx_first_offset(512)
