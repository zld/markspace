#!/usr/bin/env python3
"""Generate deterministic offline media fixtures for mediacheck demos.

The sample pack contains, under --out:
  scene-a.mp4   normal MP4: AVC video + AAC audio, 1000 ms
  scene-a.mkv   Matroska with the audio track missing and an extra
                subtitle track, 1024 ms (24 ms longer)
  scene-b.mp4   normal single-video MP4, 500 ms
  scene-b.mkv   truncated Matroska whose declared elements run past EOF

Everything is synthesized locally with the standard library; no external
media files or network resources are referenced.
"""

from __future__ import annotations

import argparse
import os
import sys

sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)),
                                "..", "src"))

from mediacheck.sampling.builders import build_matroska, build_mp4  # noqa: E402


def _write(path: str, data: bytes) -> None:
    with open(path, "wb") as f:
        f.write(data)


def build_pack(out_dir: str) -> list[str]:
    os.makedirs(out_dir, exist_ok=True)
    written: list[str] = []

    # --- scene-a: MP4 reference with video + audio ----------------------
    scene_a_mp4 = build_mp4(
        timescale=1000, duration=1000,
        tracks=[
            {"type": "video", "codec": "avc1",
             "width": 1920, "height": 1080},
            {"type": "audio", "codec": "mp4a",
             "sample_rate": 48000, "channels": 2},
        ])
    path = os.path.join(out_dir, "scene-a.mp4")
    _write(path, scene_a_mp4)
    written.append(path)

    # --- scene-a: MKV with missing audio and an extra subtitle ----------
    scene_a_mkv = build_matroska(
        timescale_ns=1_000_000, duration_ticks=1024.0,
        tracks=[
            {"type": "video", "codec_id": "V_MPEG4/ISO/AVC",
             "width": 1920, "height": 1080},
            {"type": "subtitle", "codec_id": "S_TEXT/UTF8"},
        ])
    path = os.path.join(out_dir, "scene-a.mkv")
    _write(path, scene_a_mkv)
    written.append(path)

    # --- scene-b: normal single-video MP4 -------------------------------
    scene_b_mp4 = build_mp4(
        timescale=1000, duration=500,
        tracks=[
            {"type": "video", "codec": "avc1",
             "width": 1280, "height": 720},
        ])
    path = os.path.join(out_dir, "scene-b.mp4")
    _write(path, scene_b_mp4)
    written.append(path)

    # --- scene-b: truncated Matroska, header intact, body cut -----------
    full_mkv = build_matroska(
        timescale_ns=1_000_000, duration_ticks=500.0,
        tracks=[
            {"type": "video", "codec_id": "V_VP9",
             "width": 1280, "height": 720},
        ])
    cut = max(32, len(full_mkv) // 2)
    path = os.path.join(out_dir, "scene-b.mkv")
    _write(path, full_mkv[:cut])
    written.append(path)
    return written


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description="Build mediacheck samples")
    parser.add_argument("--out", required=True,
                        help="output directory (created if missing)")
    args = parser.parse_args(argv)
    written = build_pack(args.out)
    print(f"wrote {len(written)} sample files to {args.out}")
    for path in written:
        print(f"  {path} ({os.path.getsize(path)} bytes)")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
