"""Generate deterministic .pwm media-container samples for local testing.

Run from the project root::

    python3.11 scripts/make_samples.py --out .tmp/samples --seed 17

Repeated runs with the same seed reproduce byte-identical files.  No
network access and no external media tools are involved.
"""
from __future__ import annotations

import argparse
import random
import struct
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent.parent / "src"))

from trackcheck.probe import (
    HEADER_FORMAT,
    HEADER_SIZE,
    MAGIC,
    TRACK_ENTRY_SIZE,
    TRACK_FORMAT,
)
from trackcheck.model import (
    CODEC_AAC,
    CODEC_H264,
    CODEC_SRT,
    KIND_AUDIO,
    KIND_SUBTITLE,
    KIND_VIDEO,
    SUPPORTED_VERSION,
)

# (track_id, kind, codec, timebase, start, samples, width, height,
#  sample_rate, channels, language, flags)
BASE_TRACKS = [
    (1, KIND_VIDEO, CODEC_H264, 24000, 0, 24000, 1920, 1080, 0, 0, "eng", 0),
    (2, KIND_AUDIO, CODEC_AAC, 48000, 0, 48000, 0, 0, 48000, 2, "eng", 0),
    (3, KIND_SUBTITLE, CODEC_SRT, 1000, 0, 1000, 0, 0, 0, 0, "eng", 0),
]
DRIFT_TRACKS = [
    (1, KIND_VIDEO, CODEC_H264, 24000, 0, 24000, 1920, 1080, 0, 0, "eng", 0),
    (2, KIND_AUDIO, CODEC_AAC, 48000, 0, 48000, 0, 0, 44100, 2, "eng", 0),
    (3, KIND_SUBTITLE, CODEC_SRT, 1000, 0, 1120, 0, 0, 0, 0, "eng", 0),
]


def _pad_language(tag: str) -> tuple[int, bytes]:
    raw = tag.encode("ascii")[:16]
    return len(raw), raw + b"\x00" * (16 - len(raw))


def build_container(tracks: list[tuple], seed: int) -> bytes:
    """Serialize a complete, self-consistent .pwm container."""
    rng = random.Random(seed)
    table = bytearray()
    payloads = bytearray()
    for entry in tracks:
        (track_id, kind, codec, timebase, start, samples, width, height,
         rate, channels, language, flags) = entry
        lang_len, lang_raw = _pad_language(language)
        table += struct.pack(
            TRACK_FORMAT, track_id, kind, codec.encode("latin-1"), timebase,
            start, samples, width, height, rate, channels,
            lang_len, lang_raw, flags,
        )
        payloads += bytes(rng.randrange(256) for _ in range(samples))
    body = bytes(table) + bytes(payloads)
    declared = HEADER_SIZE + len(body)
    header = struct.pack(
        HEADER_FORMAT, MAGIC, SUPPORTED_VERSION, HEADER_SIZE,
        len(tracks), declared,
    )
    return header + body


def write_samples(out_dir: Path, seed: int) -> dict[str, Path]:
    out_dir.mkdir(parents=True, exist_ok=True)
    made: dict[str, Path] = {}

    def emit(name: str, data: bytes) -> Path:
        target = out_dir / name
        target.write_bytes(data)
        made[name] = target
        return target

    emit("base.pwm", build_container(BASE_TRACKS, seed))
    emit("drift.pwm", build_container(DRIFT_TRACKS, seed + 1))
    # Same tracks as base but the subtitle label changes.
    renamed = [
        (tid, k, c, tb, st, sc, w, h, sr, ch, ("fra" if tid == 3 else lang), f)
        for (tid, k, c, tb, st, sc, w, h, sr, ch, lang, f) in BASE_TRACKS
    ]
    emit("renamed.pwm", build_container(renamed, seed + 2))
    # Missing the subtitle track entirely.
    emit("missing.pwm", build_container(BASE_TRACKS[:2], seed + 3))

    good = build_container(BASE_TRACKS, seed)
    emit("truncated.pwm", good[: len(good) - 256])

    # Declared length lies about the real file size.
    header = struct.pack(
        HEADER_FORMAT, MAGIC, SUPPORTED_VERSION, HEADER_SIZE,
        len(BASE_TRACKS), len(good) + 100,
    )
    emit("badlength.pwm", header + good[HEADER_SIZE:])

    # Unknown container version (header otherwise intact).
    bad_version = struct.pack(
        HEADER_FORMAT, MAGIC, SUPPORTED_VERSION + 9, HEADER_SIZE,
        len(BASE_TRACKS), len(good),
    ) + good[HEADER_SIZE:]
    emit("badversion.pwm", bad_version)

    # A track entry claims a payload far larger than the actual blob:
    # the file stays small but the declared sample_count overruns it.
    small = list(BASE_TRACKS[0])
    small[5] = 64
    small_blob = build_container([tuple(small)], seed + 4)
    lying = list(BASE_TRACKS[0])
    lying[5] = 10_000_000  # sample_count / payload length claim
    lying_lang_len, lying_lang_raw = _pad_language(lying[10])
    lying_entry = struct.pack(
        TRACK_FORMAT, lying[0], lying[1], lying[2].encode("latin-1"),
        lying[3], lying[4], lying[5], lying[6], lying[7], lying[8],
        lying[9], lying_lang_len, lying_lang_raw, lying[11],
    )
    real_payload = small_blob[HEADER_SIZE + TRACK_ENTRY_SIZE:]
    lying_declared = HEADER_SIZE + TRACK_ENTRY_SIZE + len(real_payload)
    lying_header = struct.pack(
        HEADER_FORMAT, MAGIC, SUPPORTED_VERSION, HEADER_SIZE, 1, lying_declared,
    )
    emit("overrun.pwm", lying_header + lying_entry + real_payload)
    return made


def main() -> int:
    parser = argparse.ArgumentParser(description="Generate .pwm sample files")
    parser.add_argument("--out", required=True, type=Path)
    parser.add_argument("--seed", type=int, default=17)
    args = parser.parse_args()
    made = write_samples(args.out, args.seed)
    for name in sorted(made):
        print(f"wrote {made[name]} ({made[name].stat().st_size} bytes)")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
