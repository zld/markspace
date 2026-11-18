"""End-to-end pipeline tests and error-chain coverage.

Samples are generated locally through scripts/make_samples.py so the suite
has no external file or network dependency.
"""
from __future__ import annotations

import json
import sys
from pathlib import Path

import pytest

ROOT = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(ROOT / "src"))
sys.path.insert(0, str(ROOT / "scripts"))

from make_samples import build_container, write_samples, BASE_TRACKS  # noqa: E402
from trackcheck import cli  # noqa: E402
from trackcheck.model import CODEC_H264, KIND_VIDEO  # noqa: E402
from trackcheck.probe import (  # noqa: E402
    HEADER_FORMAT,
    HEADER_SIZE,
    MAGIC,
    TRACK_ENTRY_SIZE,
    probe_file,
)
from trackcheck.store import SnapshotStore, StoreError  # noqa: E402
import struct  # noqa: E402


@pytest.fixture()
def samples(tmp_path: Path) -> dict[str, Path]:
    return write_samples(tmp_path / "samples", seed=17)


def test_generator_is_deterministic(tmp_path: Path) -> None:
    a = write_samples(tmp_path / "a", seed=17)
    b = write_samples(tmp_path / "b", seed=17)
    for name in a:
        assert a[name].read_bytes() == b[name].read_bytes()


def test_probe_extracts_all_track_kinds(samples: dict[str, Path]) -> None:
    media = probe_file(samples["base.pwm"])
    assert media.valid
    assert media.track_count == 3
    kinds = {t.kind_name for t in media.tracks}
    assert kinds == {"video", "audio", "subtitle"}
    video = media.track_by_id(1)
    assert video is not None and (video.width, video.height) == (1920, 1080)
    audio = media.track_by_id(2)
    assert audio is not None and audio.sample_rate == 48000
    assert audio.channels == 2 and audio.language == "eng"
    assert media.content_hash and len(media.content_hash) == 64


def test_acceptance_compare_drift(samples: dict[str, Path], tmp_path: Path,
                                  capsys: pytest.CaptureFixture[str]) -> None:
    db = tmp_path / "index.sqlite"
    report = tmp_path / "report.json"
    code = cli.main([
        "compare", "--baseline", str(samples["base.pwm"]),
        "--input", str(samples["drift.pwm"]), "--db", str(db),
        "--json", str(report), "--tolerance-ms", "40",
    ])
    out = capsys.readouterr().out
    assert "baseline_tracks=3 input_tracks=3" in out
    assert "duration_delta_ms=120" in out
    assert "mismatch_count=2" in out
    assert code == cli.EXIT_MISMATCH

    payload = json.loads(report.read_text())
    for key in ("track_count", "duration_delta_ms", "codec_summary",
                "mismatches"):
        assert key in payload
    fields = {(m["kind"], m.get("field")) for m in payload["mismatches"]}
    assert ("parameter_drift", "sample_rate") in fields
    assert ("duration_drift", "duration_ms") in fields


def test_identical_compare_is_clean(samples: dict[str, Path],
                                    capsys: pytest.CaptureFixture[str]) -> None:
    code = cli.main([
        "compare", "--baseline", str(samples["base.pwm"]),
        "--input", str(samples["base.pwm"]), "--tolerance-ms", "5",
    ])
    out = capsys.readouterr().out
    assert "mismatch_count=0" in out and "error_count=0" in out
    assert code == cli.EXIT_OK
