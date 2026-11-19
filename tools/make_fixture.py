#!/usr/bin/env python3
"""Generate local sample media for frame-audit, including timestamp anomalies.

The fixtures are synthesised with PyAV - no external assets, no ffmpeg CLI.  A
short H.264 stream is encoded with B frames so decode order and presentation
order differ, then packets are altered before muxing to reproduce what a broken
outsourced asset looks like:

``--inject-missing-pts N``
    Drop the PTS of ``N`` packets.  Depending on the muxer, a dropped PTS either
    survives as a genuinely absent timestamp (MPEG-TS) or is re-derived from the
    packet's DTS, which produces duplicate/backwards timestamps (MP4).  Both are
    anomalies the audit has to find, and the tool prints what was actually
    produced so the fixture is never assumed to contain more than it does.

``--inject-regression N``
    Collapse the PTS of ``N`` packets onto their DTS, which makes presentation
    order disagree with decode order.

``--inject-corrupt``
    Zero a slice of the encoded payload after muxing, producing packets the
    decoder rejects.

Examples
--------
    python tools/make_fixture.py --out .tmp/sample.mp4 --frames 96 \
        --gop 24 --b-frames 2 --inject-missing-pts 3

    python tools/make_fixture.py --out .tmp/corrupt.mp4 --inject-corrupt

    python tools/make_fixture.py --out .tmp/sample.ts --inject-regression 3
"""

from __future__ import annotations

import argparse
import struct
import sys
from dataclasses import dataclass
from fractions import Fraction
from pathlib import Path

import av
import numpy as np

#: Container formats keyed by the output suffix.
FORMATS: dict[str, str] = {
    ".mp4": "mp4",
    ".m4v": "mp4",
    ".mov": "mov",
    ".ts": "mpegts",
    ".m2ts": "mpegts",
    ".mkv": "matroska",
    ".nut": "nut",
}

#: Formats whose muxer stores PTS separately from DTS, so a dropped PTS turns
#: into a reused timestamp rather than an absent one.
_REUSES_DTS = {".mp4", ".m4v", ".mov", ".mkv"}


@dataclass(frozen=True, slots=True)
class Injection:
    """Which packets carry which anomaly."""

    missing: frozenset[int]
    regression: frozenset[int]


def _spread(start: int, count: int, limit: int) -> list[int]:
    """Pick ``count`` positions from ``start`` to ``limit``, evenly spaced."""
    if count <= 0:
        return []
    span = max(limit - start, count)
    step = max(1, span // (count + 1))
    return [start + step * (index + 1) for index in range(count)]


def plan_injection(
    total_packets: int, *, missing: int, regression: int
) -> Injection:
    """Distribute injected anomalies across the middle of the stream.

    Positions are spaced out so each injected packet maps to its own decoded
    frame; stacking them on consecutive packets makes the decoder synthesise a
    single value and under-reports the fixture.
    """
    low = max(1, total_packets // 8)
    high = max(low + 1, total_packets - total_packets // 8)
    missing_positions = _spread(low, missing, high)
    after = (max(missing_positions) + 2) if missing_positions else low
    regression_positions = _spread(after, regression, high)
    return Injection(
        missing=frozenset(missing_positions),
        regression=frozenset(regression_positions),
    )


def _frame_bytes(index: int, width: int, height: int) -> np.ndarray:
    """A cheap but distinct RGB frame for every index."""
    pixels = np.empty((height, width, 3), dtype=np.uint8)
    pixels[:, :, 0] = (index * 7) % 256
    pixels[:, :, 1] = (index * 3) % 256
    pixels[:, :, 2] = 128
    pixels[:, ::16, :] = 255  # hard edges keep the encoder honest
    return pixels


def encode(
    destination: Path,
    *,
    frames: int,
    gop: int,
    b_frames: int,
    fps: int,
    width: int,
    height: int,
    injection: Injection,
) -> int:
    """Encode ``frames`` pictures and return the number of packets written."""
    suffix = destination.suffix.lower()
    container_format = FORMATS.get(suffix)
    container = (
        av.open(str(destination), "w", format=container_format)
        if container_format
        else av.open(str(destination), "w")
    )
    time_base = Fraction(1, fps)
    try:
        stream = container.add_stream("libx264", rate=fps)
        stream.width = width
        stream.height = height
        stream.pix_fmt = "yuv420p"
        stream.options = {
            "preset": "ultrafast",
            "bf": str(b_frames),
            "g": str(gop),
            "sc_threshold": "0",
        }

        written = 0

        def mux_packets(packets: object) -> None:
            nonlocal written
            for packet in packets:  # type: ignore[union-attr]
                if written in injection.missing:
                    packet.pts = None
                elif written in injection.regression and packet.dts is not None:
                    packet.pts = packet.dts
                container.mux(packet)
                written += 1

        for index in range(frames):
            picture = av.VideoFrame.from_ndarray(
                _frame_bytes(index, width, height), format="rgb24"
            )
            picture.pts = index
            picture.time_base = time_base
            mux_packets(stream.encode(picture))
        mux_packets(stream.encode(None))
    finally:
        container.close()
    return written


def corrupt_file(path: Path, *, fraction: float = 0.25) -> int:
    """Zero a slice of the biggest payload box and return bytes zeroed.

    For MP4 the ``mdat`` payload is zeroed from its start, so the decoder rejects
    the leading pictures; for anything else the middle of the file is used.  The
    result is still openable as a container but contains undecodable packets.
    """
    data = bytearray(path.read_bytes())
    if not data:
        return 0
    start = 0
    marker = data.find(b"mdat")
    if marker > 4:
        box_start = marker - 4
        size = struct.unpack(">I", data[box_start:box_start + 4])[0]
        if 8 < size <= len(data) - box_start:
            start = box_start + 8
    length = max(1, int((len(data) - start) * fraction))
    end = min(len(data), start + length)
    data[start:end] = b"\x00" * (end - start)
    path.write_bytes(bytes(data))
    return end - start


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        description="Generate a local video fixture with optional timestamp damage.",
    )
    parser.add_argument("--out", required=True, help="output path (suffix picks the container)")
    parser.add_argument("--frames", type=int, default=96, help="frames to encode (default 96)")
    parser.add_argument("--gop", type=int, default=24, help="keyframe interval (default 24)")
    parser.add_argument(
        "--b-frames", type=int, default=2, help="consecutive B frames (default 2)"
    )
    parser.add_argument("--fps", type=int, default=24, help="frame rate (default 24)")
    parser.add_argument("--width", type=int, default=160, help="frame width (default 160)")
    parser.add_argument("--height", type=int, default=96, help="frame height (default 96)")
    parser.add_argument(
        "--inject-missing-pts",
        type=int,
        default=0,
        metavar="N",
        help="drop the PTS of N packets",
    )
    parser.add_argument(
        "--inject-regression",
        type=int,
        default=0,
        metavar="N",
        help="collapse the PTS of N packets onto their DTS",
    )
    parser.add_argument(
        "--inject-corrupt",
        action="store_true",
        help="zero part of the payload so packets fail to decode",
    )
    return parser


def main(argv: list[str] | None = None) -> int:
    parser = build_parser()
    args = parser.parse_args(argv)

    if args.frames <= 0:
        parser.error("--frames must be positive")
    if args.gop <= 0:
        parser.error("--gop must be positive")
    if args.b_frames < 0:
        parser.error("--b-frames must be zero or greater")
    if args.fps <= 0:
        parser.error("--fps must be positive")
    if args.width < 16 or args.height < 16:
        parser.error("--width and --height must be at least 16")
    if args.inject_missing_pts < 0 or args.inject_regression < 0:
        parser.error("injection counts must not be negative")

    destination = Path(args.out)
    destination.parent.mkdir(parents=True, exist_ok=True)

    # Worst case one packet per picture plus the encoder flush.
    plan = plan_injection(
        args.frames + 2,
        missing=args.inject_missing_pts,
        regression=args.inject_regression,
    )

    try:
        packets = encode(
            destination,
            frames=args.frames,
            gop=args.gop,
            b_frames=args.b_frames,
            fps=args.fps,
            width=args.width,
            height=args.height,
            injection=plan,
        )
    except av.FFmpegError as exc:  # type: ignore[attr-defined]
        sys.stderr.write(f"make_fixture: encoding failed: {exc}\n")
        return 1

    if args.inject_corrupt:
        corrupt_file(destination)

    suffix = destination.suffix.lower()
    reused = suffix in _REUSES_DTS
    sys.stdout.write(
        f"wrote {destination} ({packets} packets, {args.frames} frames, "
        f"gop {args.gop}, b-frames {args.b_frames})\n"
    )
    if args.inject_missing_pts:
        detail = (
            "the muxer reuses the packet DTS, so these appear as "
            "duplicate/backwards timestamps"
            if reused
            else "these stay genuinely absent in the container"
        )
        sys.stdout.write(
            f"  requested {args.inject_missing_pts} removed PTS; {detail}\n"
        )
    if args.inject_regression:
        sys.stdout.write(
            f"  requested {args.inject_regression} PTS/DTS collapses "
            "(presentation order now disagrees with decode order)\n"
        )
    if args.inject_corrupt:
        sys.stdout.write("  payload corrupted; expect decode errors\n")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
