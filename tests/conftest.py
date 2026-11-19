"""Shared test fixtures: generated media, in a temp directory per session.

Media is produced by ``tools/make_fixture.py`` so tests exercise the same
generator the acceptance commands use, and nothing is checked into the repo.
"""

from __future__ import annotations

import json
import sys
from pathlib import Path

import pytest

ROOT = Path(__file__).resolve().parent.parent
TOOLS = ROOT / "tools"
if str(ROOT) not in sys.path:
    sys.path.insert(0, str(ROOT))
if str(TOOLS) not in sys.path:
    sys.path.insert(0, str(TOOLS))

import make_fixture  # noqa: E402  (path is set up above)


def _generate(path: Path, *extra: str) -> Path:
    argv = ["--out", str(path), *extra]
    code = make_fixture.main(argv)
    assert code == 0, f"fixture generation failed for {path}"
    assert path.exists() and path.stat().st_size > 0
    return path


@pytest.fixture(scope="session")
def media_dir(tmp_path_factory: pytest.TempPathFactory) -> Path:
    return tmp_path_factory.mktemp("media")


@pytest.fixture(scope="session")
def sample_mp4(media_dir: Path) -> Path:
    return _generate(
        media_dir / "sample.mp4",
        "--frames", "96",
        "--gop", "24",
        "--b-frames", "2",
        "--inject-missing-pts", "3",
    )


@pytest.fixture(scope="session")
def clean_mp4(media_dir: Path) -> Path:
    return _generate(
        media_dir / "clean.mp4",
        "--frames", "48",
        "--gop", "24",
        "--b-frames", "2",
    )


@pytest.fixture(scope="session")
def clean_ts(media_dir: Path) -> Path:
    return _generate(
        media_dir / "clean.ts",
        "--frames", "48",
        "--gop", "24",
        "--b-frames", "2",
    )


@pytest.fixture(scope="session")
def missing_ts(media_dir: Path) -> Path:
    return _generate(
        media_dir / "missing.ts",
        "--frames", "96",
        "--gop", "24",
        "--b-frames", "2",
        "--inject-missing-pts", "3",
    )


@pytest.fixture(scope="session")
def regression_mp4(media_dir: Path) -> Path:
    return _generate(
        media_dir / "regression.mp4",
        "--frames", "96",
        "--gop", "24",
        "--b-frames", "2",
        "--inject-regression", "3",
    )


@pytest.fixture(scope="session")
def corrupt_mp4(media_dir: Path) -> Path:
    path = media_dir / "corrupt.mp4"
    return _generate(path, "--frames", "96", "--inject-corrupt")


def zero_sample_tables(source: Path, destination: Path) -> Path:
    """Copy an MP4 and clear its sample tables so it holds no frames.

    The result still opens as a valid container with a video stream, which is
    the "zero frames" case the audit must report rather than silently pass.
    """
    import struct

    data = bytearray(source.read_bytes())

    def walk(start: int, end: int):
        offset = start
        while offset + 8 <= end:
            size = struct.unpack(">I", data[offset:offset + 4])[0]
            kind = data[offset + 4:offset + 8].decode("latin1")
            header = 8
            if size == 1:
                size = struct.unpack(">Q", data[offset + 8:offset + 16])[0]
                header = 16
            elif size == 0:
                size = end - offset
            yield kind, offset, size, header
            if kind in ("moov", "trak", "mdia", "minf", "stbl"):
                yield from walk(offset + header, offset + size)
            offset += size

    for kind, offset, _, header in walk(0, len(data)):
        if kind in ("stts", "ctts", "stco", "stsc", "stsz"):
            struct.pack_into(">I", data, offset + header + 4, 0)
    destination.write_bytes(bytes(data))
    return destination


@pytest.fixture(scope="session")
def zero_frame_mp4(media_dir: Path, clean_mp4: Path) -> Path:
    return zero_sample_tables(clean_mp4, media_dir / "zero-frames.mp4")


@pytest.fixture(scope="session")
def audio_only(tmp_path_factory: pytest.TempPathFactory) -> Path:
    """An MP4 with an audio stream and no video stream at all."""
    import av

    path = tmp_path_factory.mktemp("audio") / "audio.m4a"
    with av.open(str(path), "w", format="mp4") as container:
        stream = container.add_stream("aac", rate=44_100)
        stream.layout = "mono"
        stream.format = "fltp"
        for index in range(4):
            frame = av.AudioFrame(format="fltp", layout="mono", samples=1024)
            frame.sample_rate = 44_100
            frame.pts = index * 1024
            for packet in stream.encode(frame):
                container.mux(packet)
        for packet in stream.encode(None):
            container.mux(packet)
    return path


@pytest.fixture
def batch_dir(tmp_path: Path, sample_mp4: Path, clean_ts: Path) -> Path:
    """A directory with two videos plus non-video files that must be skipped."""
    root = tmp_path / "batch"
    (root / "nested").mkdir(parents=True)
    (root / "a.mp4").write_bytes(sample_mp4.read_bytes())
    (root / "nested" / "b.ts").write_bytes(clean_ts.read_bytes())
    (root / "notes.txt").write_text("session notes\n", encoding="utf-8")
    (root / "timing.json").write_text(json.dumps({"fps": 24}), encoding="utf-8")
    (root / "poster.png").write_bytes(b"\x89PNG\r\n\x1a\n")
    return root
