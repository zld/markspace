"""Error-chain, storage and package validation tests."""
from __future__ import annotations

import json
import struct
import sys
from pathlib import Path

import pytest

ROOT = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(ROOT / "src"))
sys.path.insert(0, str(ROOT / "scripts"))

from make_samples import BASE_TRACKS, build_container, write_samples  # noqa: E402
from trackcheck import cli  # noqa: E402
from trackcheck.package import PackageError, package_media  # noqa: E402
from trackcheck.probe import (  # noqa: E402
    HEADER_FORMAT,
    HEADER_SIZE,
    MAX_TRACKS,
    SUPPORTED_VERSION,
    MAGIC,
    TRACK_ENTRY_SIZE,
    FileAccessError,
    parse_container,
    probe_file,
)


@pytest.fixture()
def samples(tmp_path: Path) -> dict[str, Path]:
    return write_samples(tmp_path / "samples", seed=17)


def test_truncated_header_reports_offset() -> None:
    media = parse_container(b"PWM", "<memory>")
    assert not media.valid
    err = media.errors[0]
    assert err.code == "E_TRUNCATED_HEADER" and err.offset == 0


def test_unknown_version_reports_offset(samples: dict[str, Path]) -> None:
    media = probe_file(samples["badversion.pwm"])
    assert [e.code for e in media.errors] == ["E_VERSION"]
    assert media.errors[0].offset == 4


def test_declared_length_mismatch(samples: dict[str, Path]) -> None:
    media = probe_file(samples["badlength.pwm"])
    err = media.errors[0]
    assert err.code == "E_DECLARED_LENGTH" and err.offset == 11


def test_truncated_body_detected(samples: dict[str, Path]) -> None:
    media = probe_file(samples["truncated.pwm"])
    assert any(e.code == "E_DECLARED_LENGTH" for e in media.errors)


def test_overrun_field_diagnostic(samples: dict[str, Path]) -> None:
    media = probe_file(samples["overrun.pwm"])
    err = next(e for e in media.errors if e.code == "E_ENTRY_OVERRUN")
    assert err.track_id == 1 and err.offset == HEADER_SIZE


def test_bad_magic_rejected(tmp_path: Path) -> None:
    target = tmp_path / "bad.pwm"
    good = build_container(BASE_TRACKS, 1)
    target.write_bytes(b"XXXX" + good[4:])
    media = probe_file(target)
    assert media.errors[0].code == "E_MAGIC"


def test_track_count_limit(tmp_path: Path) -> None:
    header = struct.pack(
        HEADER_FORMAT, MAGIC, SUPPORTED_VERSION, HEADER_SIZE,
        MAX_TRACKS + 1, HEADER_SIZE + (MAX_TRACKS + 1) * TRACK_ENTRY_SIZE,
    )
    target = tmp_path / "many.pwm"
    target.write_bytes(header)
    media = probe_file(target)
    assert media.errors[0].code == "E_TRACK_COUNT_LIMIT"
    assert media.errors[0].offset == 7


def test_unreadable_path_raises(tmp_path: Path) -> None:
    with pytest.raises(FileAccessError):
        probe_file(tmp_path / "missing.pwm")


def test_unknown_kind_and_codec_are_flagged(tmp_path: Path) -> None:
    weird = [(9, 99, "WEIR", 1000, 0, 100, 0, 0, 0, 0, "eng", 0)]
    target = tmp_path / "weird.pwm"
    target.write_bytes(build_container(weird, 5))
    media = probe_file(target)
    track = media.track_by_id(9)
    assert track is not None and track.unrecognized
    assert track.kind_name == "unknown"
    assert track.raw_fields["kind"] == 99


def test_zero_timebase_is_an_error(tmp_path: Path) -> None:
    broken = [(1, 1, "H264", 0, 0, 10, 640, 480, 0, 0, "eng", 0)]
    target = tmp_path / "zero.pwm"
    target.write_bytes(build_container(broken, 6))
    media = probe_file(target)
    assert any(e.code == "E_ZERO_TIMEBASE" for e in media.errors)


def test_package_rejects_missing_required_track(samples: dict[str, Path],
                                                tmp_path: Path) -> None:
    media = probe_file(samples["missing.pwm"])  # video + audio, no subtitle
    out = tmp_path / "manifest.json"
    # default requirement (video, audio) still met; force all three kinds
    from trackcheck.model import KIND_VIDEO, KIND_AUDIO, KIND_SUBTITLE
    with pytest.raises(PackageError) as exc:
        package_media(media, out, (KIND_VIDEO, KIND_AUDIO, KIND_SUBTITLE))
    codes = {e.code for e in exc.value.errors}
    assert "E_MISSING_REQUIRED_TRACK" in codes
    assert not out.exists()


def test_package_rejects_corrupt_container_without_output(
        samples: dict[str, Path], tmp_path: Path) -> None:
    media = probe_file(samples["overrun.pwm"])
    out = tmp_path / "nested" / "manifest.json"
    with pytest.raises(PackageError):
        package_media(media, out)
    assert not out.exists()


def test_package_success_writes_manifest(samples: dict[str, Path],
                                         tmp_path: Path) -> None:
    media = probe_file(samples["base.pwm"])
    out = tmp_path / "manifest.json"
    manifest = package_media(media, out)
    assert manifest.track_count == 3 and out.exists()
    payload = json.loads(out.read_text())
    assert payload["track_count"] == 3
