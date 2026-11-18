#!/usr/bin/env python3.11
"""Generate deterministic sample containers for mediacheck.

The fixtures exercise two hand-rolled container subsets:

* EBML/Matroska-like files with a Segment/Info/Tracks structure.
* ISO BMFF files with ftyp/moov/trak/mdia/stbl boxes.

No third-party media tooling is used; the byte layouts are written directly.
"""

from __future__ import annotations

import argparse
import struct
from pathlib import Path

# ---------------------------------------------------------------------------
# EBML writer
# ---------------------------------------------------------------------------


def vint_size(payload: int, minimum: int = 1) -> bytes:
    """Encode an EBML data size as a variable-length integer."""

    for length in range(minimum, 9):
        if payload < (1 << (7 * length)) - 1:
            value = payload | (1 << (7 * length))
            return value.to_bytes(length, "big")
    raise ValueError(f"size {payload} does not fit in an EBML vint")


def elem(element_id: int, payload: bytes) -> bytes:
    id_bytes = element_id.to_bytes((element_id.bit_length() + 7) // 8, "big")
    return id_bytes + vint_size(len(payload)) + payload


def uint(value: int) -> bytes:
    width = max(1, (value.bit_length() + 7) // 8)
    return value.to_bytes(width, "big")


def f64(value: float) -> bytes:
    return struct.pack(">d", value)


def ebml_file(doc_type: str, timecode_scale: int, duration_ticks: float | None,
              tracks: list[dict]) -> bytes:
    header = elem(0x4282, doc_type.encode("ascii"))  # DocType
    header = elem(0x1A45DFA3, header)  # EBML
    info_body = elem(0x2AD7B1, uint(timecode_scale))  # TimecodeScale
    if duration_ticks is not None:
        info_body += elem(0x4489, f64(duration_ticks))  # Duration
    info = elem(0x1549A966, info_body)  # Info
    track_payload = b""
    for spec in tracks:
        body = elem(0xD7, uint(spec["number"]))  # TrackNumber
        body += elem(0x83, uint(spec["type"]))  # TrackType
        body += elem(0x86, spec["codec"].encode("ascii"))  # CodecID
        if spec.get("codec_delay_ns") is not None:
            body += elem(0x56AA, uint(spec["codec_delay_ns"]))  # CodecDelay
        if spec.get("duration_ticks") is not None:
            body += elem(0x4D7D, f64(spec["duration_ticks"]))  # TrackDuration
        for element_id, key in ((0xB0, "width"), (0xBA, "height"),
                                (0xB5, "sample_rate"), (0x9F, "channels")):
            if spec.get(key) is not None:
                value = spec[key]
                payload = f64(float(value)) if key == "sample_rate" else uint(value)
                body += elem(element_id, payload)
        track_payload += elem(0xAE, body)  # TrackEntry
    tracks_element = elem(0x1654AE6B, track_payload)  # Tracks
    segment = elem(0x18538067, info + tracks_element)  # Segment
    return header + segment


# ---------------------------------------------------------------------------
# ISO BMFF writer
# ---------------------------------------------------------------------------


def box(box_type: bytes, payload: bytes) -> bytes:
    return struct.pack(">I", 8 + len(payload)) + box_type + payload


def full_box(box_type: bytes, version: int, flags: int, payload: bytes) -> bytes:
    return box(box_type, bytes([version]) + flags.to_bytes(3, "big") + payload)


def bmff_file(*, movie_timescale: int = 1000, movie_duration: int = 5000,
              tracks: list[dict], major_brand: str = "isom") -> bytes:
    ftyp = box(b"ftyp", major_brand.encode("ascii") + b"\x00\x00\x02\x00" +
               b"isomiso2mp41")
    mvhd_body = (struct.pack(">II", 0, 0) + struct.pack(">I", movie_timescale) +
                 struct.pack(">I", movie_duration) + b"\x00" * 80)
    mvhd = box(b"mvhd", bytes([0, 0, 0, 0]) + mvhd_body)
    traks = b""
    for spec in tracks:
        tkhd_body = (struct.pack(">II", 0, 0) + struct.pack(">I", spec["track_id"]) +
                     struct.pack(">I", 0) + struct.pack(">I", movie_duration) +
                     b"\x00" * 60)
        trak = box(b"tkhd", bytes([0, 0, 0, 7]) + tkhd_body)
        if spec.get("edit_start_media_time") is not None:
            elst_body = (struct.pack(">I", 1) + struct.pack(">I", movie_duration) +
                         struct.pack(">i", spec["edit_start_media_time"]) +
                         struct.pack(">i", 0x00010000))
            trak += box(b"edts", full_box(b"elst", 0, 0, elst_body))
        mdhd_body = (struct.pack(">II", 0, 0) +
                     struct.pack(">I", spec["media_timescale"]) +
                     struct.pack(">I", spec["media_duration"]) +
                     struct.pack(">I", 0) + struct.pack(">HH", 0x55C4, 0))
        mdia = box(b"mdhd", bytes([0, 0, 0, 0]) + mdhd_body)
        mdia += full_box(b"hdlr", 0, 0,
                         struct.pack(">I", 0) + spec["handler"].encode("ascii") +
                         b"\x00" * 12 + b"handler\x00")
        if spec["handler"] == "vide":
            entry_payload = (b"\x00" * 6 + struct.pack(">H", 1) + b"\x00" * 16 +
                             struct.pack(">HH", spec["width"], spec["height"]) +
                             b"\x00" * 12)
        else:
            entry_payload = (b"\x00" * 6 + struct.pack(">H", 1) + b"\x00" * 8 +
                             struct.pack(">H", spec["channels"]) + b"\x00" * 6 +
                             struct.pack(">I", spec["sample_rate"] << 16))
        entry_type = b"avc1" if spec["handler"] == "vide" else b"mp4a"
        stsd = full_box(b"stsd", 0, 0, struct.pack(">I", 1) +
                        box(entry_type, entry_payload))
        stbl = box(b"stbl", stsd)
        mdia += box(b"minf", stbl)
        trak += box(b"mdia", mdia)
        traks += box(b"trak", trak)
    moov = box(b"moov", mvhd + traks)
    mdat = box(b"mdat", b"\x00" * 64)
    return ftyp + moov + mdat


SAMPLE_SPECS = {
    "clip-a.mkv": ("ebml", dict(
        doc_type="matroska", timecode_scale=1_000_000, duration_ticks=5000.0,
        tracks=[
            dict(number=1, type=1, codec="V_MPEG4/ISO/AVC", codec_delay_ns=0,
                 duration_ticks=5000.0, width=640, height=360),
            dict(number=2, type=2, codec="A_AAC", codec_delay_ns=0,
                 duration_ticks=5000.0, sample_rate=48000, channels=2),
        ],
    )),
    "clip-b.mkv": ("ebml", dict(
        doc_type="matroska", timecode_scale=1_000_000, duration_ticks=5080.0,
        tracks=[
            dict(number=1, type=1, codec="V_MPEG4/ISO/AVC", codec_delay_ns=0,
                 duration_ticks=5080.0, width=640, height=360),
            dict(number=2, type=2, codec="A_AAC", codec_delay_ns=0,
                 duration_ticks=5080.0, sample_rate=48000, channels=2),
        ],
    )),
    "clip-c.mp4": ("bmff", dict(
        movie_timescale=1000, movie_duration=5000,
        tracks=[dict(track_id=1, handler="vide", media_timescale=90000,
                     media_duration=450000, width=1280, height=720)],
    )),
}


def write_samples(out: Path) -> list[Path]:
    out.mkdir(parents=True, exist_ok=True)
    written = []
    for name in sorted(SAMPLE_SPECS):
        kind, spec = SAMPLE_SPECS[name]
        data = ebml_file(**spec) if kind == "ebml" else bmff_file(**spec)
        target = out / name
        target.write_bytes(data)
        written.append(target)
    return written


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--out", required=True, type=Path, help="output directory")
    args = parser.parse_args(argv)
    for path in write_samples(args.out):
        print(f"wrote {path} ({path.stat().st_size} bytes)")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
