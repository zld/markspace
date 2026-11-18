"""Manifest validation: every rejection names the field and track."""

from __future__ import annotations

import json
from pathlib import Path

import pytest

from mixforge.errors import ManifestError
from mixforge.manifest import load_manifest

from conftest import write_manifest


def _load(tmp_path: Path, payload: dict):
    path = tmp_path / "session.json"
    path.write_text(json.dumps(payload), encoding="utf-8")
    return load_manifest(path)


def _one(track: dict, **extra) -> dict:
    return {"tracks": [{"id": "a", "path": "a.wav", **track}], **extra}


def test_minimal_manifest_defaults(tmp_path: Path) -> None:
    session = _load(tmp_path, _one({}))
    track = session.tracks[0]
    assert track.id == "a"
    assert track.offset_ms == 0.0
    assert track.gain_db == 0.0
    assert track.bus == "master"
    assert track.channel_count is None
    assert track.channel_matrix is None
    assert not track.explicit_layout
    assert track.automation is None
    assert session.rate is None


@pytest.mark.parametrize("field", ["path"])
def test_missing_required_field_reports_track_and_field(tmp_path: Path, field: str) -> None:
    with pytest.raises(ManifestError) as excinfo:
        _load(tmp_path, {"tracks": [{"id": "vo"}]})
    error = excinfo.value
    assert field in str(error)
    assert error.track_id == "vo"
    assert error.exit_code == 2


def test_duplicate_track_ids_rejected(tmp_path: Path) -> None:
    with pytest.raises(ManifestError, match="duplicate track id"):
        _load(tmp_path, {"tracks": [{"id": "a", "path": "a.wav"},
                                    {"id": "a", "path": "b.wav"}]})


@pytest.mark.parametrize("value", [float("nan"), float("inf"), float("-inf")])
def test_non_finite_numbers_rejected(tmp_path: Path, value: float) -> None:
    path = write_manifest(tmp_path / "s.json", [])
    payload = _one({"offset_ms": value})
    path.write_text(json.dumps(payload), encoding="utf-8")
    with pytest.raises(ManifestError, match="finite") as excinfo:
        load_manifest(path)
    assert excinfo.value.field.endswith("offset_ms")


def test_bool_is_not_a_number(tmp_path: Path) -> None:
    with pytest.raises(ManifestError, match="must be a number") as excinfo:
        _load(tmp_path, _one({"gain_db": True}))
    assert excinfo.value.track_id == "a"


@pytest.mark.parametrize("bad", [0, -1, 2.5, "48000", True])
def test_invalid_output_rate_rejected(tmp_path: Path, bad) -> None:
    with pytest.raises(ManifestError):
        _load(tmp_path, _one({}, output_rate=bad))


def test_unknown_track_field_rejected(tmp_path: Path) -> None:
    with pytest.raises(ManifestError, match="unknown field"):
        _load(tmp_path, _one({"gain": 3.0}))


def test_unknown_bus_rejected(tmp_path: Path) -> None:
    with pytest.raises(ManifestError, match="unknown bus") as excinfo:
        _load(tmp_path, _one({"bus": "missing"}))
    assert excinfo.value.track_id == "a"


def test_paths_relative_to_manifest(tmp_path: Path) -> None:
    session = _load(tmp_path, _one({"path": "media/a.wav"}))
    assert session.tracks[0].path == (tmp_path / "media/a.wav").resolve()


def test_path_escape_rejected(tmp_path: Path) -> None:
    with pytest.raises(ManifestError, match="escapes") as excinfo:
        _load(tmp_path, _one({"path": "../outside.wav"}))
    assert excinfo.value.track_id == "a"


def test_bus_definition_valid(tmp_path: Path) -> None:
    session = _load(tmp_path, _one({"bus": "dialogue"}, buses={
        "dialogue": {"gain_db": -3.0}}))
    assert [bus.id for bus in session.buses] == ["dialogue"]


def test_master_is_reserved(tmp_path: Path) -> None:
    with pytest.raises(ManifestError, match="reserved"):
        _load(tmp_path, {"tracks": [{"id": "a", "path": "a.wav"}],
                         "buses": {"master": {}}})


@pytest.mark.parametrize("channels,expected", [
    ("mono", ((1.0,), (1.0,))),
    ("stereo", ((1.0, 0.0), (0.0, 1.0))),
    ([[0.8, 0.2], [0.1, 0.9]], ((0.8, 0.2), (0.1, 0.9))),
])
def test_channel_layouts(tmp_path: Path, channels, expected) -> None:
    session = _load(tmp_path, _one({"channels": channels}))
    assert session.tracks[0].channel_matrix == expected
    assert session.tracks[0].channel_count == len(expected[0])


@pytest.mark.parametrize("bad,message", [
    ([[1.0, 0.0], [0.0]], "equal length"),
    ([[1.0, 0.0], [0.0, 1.0], [0.5, 0.5]], "2 output rows"),
    ([[1.0, "x"], [0.0, 1.0]], "must be a number"),
    ("quad", "mono"),
    ([], "non-empty"),
])
def test_channel_matrix_rejections(tmp_path: Path, bad, message: str) -> None:
    with pytest.raises(ManifestError, match=message):
        _load(tmp_path, _one({"channels": bad}))


def test_invalid_json_reports_line(tmp_path: Path) -> None:
    path = tmp_path / "s.json"
    path.write_text('{"tracks": [}', encoding="utf-8")
    with pytest.raises(ManifestError, match="line"):
        load_manifest(path)
