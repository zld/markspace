"""Deterministic MP4 fixtures for rebuild / validate demonstrations.

The default fixture is the one described by the task acceptance:

* one non-fragmented video track, timescale 1,
* six samples with decode durations 1, 1, 2, 1, 1, 2 seconds,
* one chunk per sample, sample sizes 100, 120, 80, 140, 100, 160 bytes,
* a top-level sidx that already describes the expected 3 references.

Everything is generated from Python literals; no external media is read.
"""

from __future__ import annotations

import struct

from .sidx import encode_sidx

DEFAULT_DURATIONS = (1, 1, 2, 1, 1, 2)
DEFAULT_SIZES = (100, 120, 80, 140, 100, 160)
DEFAULT_TIMESCALE = 1

# Sample payload bytes are deterministic and non-zero so chunk ranges are easy
# to eyeball in a hex dump.
_SAMPLE_PATTERN = b"MEDIDX!"


def box(box_type: bytes, payload: bytes) -> bytes:
    return struct.pack(">I4s", 8 + len(payload), box_type) + payload


def full_box(box_type: bytes, version: int, flags: int, payload: bytes) -> bytes:
    return box(box_type, struct.pack(">B3s", version, flags.to_bytes(3, "big")) + payload)


def ftyp() -> bytes:
    return box(b"ftyp", b"isom" + struct.pack(">I", 512) + b"isomiso2mp41")


def _mvhd(timescale: int, duration: int) -> bytes:
    payload = struct.pack(">IIII", 0, 0, timescale, duration)
    payload += struct.pack(">IH", 0x00010000, 0x0100)  # rate, volume
    payload += b"\x00" * 10
    payload += struct.pack(
        ">9I",
        0x00010000, 0, 0,
        0, 0x00010000, 0,
        0, 0, 0x40000000,
    )
    payload += b"\x00" * 24
    payload += struct.pack(">I", 2)  # next_track_ID
    return full_box(b"mvhd", 0, 0, payload)


def _tkhd(track_id: int, duration: int) -> bytes:
    payload = struct.pack(">IIIII", 0, 0, track_id, 0, duration)
    payload += b"\x00" * 8
    payload += struct.pack(">hhhh", 0, 0, 0, 0)
    payload += struct.pack(
        ">9I",
        0x00010000, 0, 0,
        0, 0x00010000, 0,
        0, 0, 0x40000000,
    )
    payload += struct.pack(">II", 0, 0)  # width, height
    return full_box(b"tkhd", 0, 0x000003, payload)


def _mdhd(timescale: int, duration: int) -> bytes:
    payload = struct.pack(">IIIIHH", 0, 0, timescale, duration, 0x55C4, 0)
    return full_box(b"mdhd", 0, 0, payload)


def _hdlr(handler: bytes) -> bytes:
    payload = b"\x00\x00\x00\x00" + handler + b"\x00" * 12 + b"mediidx\x00"
    return full_box(b"hdlr", 0, 0, payload)


def _vmhd() -> bytes:
    return full_box(b"vmhd", 0, 0x000001, b"\x00" * 8)


def _dinf() -> bytes:
    url = full_box(b"url ", 0, 0x000001, b"")
    dref = full_box(b"dref", 0, 0, struct.pack(">I", 1) + url)
    return box(b"dinf", dref)


def _stts(durations: tuple[int, ...]) -> bytes:
    # Run-length encode consecutive equal durations (order-preserving).
    entries: list[tuple[int, int]] = []
    for duration in durations:
        if entries and entries[-1][1] == duration:
            entries[-1] = (entries[-1][0] + 1, duration)
        else:
            entries.append((1, duration))
    payload = struct.pack(">I", len(entries))
    for count, duration in entries:
        payload += struct.pack(">II", count, duration)
    return full_box(b"stts", 0, 0, payload)


def _stsc(samples_per_chunk: tuple[int, ...]) -> bytes:
    entries: list[tuple[int, int, int]] = []
    for index, count in enumerate(samples_per_chunk, start=1):
        if not entries or entries[-1][1] != count:
            entries.append((index, count, 1))
    payload = struct.pack(">I", len(entries))
    for first_chunk, count, descriptor in entries:
        payload += struct.pack(">III", first_chunk, count, descriptor)
    return full_box(b"stsc", 0, 0, payload)


def _stsz(sizes: tuple[int, ...]) -> bytes:
    payload = struct.pack(">II", 0, len(sizes))
    payload += b"".join(struct.pack(">I", size) for size in sizes)
    return full_box(b"stsz", 0, 0, payload)


def _stco(offsets: tuple[int, ...], use_co64: bool = False) -> bytes:
    if use_co64:
        payload = struct.pack(">I", len(offsets))
        payload += b"".join(struct.pack(">Q", offset) for offset in offsets)
        return full_box(b"co64", 0, 0, payload)
    payload = struct.pack(">I", len(offsets))
    payload += b"".join(struct.pack(">I", offset) for offset in offsets)
    return full_box(b"stco", 0, 0, payload)


def _stbl(
    durations: tuple[int, ...],
    sizes: tuple[int, ...],
    offsets: tuple[int, ...],
    use_co64: bool,
    extra_boxes: tuple[bytes, ...] = (),
    samples_per_chunk: tuple[int, ...] | None = None,
) -> bytes:
    if samples_per_chunk is None:
        samples_per_chunk = tuple(1 for _ in sizes)
    return box(
        b"stbl",
        _stts(durations)
        + _stsc(samples_per_chunk)
        + _stsz(sizes)
        + _stco(offsets, use_co64)
        + b"".join(extra_boxes),
    )


def _moov(
    timescale: int,
    durations: tuple[int, ...],
    sizes: tuple[int, ...],
    offsets: tuple[int, ...],
    use_co64: bool,
    extra_boxes: tuple[bytes, ...] = (),
    samples_per_chunk: tuple[int, ...] | None = None,
) -> bytes:
    duration = sum(durations)
    trak = box(
        b"trak",
        _tkhd(1, duration)
        + box(
            b"mdia",
            _mdhd(timescale, duration)
            + _hdlr(b"vide")
            + box(
                b"minf",
                _vmhd()
                + _dinf()
                + _stbl(
                    durations,
                    sizes,
                    offsets,
                    use_co64,
                    extra_boxes,
                    samples_per_chunk,
                ),
            ),
        ),
    )
    return box(b"moov", _mvhd(timescale, duration) + trak)


def _sample_payload(size: int) -> bytes:
    if size <= 0:
        raise ValueError("sample size must be positive")
    repetitions = size // len(_SAMPLE_PATTERN) + 1
    return (_SAMPLE_PATTERN * repetitions)[:size]


def _sidx_for(
    timescale: int,
    segments: list[tuple[int, int, int]],
    *,
    first_offset: int,
    version: int = 0,
    earliest_presentation_time: int = 0,
) -> bytes:
    references: list[tuple[int, int, int]] = []
    for size, duration, _start in segments:
        sap_word = (1 << 31) | (1 << 28)
        references.append((size, duration, sap_word))
    return encode_sidx(
        version=version,
        reference_id=1,
        timescale=timescale,
        earliest_presentation_time=earliest_presentation_time,
        first_offset=first_offset,
        references=references,
    )


def _segment_sizes(sizes: tuple[int, ...]) -> list[tuple[int, int, int]]:
    """Reference plan for the default fixture: pairs of samples per segment."""

    groups: list[tuple[int, int, int]] = []
    for start in range(0, len(sizes), 2):
        chunk_sizes = sizes[start : start + 2]
        groups.append((sum(chunk_sizes), 0, start))
    return groups


def build_default(
    *,
    durations: tuple[int, ...] = DEFAULT_DURATIONS,
    sizes: tuple[int, ...] = DEFAULT_SIZES,
    timescale: int = DEFAULT_TIMESCALE,
    include_sidx: bool = True,
    use_co64: bool = False,
    sidx_version: int = 0,
    earliest_presentation_time: int = 0,
    index_override: bytes | None = None,
    extra_stbl_boxes: tuple[bytes, ...] = (),
    samples_per_chunk: tuple[int, ...] | None = None,
) -> bytes:
    if len(durations) != len(sizes):
        raise ValueError("durations and sizes must have the same length")
    if timescale <= 0:
        raise ValueError("timescale must be positive")

    if samples_per_chunk is None:
        samples_per_chunk = tuple(1 for _ in sizes)
    if sum(samples_per_chunk) != len(sizes):
        raise ValueError("samples_per_chunk must cover every sample")

    # Split samples into chunks according to the plan.
    chunk_spans: list[tuple[int, int]] = []
    cursor_sample = 0
    for count in samples_per_chunk:
        if count <= 0:
            raise ValueError("each chunk needs at least one sample")
        chunk_spans.append((cursor_sample, cursor_sample + count))
        cursor_sample += count
    sample_chunks = list(zip(durations, sizes))
    chunk_sizes = [
        sum(sizes[first:last]) for first, last in chunk_spans
    ]
    chunk_durations = [
        sum(durations[first:last]) for first, last in chunk_spans
    ]

    # Lay the file out: ftyp, optional sidx, moov, mdat.  moov depends on chunk
    # offsets, offsets depend on the moov size, and moov size does not depend on
    # the offsets, so two passes converge exactly.
    placeholder_offsets = tuple(0 for _ in chunk_sizes)
    moov_len = len(
        _moov(
            timescale,
            durations,
            sizes,
            placeholder_offsets,
            use_co64,
            extra_stbl_boxes,
            samples_per_chunk,
        )
    )

    # Index body size does not depend on first_offset (both are fixed-width),
    # so encode once with a zero offset to learn its length.
    groups: list[tuple[int, int, int]] = []
    start = 0
    while start < len(chunk_sizes):
        group_end = min(start + 2, len(chunk_sizes))
        group_duration = sum(chunk_durations[start:group_end])
        group_size = sum(chunk_sizes[start:group_end])
        groups.append((group_size, group_duration, start))
        start = group_end

    index_bytes = b""
    if include_sidx:
        if index_override is not None:
            index_bytes = index_override
        else:
            probe = _sidx_for(
                timescale,
                groups,
                first_offset=0,
                version=sidx_version,
                earliest_presentation_time=earliest_presentation_time,
            )
            sidx_end = len(ftyp()) + len(probe)
            mdat_payload_start = sidx_end + moov_len + 8
            first_sample_offset = mdat_payload_start
            index_bytes = _sidx_for(
                timescale,
                groups,
                first_offset=first_sample_offset - sidx_end,
                version=sidx_version,
                earliest_presentation_time=earliest_presentation_time,
            )
            if len(index_bytes) != len(probe):
                raise AssertionError("sidx size changed when first_offset was set")

    data_start = len(ftyp()) + len(index_bytes) + moov_len + 8
    offsets: list[int] = []
    cursor = data_start
    for size in chunk_sizes:
        offsets.append(cursor)
        cursor += size
    moov = _moov(
        timescale,
        durations,
        sizes,
        tuple(offsets),
        use_co64,
        extra_stbl_boxes,
        samples_per_chunk,
    )
    if len(moov) != moov_len:
        raise AssertionError("moov size changed between layout passes; fixture is unstable")
    mdat_payload = b"".join(_sample_payload(size) for _duration, size in sample_chunks)
    mdat = box(b"mdat", mdat_payload)
    return ftyp() + index_bytes + moov + mdat


def write_fixture(path: str, data: bytes | None = None) -> int:
    payload = data if data is not None else build_default()
    with open(path, "wb") as handle:
        handle.write(payload)
    return len(payload)


if __name__ == "__main__":  # pragma: no cover - manual smoke helper
    import sys

    target = sys.argv[1] if len(sys.argv) > 1 else "/tmp/sample.mp4"
    size = write_fixture(target)
    print(f"wrote {target} ({size} bytes)")
