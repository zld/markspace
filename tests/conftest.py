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
