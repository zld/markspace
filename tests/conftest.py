import struct
import sys
from pathlib import Path

import pytest

ROOT = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(ROOT / "scripts"))

import make_samples  # noqa: E402


@pytest.fixture(scope="session")
def sample_dir(tmp_path_factory) -> Path:
    out = tmp_path_factory.mktemp("samples")
    make_samples.build_default_samples(out)
    return out


@pytest.fixture(scope="session")
def edge_dir(tmp_path_factory) -> Path:
    out = tmp_path_factory.mktemp("edge")
    make_samples.build_default_samples(out)
    make_samples.build_edge_samples(out)
    return out


@pytest.fixture
def valid_mp4(tmp_path) -> Path:
    path = tmp_path / "valid.mp4"
    make_samples.build_mp4(path, movie_timescale=1000, tracks=[
        {"kind": "video", "timescale": 30000, "duration": 60000,
         "track_id": 1, "width": 1920, "height": 1080},
        {"kind": "audio", "timescale": 48000, "duration": 96000,
         "track_id": 2, "sample_rate": 48000, "channels": 2},
    ])
    return path


@pytest.fixture
def valid_wav(tmp_path) -> Path:
    path = tmp_path / "valid.wav"
    make_samples.build_wav(path, sample_rate=48000, channels=1, bits=16,
                           seconds=1.0)
    return path


def corrupt_box_size(data: bytes, fourcc: bytes, delta: int) -> bytes:
    """Return data with the named box's size field adjusted by delta."""
    idx = data.find(fourcc)
    assert idx >= 4
    size = struct.unpack_from(">I", data, idx - 4)[0]
    out = bytearray(data)
    struct.pack_into(">I", out, idx - 4, size + delta)
    return bytes(out)
