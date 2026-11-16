"""Synthetic ISO BMFF fixtures: valid multitrack files and damaged variants."""

from __future__ import annotations

import struct

VALID_CASES = ("valid-multitrack", "valid-64bit")
DAMAGED_CASES = ("corrupt-stbl", "truncated-box", "stale-sidx", "hierarchy-sidx")
ALL_CASES = VALID_CASES + DAMAGED_CASES

SAMPLE_COUNT = 24
SAMPLE_SIZE = 64 * 1024
SYNC_EVERY = 8
GROUP_SIZE = SAMPLE_SIZE * SYNC_EVERY


def box(box_type: bytes, payload: bytes, largesize: bool = False) -> bytes:
    if largesize:
        return struct.pack(">I", 1) + box_type + struct.pack(">Q", 16 + len(payload)) + payload
    return struct.pack(">I", 8 + len(payload)) + box_type + payload


def fullbox(box_type: bytes, version: int, flags: int, payload: bytes) -> bytes:
    head = bytes([version]) + struct.pack(">I", flags)[1:]
    return box(box_type, head + payload)


def _ftyp() -> bytes:
    return box(b"ftyp", b"iso5" + struct.pack(">I", 0x200) + b"iso5mp41")


def _tkhd(track_id: int) -> bytes:
    payload = struct.pack(">III", 0, 0, track_id) + struct.pack(">I", 0)
    payload += b"\x00" * 64
    return fullbox(b"tkhd", 0, 3, payload)


def _mdhd(timescale: int, duration: int) -> bytes:
    return fullbox(b"mdhd", 0, 0, struct.pack(">IIII", 0, 0, timescale, duration))


def _hdlr(handler: bytes) -> bytes:
    return fullbox(b"hdlr", 0, 0, struct.pack(">I", 0) + handler + b"\x00" * 12)


def _stbl(sizes: list[int], chunk_offsets: list[int], spc: int, timescale_ok: bool,
          syncs: list[int] | None, ctts: list[int] | None, use_co64: bool,
          sample_delta: int = 1000) -> bytes:
    count = len(sizes)
    stsz_payload = struct.pack(">II", 0, count if timescale_ok else count + 1)
    stsz_payload += b"".join(struct.pack(">I", s) for s in sizes)
    if not timescale_ok:
        stsz_payload = stsz_payload[:-4]  # claim one more entry than we store
    stsz = fullbox(b"stsz", 0, 0, stsz_payload)
    if use_co64:
        co = fullbox(b"co64", 0, 0, struct.pack(">I", len(chunk_offsets))
                     + b"".join(struct.pack(">Q", o) for o in chunk_offsets))
    else:
        co = fullbox(b"stco", 0, 0, struct.pack(">I", len(chunk_offsets))
                     + b"".join(struct.pack(">I", o) for o in chunk_offsets))
    stsc = fullbox(b"stsc", 0, 0,
                   struct.pack(">I", 1) + struct.pack(">III", 1, spc, 1))
    stts = fullbox(b"stts", 0, 0, struct.pack(">I", 1)
                   + struct.pack(">II", count, sample_delta))
    children = [stsz, co, stsc, stts]
    if ctts is not None:
        payload = struct.pack(">II", 1, count) + struct.pack(">I", 0)
        children.append(fullbox(b"ctts", 0, 0, payload))
    if syncs is not None:
        payload = struct.pack(">I", len(syncs)) + b"".join(struct.pack(">I", n) for n in syncs)
        children.append(fullbox(b"stss", 0, 0, payload))
    return box(b"stbl", b"".join(children))


def _trak(track_id: int, handler: bytes, timescale: int, duration: int,
          sizes: list[int], chunk_offsets: list[int], spc: int,
          syncs: list[int] | None, ctts: list[int] | None,
          corrupt: bool, use_co64: bool, sample_delta: int = 1000) -> bytes:
    stbl = _stbl(sizes, chunk_offsets, spc, not corrupt, syncs, ctts,
                 use_co64, sample_delta)
    minf = box(b"minf", box(b"dinf", fullbox(b"dref", 0, 1, struct.pack(">I", 1)
               + fullbox(b"url ", 0, 1, b""))) + stbl)
    mdia = box(b"mdia", _mdhd(timescale, duration) + _hdlr(handler) + minf)
    return box(b"trak", _tkhd(track_id) + mdia)


def _sidx(reference_id: int, timescale: int, first_offset: int,
         sizes: list[int], durations: list[int], version: int,
         stale: bool, hierarchical: bool) -> bytes:
    refs = b""
    for i, (size, duration) in enumerate(zip(sizes, durations)):
        if stale:
            size = max(1, size - 1)
        type_bit = 1 if hierarchical and i == 0 else 0
        raw1 = (type_bit << 31) | size
        sap = 0x90000000  # starts_with_sap=1, sap_type=1, sap_delta=0
        refs += struct.pack(">III", raw1, duration, sap)
    if version == 0:
        payload = struct.pack(">IIII", reference_id, timescale, 0, first_offset)
        payload += struct.pack(">HH", 0, len(sizes)) + refs
    else:
        payload = struct.pack(">IIQQ", reference_id, timescale, 0, first_offset)
        payload += struct.pack(">HH", 0, len(sizes)) + refs
    return fullbox(b"sidx", version, 0, payload)


def build_mp4(case: str) -> bytes:
    if case not in ALL_CASES:
        raise ValueError(f"unknown fixture case: {case}")
    use_co64 = case == "valid-64bit"
    sidx_version = 1 if case == "valid-64bit" else 0
    corrupt = case == "corrupt-stbl"
    stale = case == "stale-sidx"
    hierarchical = case == "hierarchy-sidx"
    sizes = [SAMPLE_SIZE] * SAMPLE_COUNT
    syncs = list(range(1, SAMPLE_COUNT + 1, SYNC_EVERY))
    ctts_zero = [0] * SAMPLE_COUNT
    # First pass with placeholder offsets: box widths are independent of values.
    moov = _build_moov(sizes, [0, 0], syncs, ctts_zero, corrupt, use_co64)
    group_sizes = [GROUP_SIZE] * 6
    group_durations = [SYNC_EVERY * 1000] * 3 + [SYNC_EVERY * 500] * 3
    sidx0 = _sidx(1, 24000, 0, group_sizes, group_durations,
                  sidx_version, stale, hierarchical)
    header_len = len(_ftyp()) + len(sidx0) + len(moov) + 8
    video_offset = header_len
    audio_offset = video_offset + SAMPLE_COUNT * SAMPLE_SIZE
    moov = _build_moov(sizes, [video_offset, audio_offset], syncs,
                       ctts_zero, corrupt, use_co64)
    first_offset = header_len - (len(_ftyp()) + len(sidx0))
    sidx = _sidx(1, 24000, first_offset, group_sizes, group_durations,
                 sidx_version, stale, hierarchical)
    mdat_payload = bytes([0xAB]) * (SAMPLE_COUNT * SAMPLE_SIZE)
    mdat_payload += bytes([0xCD]) * (SAMPLE_COUNT * SAMPLE_SIZE)
    data = _ftyp() + sidx + moov + box(b"mdat", mdat_payload)
    if case == "truncated-box":
        data = data[: len(data) - 256]
    return data


def _build_moov(sizes, offsets, syncs, ctts, corrupt, use_co64) -> bytes:
    video_delta = 1000
    audio_delta = 500
    video = _trak(1, b"vide", 24000, SAMPLE_COUNT * video_delta, sizes,
                  [offsets[0]], SAMPLE_COUNT, syncs, ctts, corrupt,
                  use_co64, video_delta)
    audio = _trak(2, b"soun", 48000, SAMPLE_COUNT * audio_delta, sizes,
                  [offsets[1]], SAMPLE_COUNT, None, None, corrupt,
                  use_co64, audio_delta)
    return box(b"moov", video + audio)
