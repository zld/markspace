"""Generate reproducible video fixtures with B-frames and faulty timestamps.

The fixture is produced in two deterministic stages:

1. Encode a smooth-motion H.264 elementary stream into a temporary MP4.
   Smooth content plus ``sc_threshold=0`` makes libx264 emit real B-frames,
   so decode (DTS) order and presentation (PTS) order differ.
2. Remux every video packet while rewriting selected presentation timestamps
   to inject missing, duplicate and large-jump PTS values.  DTS stays
   monotonic so the muxer remains valid.  The output uses the MPEG-TS muxer
   (the only common muxer that accepts a missing PTS); the file name may carry
   any extension because PyAV/FFmpeg probes the container by content.

A strictly backwards PTS cannot be expressed in a legal PTS>=DTS stream: the
muxer snaps such values back onto the reorder grid.  Regression detection is
therefore exercised on synthetic frame records in the test suite, while the
fixture covers the three fault classes that survive a real container round
trip.

A JSON manifest written next to the output records every injected fault so
tests can assert against known, reproducible values.
"""

from __future__ import annotations

import argparse
import json
import tempfile
from fractions import Fraction
from pathlib import Path

import av
import numpy as np

FRAME_COUNT = 120
WIDTH = 176
HEIGHT = 144
FPS = Fraction(30000, 1001)
ENCODE_TIME_BASE = Fraction(1001, 30000)
GOP_SIZE = 30
B_FRAMES = 2

# Presentation frame indices chosen to land inside and across GOP boundaries.
FAULT_MISSING_INDEX = 37
FAULT_DUPLICATE_INDEX = 58
FAULT_JUMP_INDEX = 91
# Chosen above the 2.0 s audit threshold with margin.  A 3.5 s injection
# survives the MPEG-TS clock as a ~2.57 s observed gap, comfortably over the
# default jump threshold without timestamp-wrap ambiguity.
JUMP_SECONDS = 3.5


def render_frame(index: int) -> np.ndarray:
    """Deterministic smooth-motion RGB frame (seed-independent math)."""
    image = np.zeros((HEIGHT, WIDTH, 3), dtype=np.uint8)
    offset = (index * 4) % WIDTH
    band_width = 40
    start = max(0, offset)
    end = min(WIDTH, offset + band_width)
    gradient = np.linspace(60, 230, end - start, dtype=np.uint8)
    image[:, start:end, 0] = gradient[None, :]
    rows = np.arange(HEIGHT, dtype=np.uint16)
    image[:, :, 1] = ((rows[:, None] + index * 2) % 256).astype(np.uint8)
    image[:, :, 2] = np.uint8((index * 3) % 256)
    return image


def encode_source(path: Path) -> None:
    container = av.open(str(path), "w", format="mp4")
    stream = container.add_stream(
        "libx264",
        rate=FPS,
        options={
            "preset": "slow",
            "crf": "28",
            "bf": str(B_FRAMES),
            "sc_threshold": "0",
        },
    )
    stream.width = WIDTH
    stream.height = HEIGHT
    stream.pix_fmt = "yuv420p"
    stream.time_base = ENCODE_TIME_BASE
    stream.codec_context.max_b_frames = B_FRAMES
    stream.gop_size = GOP_SIZE

    for index in range(FRAME_COUNT):
        frame = av.VideoFrame.from_ndarray(render_frame(index), format="rgb24")
        frame.pts = index
        frame.time_base = ENCODE_TIME_BASE
        for packet in stream.encode(frame):
            container.mux(packet)
    for packet in stream.encode():
        container.mux(packet)
    container.close()


def build_faults() -> dict[int, dict[str, object]]:
    """Return presentation-index -> fault specification in source units.

    Injected PTS values always stay at or above the packet DTS so the MPEG-TS
    muxer accepts them.  Three container-representable fault classes are
    injected end to end: missing, duplicate and jump.  Backwards (regression)
    timestamps cannot be represented in a legal PTS>=DTS stream because the
    muxer snaps them onto the reorder grid; regression handling is covered
    deterministically at the frame-record level in the test suite.
    """
    step = ENCODE_TIME_BASE
    return {
        FAULT_MISSING_INDEX: {
            "pts": None,
            "kind": "missing",
            "description": "PTS removed entirely",
        },
        FAULT_DUPLICATE_INDEX: {
            "pts": (FAULT_DUPLICATE_INDEX - 1) * step.numerator,
            "kind": "duplicate",
            "description": "PTS copied from the preceding frame",
        },
        # PTS jumps forward by JUMP_SECONDS on top of its normal position;
        # the result stays well above the packet DTS.  In the 1/30000 tick
        # domain one second is 30000 ticks.
        FAULT_JUMP_INDEX: {
            "pts": (FAULT_JUMP_INDEX * step.numerator)
            + int(JUMP_SECONDS * ENCODE_TIME_BASE.denominator),
            "kind": "jump",
            "description": f"PTS pushed forward by {JUMP_SECONDS} seconds",
        },
    }


def remux_with_faults(source: Path, target: Path) -> dict[str, object]:
    faults = build_faults()
    injected: list[dict[str, object]] = []

    input_container = av.open(str(source))
    source_stream = input_container.streams.video[0]
    output_container = av.open(str(target), "w", format="mpegts")
    output_container.add_stream(template=source_stream)

    # Packet PTS is the presentation frame number scaled by the encode time
    # base numerator; demux order is DTS order, so faults must be located by
    # presentation index rather than by packet arrival position.
    pts_unit = ENCODE_TIME_BASE.numerator

    for packet in input_container.demux(source_stream):
        # The final zero-size flush packet has no DTS and must not be muxed.
        if packet.dts is None:
            continue
        original_pts = packet.pts
        presentation_index = (
            original_pts // pts_unit if original_pts is not None else -1
        )
        fault = faults.get(presentation_index)
        if fault is not None:
            kind = str(fault["kind"])
            new_pts = None if fault["pts"] is None else int(fault["pts"])
            packet.pts = new_pts
            injected.append(
                {
                    "presentation_index": presentation_index,
                    "kind": kind,
                    "original_pts": int(original_pts)
                    if original_pts is not None
                    else None,
                    "injected_pts": new_pts,
                    "description": fault["description"],
                }
            )
        output_container.mux(packet)

    output_container.close()
    input_container.close()

    return {
        "frame_count": FRAME_COUNT,
        "fps": str(FPS),
        "width": WIDTH,
        "height": HEIGHT,
        "b_frames": B_FRAMES,
        "gop_size": GOP_SIZE,
        "container": "mpegts",
        "faults": injected,
    }


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output", required=True, help="output video path")
    parser.add_argument(
        "--manifest",
        default=None,
        help="manifest JSON path (defaults to <output>.manifest.json)",
    )
    args = parser.parse_args()

    target = Path(args.output)
    target.parent.mkdir(parents=True, exist_ok=True)
    manifest_path = (
        Path(args.manifest)
        if args.manifest
        else target.with_suffix(target.suffix + ".manifest.json")
    )

    with tempfile.TemporaryDirectory(prefix="frameaudit_fixture_") as tmp:
        source = Path(tmp) / "source.mp4"
        encode_source(source)
        manifest = remux_with_faults(source, target)

    manifest["output"] = str(target)
    manifest_path.write_text(json.dumps(manifest, indent=2) + "\n", encoding="utf-8")
    print(f"wrote fixture: {target} ({FRAME_COUNT} frames, B-frames={B_FRAMES})")
    print(f"wrote manifest: {manifest_path} ({len(manifest['faults'])} faults)")


if __name__ == "__main__":
    main()
