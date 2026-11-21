#!/usr/bin/env python3
"""Generate the reproducible sample fixture and run an end-to-end check.

Creates examples/ with two 2x2 PNGs (opaque blue base, red overlay) and a
timeline JSON, then runs the CLI and verifies the digest against an
independently computed expectation: every pixel is [128, 0, 127, 255].

Usage: .venv/bin/python scripts/make_fixture.py
"""

from __future__ import annotations

import hashlib
import json
import subprocess
import sys
from pathlib import Path

from PIL import Image

ROOT = Path(__file__).resolve().parent.parent
EXAMPLES = ROOT / "examples"
W = H = 2


def build_fixture() -> Path:
    EXAMPLES.mkdir(exist_ok=True)
    Image.new("RGBA", (W, H), (0, 0, 255, 255)).save(EXAMPLES / "blue.png")
    Image.new("RGBA", (W, H), (255, 0, 0, 255)).save(EXAMPLES / "red.png")
    timeline = {
        "frame_rate": {"num": 2, "den": 1},
        "duration": {"num": 1, "den": 1},  # 1s at 2 fps -> frames [0, 2)
        "layers": [
            {"name": "base", "png": "blue.png", "start_frame": 0,
             "end_frame": 2, "blend": "normal", "z": 0},
            {"name": "overlay", "png": "red.png", "start_frame": 0,
             "end_frame": 2, "opacity": 0.5, "blend": "normal", "z": 1},
        ],
        "audio": {
            "sample_rate": 48000,
            "events": [
                {"name": "cue_start", "sample": 0},
                {"name": "cue_mid", "sample": 24000},  # frame 1 boundary
            ],
        },
    }
    path = EXAMPLES / "timeline.json"
    path.write_text(json.dumps(timeline, indent=2), encoding="utf-8")
    return path


def verify(timeline_path: Path) -> None:
    proc = subprocess.run(
        [sys.executable, "-m", "timeline_render.cli", "render",
         "--input", str(timeline_path), "--width", str(W), "--height", str(H),
         "--json"],
        capture_output=True, text=True, cwd=ROOT,
    )
    if proc.returncode != 0:
        raise SystemExit(f"CLI failed (exit {proc.returncode}): {proc.stderr}")
    summary = json.loads(proc.stdout)
    expected = bytes([128, 0, 127, 255] * (W * H))
    digest = hashlib.sha256(expected).hexdigest()
    assert summary["frame_count"] == 2, summary
    assert len(summary["frames"]) == 2, summary
    for i, entry in enumerate(summary["frames"]):
        assert entry["frame"] == i, entry
        assert entry["pixel_sha256"] == digest, (
            f"frame {i}: {entry['pixel_sha256']} != expected {digest}")
        assert entry["audio_alignment_error_samples"] == 0, entry
    assert (summary["frames"][0]["pixel_sha256"]
            == summary["frames"][1]["pixel_sha256"])
    print("fixture OK: frame_count=2, identical digests, alignment error 0")
    print(f"sha256: {digest}")


def main() -> None:
    timeline_path = build_fixture()
    print(f"wrote {timeline_path.relative_to(ROOT)} and layer PNGs")
    verify(timeline_path)


if __name__ == "__main__":
    main()
