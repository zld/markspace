"""Deterministic fMP4 fixture generator; no external assets involved.

Layout: ftyp, moov (two tracks with full stbl sample tables), one mdat
holding the sample-table chunks, a version-1 sidx, then three
moof+mdat fragments. A valid fixture rebuilds byte-identically, which
the pipeline tests rely on. Corruption knobs produce the failure modes
the validator and repairer must catch.
"""
from __future__ import annotations

import struct

from .repair import build_sidx

TIMESCALE_VIDEO = 15360
TIMESCALE_AUDIO = 48000
VIDEO_STTS = [(100, 512), (20, 256)]   # 120 samples
AUDIO_STTS = [(96, 1024)]              # 96 samples, constant 64-byte size
FRAGMENT_DURATION = 7680               # 0.5s at the video timescale
FRAGMENT_COUNT = 3

CORRUPTIONS = (
    "sidx-offset", "sidx-dup", "sidx-overflow", "no-sidx",
    "stts-count", "tfdt-rollback", "truncate", "bad-box-size",
    "unclosed", "orphan-mdat",
)


def _box(t, payload):
    return struct.pack(">I4s", 8 + len(payload), t) + payload


def _full(t, version, flags, payload):
    return _box(t, bytes((version,)) + flags.to_bytes(3, "big") + payload)


def _sample_sizes():
    return [100 + (i * 37) % 61 for i in range(120)]


def _stbl(timescale_entries, stsc_entries, stsz_payload, chunk_offsets):
    stts = _full(b"stts", 0, 0,
                 struct.pack(">I", len(timescale_entries))
                 + b"".join(struct.pack(">II", c, d)
                            for c, d in timescale_entries))
    stsc = _full(b"stsc", 0, 0,
                 struct.pack(">I", len(stsc_entries))
                 + b"".join(struct.pack(">III", *e) for e in stsc_entries))
    stsz = _full(b"stsz", 0, 0, stsz_payload)
    stco = _full(b"stco", 0, 0,
                 struct.pack(">I", len(chunk_offsets))
                 + b"".join(struct.pack(">I", o) for o in chunk_offsets))
    return _box(b"stbl", stts + stsc + stsz + stco)


def _trak(track_id, timescale, stbl):
    tkhd = _full(b"tkhd", 0, 7, struct.pack(">IIIII", 0, 0, track_id, 0, 0)
                 + b"\x00" * 72)
    mdhd = _full(b"mdhd", 0, 0, struct.pack(">IIIIHH", 0, 0, timescale, 0,
                                            0x55C4, 0))
    minf = _box(b"minf", stbl)
    return _box(b"trak", tkhd + _box(b"mdia", mdhd + minf))


def _moov(video_offsets, audio_offsets):
    mvhd = _full(b"mvhd", 0, 0, struct.pack(">IIII", 0, 0, 1000, 0)
                 + b"\x00" * 80)
    sizes = _sample_sizes()
    stsz_v = struct.pack(">II", 0, len(sizes)) + b"".join(
        struct.pack(">I", s) for s in sizes)
    stsz_a = struct.pack(">II", 64, 96)
    video = _trak(1, TIMESCALE_VIDEO,
                  _stbl(VIDEO_STTS, [(1, 40, 1)], stsz_v, video_offsets))
    audio = _trak(2, TIMESCALE_AUDIO,
                  _stbl(AUDIO_STTS, [(1, 48, 1)], stsz_a, audio_offsets))
    return _box(b"moov", mvhd + video + audio)


def _moof(index, tfdt):
    mfhd = _full(b"mfhd", 0, 0, struct.pack(">I", index + 1))
    tfhd = _full(b"tfhd", 0, 0, struct.pack(">I", 1))
    tfdt_box = _full(b"tfdt", 1, 0, struct.pack(">Q", tfdt))
    return _box(b"moof", mfhd + _box(b"traf", tfhd + tfdt_box))


def _fill(length, seed):
    return bytes(((i * 31 + seed) & 0xFF) for i in range(length))


def build_fixture(corrupt=None):
    """Return fixture bytes; `corrupt` selects a deterministic defect."""
    if corrupt is not None and corrupt not in CORRUPTIONS:
        raise ValueError(f"unknown corruption {corrupt!r}")
    sizes = _sample_sizes()
    video_chunks = [sum(sizes[i * 40:(i + 1) * 40]) for i in range(3)]
    audio_chunks = [48 * 64, 48 * 64]
    chunk_sizes = video_chunks + audio_chunks

    ftyp = _box(b"ftyp", b"isom" + struct.pack(">I", 0) + b"isomiso6mp41")
    moov_len = len(_moov([0] * 3, [0] * 2))
    mdat1_off = len(ftyp) + moov_len
    offsets, pos = [], mdat1_off + 8
    for size in chunk_sizes:
        offsets.append(pos)
        pos += size
    moov = _moov(offsets[:3], offsets[3:])
    mdat1 = _box(b"mdat", b"".join(_fill(n, 7 + k)
                                   for k, n in enumerate(chunk_sizes)))

    tfdts = [k * FRAGMENT_DURATION for k in range(FRAGMENT_COUNT)]
    if corrupt == "tfdt-rollback":
        tfdts[-1] = 100
    fragments = b""
    frag_lengths = []
    for k, tfdt in enumerate(tfdts):
        frag = _moof(k, tfdt) + _box(b"mdat", _fill(2000 + k * 500, 100 + k))
        frag_lengths.append(len(frag))
        fragments += frag

    sidx = build_sidx(1, TIMESCALE_VIDEO, 0,
                      [FRAGMENT_DURATION] * FRAGMENT_COUNT, frag_lengths)
    data = ftyp + moov + mdat1
    if corrupt != "no-sidx":
        data += sidx
    if corrupt == "sidx-dup":
        data += sidx  # second copy: overlapping, misaligned references
    if corrupt == "orphan-mdat":
        data += _box(b"mdat", _fill(300, 255))  # unattributable mdat
    data += fragments

    if corrupt == "sidx-offset":
        # first_offset lives 28 bytes into the sidx box (v1 layout)
        at = data.index(b"sidx") - 4 + 28
        data = data[:at] + struct.pack(">q", 64) + data[at + 8:]
    elif corrupt == "sidx-overflow":
        at = data.index(b"sidx") - 4 + 20  # earliest_presentation_time
        data = data[:at] + struct.pack(">Q", 0xFFFFFFFFFFFFFFFF) + data[at + 8:]
    elif corrupt == "stts-count":
        at = data.index(b"stts") - 4 + 16  # first entry's sample_count
        data = data[:at] + struct.pack(">I", 90) + data[at + 4:]
    elif corrupt == "bad-box-size":
        at = len(ftyp)  # moov size field
        data = data[:at] + struct.pack(">I", moov_len + 16) + data[at + 4:]
    elif corrupt == "truncate":
        data = data[:-100]
    elif corrupt == "unclosed":
        data += _moof(FRAGMENT_COUNT, FRAGMENT_COUNT * FRAGMENT_DURATION)
    return data


def write_fixture(path, corrupt=None):
    with open(path, "wb") as fh:
        fh.write(build_fixture(corrupt))
    return path
