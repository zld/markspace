#!/usr/bin/env python3.11
"""Generate deterministic local media fixtures.

The generated tree holds exactly four containers:

``reordered.mp4``
    the two-track baseline (video + English audio) with the tracks emitted
    in the opposite order; reordering alone must not be a difference.
``drift.mp4``
    same two tracks, video duration drifts by exactly the 80 ms tolerance.
``params.wav``
    same 1000 ms duration as the baseline WAV, re-exported at 44.1 kHz mono.
``truncated.mp4``
    a single-track MP4 whose ``mdat`` is cut short.

``baseline.json`` embeds the expected record for each file (including the
pristine two-track MP4 that is not written to disk), so ``compare`` can
attach every fixture to its baseline by relative path.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import struct
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / "src"))

from mediacheck.sniff import sniff_file  # noqa: E402
from mediacheck.util import atomic_write_json  # noqa: E402

TOOL_VERSION = "0.1.0"
VIDEO_TIMESCALE = 600
AUDIO_TIMESCALE = 48000
VIDEO_DURATION_MS = 3000.0
AUDIO_DURATION_MS = 1300.0
DRIFT_MS = 80.0


def _box(type_name: str, payload: bytes) -> bytes:
    return struct.pack(">I4s", 8 + len(payload), type_name.encode("latin-1")) + payload


def _full_box(type_name: str, version: int, flags: int, payload: bytes) -> bytes:
    return _box(type_name, bytes([version]) + flags.to_bytes(3, "big") + payload)


def _ftyp() -> bytes:
    return _box("ftyp", b"isom" + struct.pack(">I", 512) + b"isomiso2mp41")


def _mvhd(duration_ms: float) -> bytes:
    timescale = VIDEO_TIMESCALE
    duration = int(round(duration_ms / 1000.0 * timescale))
    payload = struct.pack(">II", 0, 0)
    payload += struct.pack(">II", timescale, duration)
    payload += struct.pack(">I", 0x00010000)
    payload += struct.pack(">H", 0x0100)
    payload += b"\x00" * 10
    payload += struct.pack(">9I", 0x00010000, 0, 0, 0, 0x00010000, 0, 0, 0, 0x40000000)
    payload += b"\x00" * 24
    payload += struct.pack(">I", 2)
    return _full_box("mvhd", 0, 0, payload)


def _language_bytes(code: str) -> bytes:
    packed = 0
    for char in code:
        packed = (packed << 5) | (ord(char) - 0x60)
    return packed.to_bytes(2, "big")


def _mdhd(duration_ms: float, language: str, timescale: int) -> bytes:
    duration = int(round(duration_ms / 1000.0 * timescale))
    payload = struct.pack(">II", 0, 0)
    payload += struct.pack(">II", timescale, duration)
    payload += _language_bytes(language)
    payload += struct.pack(">H", 0)
    return _full_box("mdhd", 0, 0, payload)


def _hdlr(handler: str) -> bytes:
    payload = b"\x00" * 4 + handler.encode("latin-1") + b"\x00" * 12 + b"mediacheck\x00"
    return _full_box("hdlr", 0, 0, payload)


def _audio_entry(codec: str, channels: int, sample_size: int, rate: int) -> bytes:
    body = b"\x00" * 6
    body += struct.pack(">H", 1)
    body += b"\x00" * 8
    body += struct.pack(">H", channels)
    body += struct.pack(">H", sample_size)
    body += struct.pack(">HH", 0, 0)
    body += struct.pack(">I", rate << 16)
    return struct.pack(">I4s", 8 + len(body), codec.encode("latin-1")) + body


def _video_entry(codec: str, width: int = 1920, height: int = 1080) -> bytes:
    body = b"\x00" * 6
    body += struct.pack(">H", 1)
    body += b"\x00" * 16
    body += struct.pack(">HH", width, height)
    body += struct.pack(">II", 0x00480000, 0x00480000)
    body += struct.pack(">I", 0)
    body += struct.pack(">H", 1)
    body += b"\x00" * 32
    body += struct.pack(">Hh", 0x18, -1)
    return struct.pack(">I4s", 8 + len(body), codec.encode("latin-1")) + body


def _stsd(entry: bytes) -> bytes:
    return _full_box("stsd", 0, 0, struct.pack(">I", 1) + entry)


def _stts(delta: int, count: int) -> bytes:
    payload = struct.pack(">I", 1) + struct.pack(">II", count, delta)
    return _full_box("stts", 0, 0, payload)


def _trak_audio(language: str, duration_ms: float, channels: int = 2, rate: int = 48000) -> bytes:
    entry = _audio_entry("mp4a", channels, 16, rate)
    stbl = _box("stbl", _stsd(entry) + _box("stts", _stts(1024, 1)))
    mdia = _box(
        "mdia",
        _mdhd(duration_ms, language, AUDIO_TIMESCALE) + _hdlr("soun") + _box("minf", stbl),
    )
    return _box("trak", mdia)


def _trak_video(language: str, duration_ms: float) -> bytes:
    entry = _video_entry("avc1")
    stbl = _box("stbl", _stsd(entry) + _box("stts", _stts(1000, 1)))
    mdia = _box(
        "mdia",
        _mdhd(duration_ms, language, VIDEO_TIMESCALE) + _hdlr("vide") + _box("minf", stbl),
    )
    return _box("trak", mdia)


def _movie(traks: list[bytes], duration_ms: float) -> bytes:
    return _box("moov", _mvhd(duration_ms) + b"".join(traks))


def _mp4(traks: list[bytes], duration_ms: float, payload_size: int = 64) -> bytes:
    return _ftyp() + _movie(traks, duration_ms) + _box("mdat", b"\x00" * payload_size)


def baseline_tracks() -> tuple[bytes, bytes]:
    video = _trak_video("und", VIDEO_DURATION_MS)
    audio = _trak_audio("eng", AUDIO_DURATION_MS)
    return video, audio


def build_baseline_mp4() -> bytes:
    video, audio = baseline_tracks()
    return _mp4([video, audio], VIDEO_DURATION_MS)


def build_reordered_mp4() -> bytes:
    video, audio = baseline_tracks()
    return _mp4([audio, video], VIDEO_DURATION_MS)


def build_drift_mp4() -> bytes:
    video, audio = baseline_tracks()
    drifted = _trak_video("und", VIDEO_DURATION_MS + DRIFT_MS)
    return _mp4([drifted, audio], VIDEO_DURATION_MS + DRIFT_MS)


def build_truncated_mp4() -> bytes:
    audio = _trak_audio("eng", AUDIO_DURATION_MS)
    payload = _mp4([audio], AUDIO_DURATION_MS, payload_size=8192)
    return payload[:-4096]


def _build_wav(sample_rate: int, channels: int, bits: int, duration_ms: float) -> bytes:
    block_align = channels * bits // 8
    byte_rate = sample_rate * block_align
    data_size = int(round(duration_ms / 1000.0 * byte_rate))
    fmt = struct.pack("<HHIIHH", 1, channels, sample_rate, byte_rate, block_align, bits)
    body = (
        b"WAVE"
        + b"fmt " + struct.pack("<I", len(fmt)) + fmt
        + b"JUNK" + struct.pack("<I", 4) + b"\x00" * 4
        + b"LIST" + struct.pack("<I", 10) + b"INFOxxxx" + b"\x00\x00"
        + b"data" + struct.pack("<I", data_size) + b"\x00" * data_size
    )
    return b"RIFF" + struct.pack("<I", len(body)) + body


def build_baseline_wav() -> bytes:
    return _build_wav(48000, 2, 16, 1000.0)


def build_params_wav() -> bytes:
    return _build_wav(44100, 1, 16, 1000.0)


def _write(path: Path, payload: bytes) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_bytes(payload)


def _sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with open(path, "rb") as handle:
        for chunk in iter(lambda: handle.read(1 << 20), b""):
            digest.update(chunk)
    return digest.hexdigest()


def _record(path: Path) -> dict:
    result = sniff_file(path)
    record = result.to_dict()
    record["sha256"] = _sha256(path)
    return record


def create_samples(out: Path) -> dict:
    out.mkdir(parents=True, exist_ok=True)
    if any(out.iterdir()):
        raise SystemExit(f"refusing to overwrite non-empty directory: {out}")

    staging = out.parent / f".{out.name}.staging"
    if staging.exists():
        raise SystemExit(f"staging directory already exists: {staging}")
    staging.mkdir(parents=True)

    # Write pristine fixtures to staging so their expected records are
    # sniffed by the same code path the CLI uses.
    _write(staging / "baseline.mp4", build_baseline_mp4())
    _write(staging / "baseline.wav", build_baseline_wav())
    _write(staging / "single.mp4", build_truncated_mp4() + b"\x00" * 4096)

    expected = {
        "reordered.mp4": _record(staging / "baseline.mp4"),
        "drift.mp4": _record(staging / "baseline.mp4"),
        "params.wav": _record(staging / "baseline.wav"),
        "truncated.mp4": _record(staging / "single.mp4"),
    }

    _write(out / "reordered.mp4", build_reordered_mp4())
    _write(out / "drift.mp4", build_drift_mp4())
    _write(out / "params.wav", build_params_wav())
    _write(out / "truncated.mp4", build_truncated_mp4())

    entries = []
    for relative in sorted(expected):
        record = expected[relative]
        entries.append(
            {
                "path": relative,
                "container": record["container"],
                "declared_duration_ms": record["declared_duration_ms"],
                "tracks": record["tracks"],
                "record": record,
            }
        )
    manifest_payload = {
        "tool": "mediacheck",
        "tool_version": TOOL_VERSION,
        "files": entries,
    }
    manifest = out / "baseline.json"
    atomic_write_json(manifest, manifest_payload)

    for name in ("baseline.mp4", "baseline.wav", "single.mp4"):
        (staging / name).unlink()
    staging.rmdir()

    return {
        "root": str(out),
        "baseline_manifest": str(manifest),
        "files": sorted(p.name for p in out.iterdir() if p.suffix in {".wav", ".mp4"}),
    }


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description="Generate deterministic local media samples.")
    parser.add_argument("--out", required=True, help="output directory for fixtures")
    args = parser.parse_args(argv)
    info = create_samples(Path(args.out))
    print(json.dumps(info, ensure_ascii=False, indent=2))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
