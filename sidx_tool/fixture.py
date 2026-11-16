"""Deterministic classic-MP4 fixture generator.

Builds an unencrypted, non-fragmented MP4 with one video and one audio track,
configurable sample counts/sizes, multiple mdat boxes and a selectable SIDX
state (fresh / stale / missing / damaged).  All payload bytes derive from a
seeded PRNG, so identical parameters always produce byte-identical files, and
the damage variants reproduce the exact failure chains described in the spec.
"""

from __future__ import annotations

import struct
from dataclasses import dataclass
from pathlib import Path

from .errors import FormatError

VIDEO_TIMESCALE = 30
AUDIO_TIMESCALE = 48000
VIDEO_SAMPLES_PER_SEG = 15  # 30 fps -> 500 ms per segment
AUDIO_SAMPLES_PER_SEG = 15


class SeededRandom:
    """Tiny deterministic LCG so fixtures never depend on ``random`` seeding."""

    def __init__(self, seed: int):
        self._state = seed & 0xFFFFFFFF or 1

    def next_u16(self) -> int:
        self._state = (1103515245 * self._state + 12345) & 0x7FFFFFFF
        return self._state & 0xFFFF


@dataclass
class FixtureConfig:
    seed: int = 41
    samples_per_track: int = 60
    sample_bytes: int = 1000
    multi_mdat: bool = True
    # fresh | stale | missing | damaged
    index_state: str = "stale"


def _box(box_type: bytes, payload: bytes) -> bytes:
    return struct.pack(">I", 8 + len(payload)) + box_type + payload


def _fullbox(box_type: bytes, payload: bytes, version: int = 0, flags: int = 0) -> bytes:
    return _box(box_type, bytes([version]) + flags.to_bytes(3, "big") + payload)


def _sample_payload(rng: SeededRandom, track_id: int, index: int, size: int) -> bytes:
    tag = struct.pack(">HH", track_id & 0xFFFF, index & 0xFFFF)
    body = bytes(rng.next_u16() & 0xFF for _ in range(size - 4))
    return tag + body


def build_fixture(config: FixtureConfig) -> bytes:
    if config.samples_per_track <= 0 or config.sample_bytes <= 0:
        raise FormatError("fixture sample counts and sizes must be positive")
    if config.index_state not in {"fresh", "stale", "missing", "damaged"}:
        raise FormatError(f"unknown index_state {config.index_state!r}")
    rng = SeededRandom(config.seed)
    n = config.samples_per_track
    size = config.sample_bytes
    video_payload = [_sample_payload(rng, 1, i, size) for i in range(n)]
    audio_payload = [_sample_payload(rng, 2, i, size) for i in range(n)]
    ftyp = _box(b"ftyp", b"isom" + struct.pack(">I", 0x200) + b"isomiso2mp41")
    mdats, relative_plan = _build_mdat(video_payload, audio_payload, config)
    # Pass 1: measure prefix size with placeholder zero offsets.
    placeholder = ChunkPlan([0] * len(relative_plan.video_offsets),
                            [0] * len(relative_plan.audio_offsets), relative_plan.segments)
    probe_moov = _build_moov(n, size, placeholder)
    probe_sidx = b"" if config.index_state == "missing" else _build_sidx(relative_plan, config.index_state)
    media_origin = len(ftyp) + len(probe_moov) + len(probe_sidx)
    absolute = ChunkPlan(
        [media_origin + off for off in relative_plan.video_offsets],
        [media_origin + off for off in relative_plan.audio_offsets],
        relative_plan.segments,
    )
    # Pass 2: moov is offset-agnostic in size (same field widths), reuse probe.
    moov = _build_moov(n, size, absolute)
    parts = [ftyp, moov]
    if config.index_state != "missing":
        parts.append(_build_sidx(relative_plan, config.index_state))
    parts.extend(mdat.serialized for mdat in mdats)
    return b"".join(parts)


def write_fixture(config: FixtureConfig, out: str | Path) -> Path:
    out = Path(out)
    out.parent.mkdir(parents=True, exist_ok=True)
    out.write_bytes(build_fixture(config))
    return out


@dataclass
class MdatSpec:
    serialized: bytes
    payload_size: int


@dataclass
class ChunkPlan:
    video_offsets: list[int]
    audio_offsets: list[int]
    # Per segment: (video payload size, audio payload size, duration ms)
    segments: list[tuple[int, int, int]]


def _build_moov(n: int, sample_size: int, plan: ChunkPlan) -> bytes:
    video_dur = n  # stts delta 1 tick/sample; last DTS 59 + 1 = 60 ticks = 2000 ms
    audio_dur = n * 1600  # stts delta 1600 ticks/sample; 60 samples = 2000 ms
    movie_dur = video_dur * 1000 // VIDEO_TIMESCALE  # 2000 ms in mvhd timescale 1000
    mvhd = _fullbox(b"mvhd", _mvhd_payload(1000, movie_dur))
    video_trak = _build_trak(1, b"vide", VIDEO_TIMESCALE, video_dur, n, sample_size,
                             VIDEO_SAMPLES_PER_SEG, plan.video_offsets)
    audio_trak = _build_trak(2, b"soun", AUDIO_TIMESCALE, audio_dur, n, sample_size,
                             VIDEO_SAMPLES_PER_SEG, plan.audio_offsets)
    return _box(b"moov", mvhd + video_trak + audio_trak)


def _mvhd_payload(timescale: int, duration: int) -> bytes:
    return (
        b"\x00" * 4  # creation_time
        + b"\x00" * 4  # modification_time
        + struct.pack(">II", timescale, duration)
        + struct.pack(">I", 0x00010000)  # rate 1.0
        + struct.pack(">H", 0x0100)  # volume 1.0
        + b"\x00" * 10  # reserved
        + b"\x00\x01\x00\x00" + b"\x00" * 4 + b"\x00" * 4
        + b"\x00" * 4 + b"\x00\x01\x00\x00" + b"\x00" * 4
        + b"\x00" * 4 + b"\x00" * 4 + b"\x40\x00\x00\x00"
        + b"\x00" * 24  # pre-defined
        + struct.pack(">I", 2)  # next_track_ID
    )


def _build_trak(track_id: int, handler: bytes, timescale: int, duration: int,
                n: int, sample_size: int, group: int, chunk_offsets: list[int]) -> bytes:
    tkhd = _fullbox(b"tkhd", struct.pack(">IIII", 0, 0, track_id, 0)
                    + struct.pack(">I", duration) + b"\x00" * 8
                    + struct.pack(">HHh", 0, 0, 0) + b"\x00" * 2
                    + struct.pack(">IIHH", 0x00010000, 0, 0, 0)
                    + b"\x00\x01\x00\x00" + b"\x00" * 4 + b"\x00" * 4
                    + b"\x00" * 4 + b"\x00\x01\x00\x00" + b"\x00" * 4
                    + b"\x00" * 4 + b"\x00" * 4 + b"\x40\x00\x00\x00"
                    + b"\x00" * 24 + struct.pack(">II", 320 << 16, 240 << 16))
    mdhd = _fullbox(b"mdhd", struct.pack(">IIII", 0, 0, timescale, duration) + struct.pack(">HH", 0x55C4, 0))
    hdlr = _fullbox(b"hdlr", b"\x00" * 4 + handler + b"\x00" * 12 + b"VideoHandler\x00"
                    if handler == b"vide" else b"\x00" * 4 + handler + b"\x00" * 12 + b"SoundHandler\x00")
    sample_entry_type = b"avc1" if handler == b"vide" else b"mp4a"
    sample_entry = _box(sample_entry_type, b"\x00" * 6 + struct.pack(">H", 1)
                        + b"\x00" * 16 + struct.pack(">HH", 320, 240) + b"\x00" * 50)
    stsd = _fullbox(b"stsd", struct.pack(">I", 1) + sample_entry)
    stts = _fullbox(b"stts", struct.pack(">III", 1, n, 1 if handler == b"vide" else 1600))
    stsc = _fullbox(b"stsc", struct.pack(">IIII", 1, 1, group, 1))
    stsz = _fullbox(b"stsz", struct.pack(">II", 0, n) + b"".join(struct.pack(">I", sample_size) for _ in range(n)))
    chunk_count = (n + group - 1) // group
    stco = _fullbox(b"stco", struct.pack(">I", chunk_count)
                    + b"".join(struct.pack(">I", offset) for offset in chunk_offsets))
    stss = b""
    if handler == b"vide":
        sync = list(range(1, n + 1, group))
        stss = _fullbox(b"stss", struct.pack(">I", len(sync)) + b"".join(struct.pack(">I", s) for s in sync))
    stbl = _box(b"stbl", stsd + stts + stsc + stsz + stco + stss)
    dinf = _box(b"dinf", _fullbox(b"dref", struct.pack(">II", 0, 1)
                                  + _fullbox(b"url ", b"", flags=1)))
    minf = _box(b"minf", _fullbox(b"vmhd" if handler == b"vide" else b"smhd",
                                  b"\x00" * 8 if handler == b"vide" else b"\x00" * 4)
                + dinf + stbl)
    mdia = _box(b"mdia", mdhd + hdlr + minf)
    return _box(b"trak", tkhd + mdia)


def _build_mdat(video_payload, audio_payload, config: FixtureConfig) -> tuple[list[MdatSpec], ChunkPlan]:
    """Build mdat boxes and the chunk offsets their stco tables must record."""
    n = config.samples_per_track
    group = VIDEO_SAMPLES_PER_SEG
    chunk_count = (n + group - 1) // group
    mdats: list[MdatSpec] = []
    video_offsets: list[int] = []
    audio_offsets: list[int] = []
    segments: list[tuple[int, int, int]] = []
    if config.multi_mdat:
        for chunk_index in range(chunk_count):
            lo, hi = chunk_index * group, min(n, (chunk_index + 1) * group)
            vdata = b"".join(video_payload[lo:hi])
            adata = b"".join(audio_payload[lo:hi])
            mdats.append(MdatSpec(_box(b"mdat", vdata), len(vdata)))
            mdats.append(MdatSpec(_box(b"mdat", adata), len(adata)))
            segments.append((len(vdata), len(adata), 500))
    else:
        vdata, adata = b"".join(video_payload), b"".join(audio_payload)
        mdats.append(MdatSpec(_box(b"mdat", vdata + adata), len(vdata) + len(adata)))
        segments.append((len(vdata), len(adata), 500 * chunk_count))
    # Relative offsets inside the media region (absolute prefix added later).
    cursor = 0
    if config.multi_mdat:
        for index in range(chunk_count):
            video_offsets.append(cursor + 8)
            cursor += 8 + segments[index][0]
            audio_offsets.append(cursor + 8)
            cursor += 8 + segments[index][1]
    else:
        # One mdat: all video chunks first, then all audio chunks, each chunk
        # packed back-to-back at its sample size * group length.
        inner = 8
        for index in range(chunk_count):
            lo, hi = index * group, min(n, (index + 1) * group)
            video_offsets.append(inner)
            inner += (hi - lo) * config.sample_bytes
        for index in range(chunk_count):
            lo, hi = index * group, min(n, (index + 1) * group)
            audio_offsets.append(inner)
            inner += (hi - lo) * config.sample_bytes
    plan = ChunkPlan(video_offsets, audio_offsets, segments)
    return mdats, plan


def _build_sidx(plan: ChunkPlan, state: str) -> bytes:
    timescale = VIDEO_TIMESCALE
    refs = b""
    for vsize, asize, duration_ms in plan.segments:
        size = vsize + asize + (16 if len(plan.segments) > 1 or vsize != asize else 8)
        duration_ticks = duration_ms * timescale // 1000
        refs += struct.pack(">III", size & 0x7FFFFFFF, duration_ticks, 0x90000000)
    payload = struct.pack(">IIII", 1, timescale, 0, 0) + struct.pack(">H", 0) + struct.pack(">H", len(plan.segments)) + refs
    if state == "stale":
        # Deliberately wrong referenced_size: claims 7 extra bytes per segment.
        stale = b""
        for vsize, asize, duration_ms in plan.segments:
            size = vsize + asize + 23
            stale += struct.pack(">III", size & 0x7FFFFFFF, duration_ms * timescale // 1000, 0x90000000)
        payload = struct.pack(">IIII", 1, timescale, 0, 0) + struct.pack(">H", 0) + struct.pack(">H", len(plan.segments)) + stale
    elif state == "damaged":
        payload = payload[:8] + b"\xff\xff\xff"  # truncated before required fields
    return _fullbox(b"sidx", payload)
