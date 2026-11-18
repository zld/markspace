"""MP4 writer: ftyp, moov, mdat and sample tables (stream copy only)."""
from __future__ import annotations

import struct

from . import index

_MATRIX = struct.pack(">9I", 0x10000, 0, 0, 0, 0x10000, 0, 0, 0, 0x40000000)
MOVIE_TIMESCALE = 1000


def box(tag: bytes, payload: bytes = b"") -> bytes:
    return struct.pack(">I4s", 8 + len(payload), tag) + payload


def full_box(tag: bytes, version: int, flags: int, payload: bytes = b"") -> bytes:
    return box(tag, bytes([version]) + flags.to_bytes(3, "big") + payload)


def _ftyp(has_hevc: bool) -> bytes:
    brands = b"isom" + b"iso2" + (b"hvc1" if has_hevc else b"avc1") + b"mp41"
    return box(b"ftyp", b"isom" + struct.pack(">I", 512) + brands)


def _mvhd(duration_ms: int, next_track_id: int) -> bytes:
    payload = struct.pack(">IIII", 0, 0, MOVIE_TIMESCALE, duration_ms)
    payload += struct.pack(">IH", 0x10000, 0x0100) + b"\0" * 8
    return full_box(b"mvhd", 0, 0, payload + _MATRIX + b"\0" * 24
                    + struct.pack(">I", next_track_id))


def _tkhd(track_id: int, duration_ms: int, width: int, height: int,
          is_audio: bool) -> bytes:
    payload = struct.pack(">IIIII", 0, 0, track_id, 0, duration_ms) + b"\0" * 8
    payload += struct.pack(">hhh", 0, 0, 0x0100 if is_audio else 0) + b"\0\0"
    payload += _MATRIX + struct.pack(">II", width << 16, height << 16)
    return full_box(b"tkhd", 0, 7, payload)


def _mdhd(timescale: int, duration: int, language: str) -> bytes:
    code = 0
    for ch in (language or "und")[:3]:
        code = (code << 5) | ((ord(ch) - 0x60) & 0x1F)
    return full_box(b"mdhd", 0, 0,
                    struct.pack(">IIIIHH", 0, 0, timescale, duration, code, 0))


def _hdlr(kind: bytes) -> bytes:
    name = b"VideoHandler\0" if kind == b"vide" else b"SoundHandler\0"
    return full_box(b"hdlr", 0, 0, struct.pack(">I4s", 0, kind)
                    + b"\0" * 12 + name)


def _dinf() -> bytes:
    return box(b"dinf", full_box(b"dref", 0, 0,
               struct.pack(">I", 1) + full_box(b"url ", 0, 1)))


def _video_entry(tag: bytes, codec_private: bytes, w: int, h: int) -> bytes:
    payload = b"\0" * 6 + struct.pack(">H", 1) + b"\0" * 16
    payload += struct.pack(">HHII", w, h, 0x480000, 0x480000) + struct.pack(">I", 0)
    payload += struct.pack(">H", 1) + b"\0" * 32 + struct.pack(">Hh", 0x18, -1)
    codec_box = box(b"avcC" if tag == b"avc1" else b"hvcC", codec_private)
    return box(tag, payload + codec_box)


def _desc(tag: int, payload: bytes) -> bytes:
    n, groups = len(payload), []
    while True:
        groups.append(n & 0x7F)
        n >>= 7
        if not n:
            break
    out = bytearray([tag])
    for i, g in enumerate(reversed(groups)):
        out.append(g | (0x80 if i < len(groups) - 1 else 0))
    return bytes(out) + payload


def _esds(codec_private: bytes) -> bytes:
    dec_specific = _desc(0x05, codec_private)
    dec_config = _desc(0x04, bytes([0x40, 0x15]) + b"\0\0\0"
                       + struct.pack(">II", 0, 0) + dec_specific)
    es = _desc(0x03, struct.pack(">HB", 1, 0) + dec_config + _desc(0x06, b"\x02"))
    return full_box(b"esds", 0, 0, es)


def _audio_entry(codec_private: bytes, channels: int, rate: int) -> bytes:
    payload = b"\0" * 6 + struct.pack(">H", 1) + b"\0" * 8
    payload += struct.pack(">HHHHI", channels, 16, 0, 0, int(rate) << 16)
    return box(b"mp4a", payload + _esds(codec_private))


def _stsd(meta: dict) -> bytes:
    if meta["kind"] == "video":
        entry = _video_entry(meta["codec"].encode(), meta["codec_private"],
                             meta["width"], meta["height"])
    else:
        entry = _audio_entry(meta["codec_private"], meta["channels"],
                             meta["timescale"])
    return full_box(b"stsd", 0, 0, struct.pack(">I", 1) + entry)


def _table_u32(tag: bytes, pairs) -> bytes:
    body = struct.pack(">I", len(pairs))
    body += b"".join(struct.pack(">II", a, b) for a, b in pairs)
    return full_box(tag, 0, 0, body)


def _stbl(meta: dict, co64: bool) -> bytes:
    out = _stsd(meta)
    out += _table_u32(b"stts", meta["stts"])
    if meta.get("ctts"):
        out += _table_u32(b"ctts", meta["ctts"])
    out += _table_u32(b"stsc", meta["stsc"])
    sizes = meta["sizes"]
    out += full_box(b"stsz", 0, 0, struct.pack(">II", 0, len(sizes))
                    + b"".join(struct.pack(">I", s) for s in sizes))
    if meta["stss"]:
        out += full_box(b"stss", 0, 0, struct.pack(">I", len(meta["stss"]))
                        + b"".join(struct.pack(">I", s) for s in meta["stss"]))
    fmt = ">Q" if co64 else ">I"
    tag = b"co64" if co64 else b"stco"
    out += full_box(tag, 0, 0, struct.pack(">I", len(meta["offsets"]))
                    + b"".join(struct.pack(fmt, o) for o in meta["offsets"]))
    return box(b"stbl", out)


def _trak(meta: dict, co64: bool) -> bytes:
    is_audio = meta["kind"] == "audio"
    header = full_box(b"vmhd", 0, 1, struct.pack(">HHHH", 0, 0, 0, 0)) \
        if not is_audio else full_box(b"smhd", 0, 0, struct.pack(">HH", 0, 0))
    minf = box(b"minf", header + _dinf() + _stbl(meta, co64))
    mdia = box(b"mdia", _mdhd(meta["timescale"], meta["duration_units"],
                              meta["language"])
               + _hdlr(b"soun" if is_audio else b"vide") + minf)
    tkhd = _tkhd(meta["mp4_track_id"], meta["duration_ms"],
                 meta.get("width", 0), meta.get("height", 0), is_audio)
    return box(b"trak", tkhd + mdia)


def build_moov(metas: list[dict], co64: bool) -> bytes:
    duration_ms = max(m["duration_ms"] for m in metas)
    moov = _mvhd(duration_ms, len(metas) + 1)
    moov += b"".join(_trak(m, co64) for m in metas)
    return box(b"moov", moov)


def mux(write, metas: list[dict], media: list[bytes]) -> dict:
    """Write a complete MP4. ``media[i]`` is the concatenated sample data
    for ``metas[i]``; chunk offsets are computed in a two-pass layout so
    stco/co64 selection is exact. Returns layout statistics."""
    ft = _ftyp(any(m["codec"] == "hvc1" for m in metas))
    total_media = sum(len(b) for b in media)
    mdat_header = 16 if total_media + 8 > index.UINT32_MAX else 8
    co64 = False
    for m in metas:
        m["offsets"] = [0] * len(m["sizes"])
    while True:
        moov_len = len(build_moov(metas, co64))
        base = len(ft) + moov_len + mdat_header
        pos = base
        for m, blob in zip(metas, media):
            m["offsets"], pos = index.chunk_offsets(m["sizes"], pos)
        if not co64 and index.needs_co64(list(m["offsets"]) + [pos - 1]):
            co64 = True  # offsets overflow 32 bits: rebuild with co64
            continue
        moov = build_moov(metas, co64)
        break
    file_size = base + total_media
    write(ft)
    write(moov)
    if mdat_header == 16:
        write(struct.pack(">I4sQ", 1, b"mdat", total_media + 16))
    else:
        write(struct.pack(">I4s", total_media + 8, b"mdat"))
    for blob in media:
        write(blob)
    return {"co64": co64, "file_size": file_size, "mdat_bytes": total_media}
