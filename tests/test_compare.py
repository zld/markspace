import json

import pytest

from mediacheck.compare import (Comparator, DurationPolicy, Policy,
                                PolicyError, TrackPair, load_policy,
                                parse_policy)


def track(track_id, kind, codec, duration, *, start=0.0, index=0, **extra):
    data = {
        "index": index,
        "track_id": track_id,
        "kind": kind,
        "codec": codec,
        "duration_seconds": duration,
        "start_seconds": start,
    }
    if kind == "video":
        data.setdefault("width", 1920)
        data.setdefault("height", 1080)
    if kind == "audio":
        data.setdefault("sample_rate", 48000)
        data.setdefault("channels", 2)
    data.update(extra)
    return data


def media(name, tracks, duration=None):
    return {
        "name": name,
        "relative_path": name,
        "container": "mp4",
        "track_count": len(tracks),
        "duration_seconds": duration if duration is not None
        else max((t["duration_seconds"] or 0) for t in tracks),
        "tracks": tracks,
    }


def manifest(*items):
    return {"manifest_version": 1, "label": "pkg", "media": list(items)}


def compare_policy(ref, tgt, policy=None):
    return Comparator(policy or Policy()).compare(ref, tgt)


def test_identical_package_is_ok():
    ref = manifest(media("a.mp4", [track(1, "video", "h264", 2.0),
                                   track(2, "audio", "aac", 2.0)]))
    result = compare_policy(ref, ref)
    assert result["ok"] is True
    assert result["inconsistency_count"] == 0
    assert result["duration_delta_seconds"] == 0.0
    assert len(result["pairs"]) == 2


def test_missing_media_and_extra_media_are_reported():
    ref = manifest(media("a.mp4", [track(1, "video", "h264", 2.0)]))
    tgt = manifest(media("b.mp4", [track(1, "video", "h264", 2.0)]))
    result = compare_policy(ref, tgt)
    codes = {i["code"] for i in result["issues"]}
    assert "missing_target_media" in codes
    assert "unexpected_target_media" in codes
    assert result["ok"] is False


def test_missing_track_reported():
    ref = manifest(media("a.mp4", [track(1, "video", "h264", 2.0),
                                   track(2, "audio", "aac", 2.0)]))
    tgt = manifest(media("a.mp4", [track(1, "video", "h264", 2.0)]))
    result = compare_policy(ref, tgt)
    codes = {i["code"] for i in result["issues"]}
    assert "missing_target_track" in codes


def test_track_order_change_still_matches():
    ref = manifest(media("a.mp4", [track(1, "video", "h264", 2.0, index=0),
                                   track(2, "audio", "aac", 2.0, index=1)]))
    tgt = manifest(media("a.mp4", [track(2, "audio", "aac", 2.0, index=0),
                                   track(1, "video", "h264", 2.0, index=1)]))
    result = compare_policy(ref, tgt)
    assert result["ok"] is True
    pairs = {(p["reference_track_id"], p["target_track_id"])
             for p in result["pairs"]}
    assert pairs == {(1, 1), (2, 2)}


def test_duplicate_kind_is_ambiguous_without_mapping():
    ref = manifest(media("a.mp4", [track(1, "audio", "aac", 2.0, index=0),
                                   track(2, "audio", "aac", 2.0, index=1)]))
    tgt = manifest(media("a.mp4", [track(1, "audio", "aac", 2.0, index=0),
                                   track(2, "audio", "aac", 2.0, index=1)]))
    result = compare_policy(ref, tgt)
    assert result["ok"] is False
    assert any(i["code"] == "ambiguous_tracks" for i in result["issues"])
    assert result["pairs"] == []


def test_explicit_mapping_resolves_duplicates():
    ref = manifest(media("a.mp4", [track(1, "audio", "aac", 2.0, index=0),
                                   track(2, "audio", "aac", 2.0, index=1)]))
    tgt = manifest(media("a.mp4", [track(7, "audio", "aac", 2.0, index=0),
                                   track(8, "audio", "aac", 2.0, index=1)]))
    policy = Policy(matching=Policy().matching)
    policy.matching.pairs = [TrackPair(1, 8), TrackPair(2, 7)]
    result = compare_policy(ref, tgt, policy)
    assert result["ok"] is True
    pairs = {(p["reference_track_id"], p["target_track_id"])
             for p in result["pairs"]}
    assert pairs == {(1, 8), (2, 7)}


def test_explicit_pair_outranks_ignore_kinds():
    ref = manifest(media("a.mp4", [track(1, "timecode", "tmcd", 2.0)]))
    tgt = manifest(media("a.mp4", [track(5, "timecode", "tmcd", 2.0)]))
    policy = Policy()
    policy.matching.ignore_kinds = ["timecode"]
    policy.matching.pairs = [TrackPair(1, 5)]
    result = compare_policy(ref, tgt, policy)
    assert result["ok"] is True
    assert [(p["reference_track_id"], p["target_track_id"])
            for p in result["pairs"]] == [(1, 5)]


def test_ignored_kind_is_not_reported():
    ref = manifest(media("a.mp4", [track(1, "video", "h264", 2.0),
                                   track(2, "timecode", "tmcd", 2.0)]))
    tgt = manifest(media("a.mp4", [track(1, "video", "h264", 2.0)]))
    policy = Policy()
    policy.matching.ignore_kinds = ["timecode"]
    result = compare_policy(ref, tgt, policy)
    assert result["ok"] is True


def test_match_by_index_pairs_duplicates():
    ref = manifest(media("a.mp4", [track(1, "audio", "aac", 2.0, index=0),
                                   track(2, "audio", "aac", 2.0, index=1)]))
    tgt = manifest(media("a.mp4", [track(1, "audio", "aac", 2.0, index=0),
                                   track(2, "audio", "aac", 2.0, index=1)]))
    policy = Policy()
    policy.matching.match_by_index = True
    result = compare_policy(ref, tgt, policy)
    assert result["ok"] is True
    assert len(result["pairs"]) == 2


def test_explicit_mapping_to_missing_target_is_hard():
    ref = manifest(media("a.mp4", [track(1, "audio", "aac", 2.0)]))
    tgt = manifest(media("a.mp4", [track(9, "audio", "aac", 2.0)]))
    policy = Policy()
    policy.matching.pairs = [TrackPair(1, 5)]
    result = compare_policy(ref, tgt, policy)
    assert any(i["code"] == "explicit_pair_missing" for i in result["issues"])


def test_extra_target_tracks_only_reported():
    ref = manifest(media("a.mp4", [track(1, "video", "h264", 2.0)]))
    tgt = manifest(media("a.mp4", [track(1, "video", "h264", 2.0),
                                   track(2, "audio", "aac", 2.0)]))
    result = compare_policy(ref, tgt)
    assert any(i["code"] == "unexpected_target_track"
               for i in result["issues"])


def test_duration_within_tolerance_is_ok():
    ref = manifest(media("a.mp4", [track(1, "video", "h264", 2.0)]))
    tgt = manifest(media("a.mp4", [track(1, "video", "h264", 2.019)]))
    policy = Policy(duration=DurationPolicy(tolerance_seconds=0.02))
    result = compare_policy(ref, tgt, policy)
    assert result["ok"] is True
    pair = result["pairs"][0]
    assert pair["duration_delta_seconds"] == pytest.approx(0.019, abs=1e-6)
    assert pair["duration_delta_ratio"] == pytest.approx(0.0095, abs=1e-4)
    assert pair["duration_within_tolerance"] is True


def test_duration_beyond_tolerance_is_hard():
    ref = manifest(media("a.mp4", [track(1, "video", "h264", 2.0)]))
    tgt = manifest(media("a.mp4", [track(1, "video", "h264", 2.5)]))
    policy = Policy(duration=DurationPolicy(tolerance_seconds=0.02))
    result = compare_policy(ref, tgt, policy)
    assert result["ok"] is False
    assert result["duration_delta_seconds"] == pytest.approx(0.5)
    assert result["duration_delta_ratio"] == pytest.approx(0.25)


def test_ratio_tolerance_can_permit_delta():
    ref = manifest(media("a.mp4", [track(1, "video", "h264", 100.0)]))
    tgt = manifest(media("a.mp4", [track(1, "video", "h264", 100.5)]))
    policy = Policy(duration=DurationPolicy(tolerance_seconds=0.02,
                                            tolerance_ratio=0.01))
    result = compare_policy(ref, tgt, policy)
    assert result["ok"] is True


def test_rounding_noise_does_not_trigger():
    # Both sides are stored at microsecond precision; the 0.1s delta here
    # would be a false positive if rounding were done badly.
    ref = manifest(media("a.mp4", [track(1, "video", "h264", 3.0000004)]))
    tgt = manifest(media("a.mp4", [track(1, "video", "h264", 3.0000005)]))
    result = compare_policy(ref, tgt)
    assert result["ok"] is True


def test_zero_duration_handled_without_division_error():
    ref = manifest(media("a.mp4", [track(1, "video", "h264", 0.0)],
                         duration=0.0))
    tgt = manifest(media("a.mp4", [track(1, "video", "h264", 1.0)],
                         duration=1.0))
    result = compare_policy(ref, tgt)
    assert result["ok"] is False
    pair = result["pairs"][0]
    assert pair["duration_delta_seconds"] == pytest.approx(1.0)
    assert pair["duration_delta_ratio"] is None


def test_nonzero_start_time_severity():
    ref = manifest(media("a.mp4", [track(1, "audio", "aac", 2.0, start=0.0)]))
    tgt = manifest(media("a.mp4", [track(1, "audio", "aac", 2.0, start=0.5)]))
    policy = Policy(duration=DurationPolicy(start_severity="warning"))
    result = compare_policy(ref, tgt, policy)
    assert result["ok"] is True
    assert any(i["code"] == "start_time_mismatch"
               and i["severity"] == "warning" for i in result["issues"])


def test_codec_mismatch_is_hard_and_warn_field_is_warning():
    ref = manifest(media("a.mp4", [track(1, "audio", "aac", 2.0)]))
    tgt = manifest(media("a.mp4", [track(1, "audio", "aac", 2.0,
                                        channels=1)]))
    policy = Policy()
    policy.codec.warn_fields = {"channels"}
    result = compare_policy(ref, tgt, policy)
    assert result["ok"] is True
    assert any(i["code"] == "codec_mismatch" and i["severity"] == "warning"
               for i in result["issues"])

    policy2 = Policy()
    result2 = compare_policy(ref, tgt, policy2)
    assert result2["ok"] is False


def test_codec_whitelist_permits_alternative():
    ref = manifest(media("a.mp4", [track(1, "video", "h264", 2.0)]))
    tgt = manifest(media("a.mp4", [track(1, "video", "hevc", 2.0)]))
    policy = Policy()
    policy.codec.whitelist = {"video": [{"codec": "hevc"}]}
    result = compare_policy(ref, tgt, policy)
    assert result["ok"] is True
    assert any(i["code"] == "codec_whitelisted" for i in result["issues"])


def test_missing_field_severity_configurable():
    ref = manifest(media("a.mp4", [track(1, "audio", "aac", 2.0,
                                        sample_rate=None)]))
    tgt = manifest(media("a.mp4", [track(1, "audio", "aac", 2.0)]))
    policy = Policy()
    policy.codec.missing_severity = "warning"
    result = compare_policy(ref, tgt, policy)
    assert any(i["code"] == "codec_missing_field"
               and i["severity"] == "warning" for i in result["issues"])


def test_policy_parse_full_document():
    policy = parse_policy({
        "name": "delivery",
        "duration": {"tolerance_seconds": 0.05, "tolerance_ratio": 0.001,
                     "start_severity": "warning"},
        "matching": {
            "pairs": [{"reference_track_id": 1, "target_track_id": 10}],
            "require_kinds": ["audio"],
            "ignore_kinds": ["timecode"],
            "match_by_index": True,
        },
        "codec": {
            "whitelist": {"audio": [{"codec": "aac", "sample_rate": 48000}]},
            "warn_fields": ["bit_depth"],
            "ignore_fields": ["language"],
            "missing_severity": "info",
        },
    })
    assert policy.duration.tolerance_seconds == 0.05
    assert policy.matching.pairs[0] == TrackPair(1, 10)
    assert policy.codec.whitelist["audio"][0]["codec"] == "aac"


def test_policy_unknown_field_reports_path():
    with pytest.raises(PolicyError) as info:
        parse_policy({"duration": {"tolerance_secondz": 0.1}})
    assert info.value.field_path == "duration.tolerance_secondz"


def test_policy_bad_type_reports_path():
    with pytest.raises(PolicyError) as info:
        parse_policy({"duration": {"tolerance_seconds": "fast"}})
    assert info.value.field_path == "duration.tolerance_seconds"


def test_policy_bad_pair_reports_index():
    with pytest.raises(PolicyError) as info:
        parse_policy({"matching": {"pairs": [{"reference_track_id": 1}]}})
    assert info.value.field_path == "matching.pairs[0].target_track_id"


def test_policy_bad_kind_reports_index():
    with pytest.raises(PolicyError) as info:
        parse_policy({"codec": {"whitelist": {"sounds": []}}})
    assert info.value.field_path == "codec.whitelist.sounds"


def test_policy_json_syntax_error_has_line_column(tmp_path):
    path = tmp_path / "bad.json"
    path.write_text('{\n  "duration": {\n    "tolerance_seconds": ,\n  }\n}\n')
    with pytest.raises(PolicyError) as info:
        load_policy(path)
    assert info.value.line == 3
    assert info.value.column is not None


def test_policy_negative_tolerance_from_cli():
    with pytest.raises(PolicyError):
        load_policy(None, tolerance_seconds=-1)


def test_policy_dumped_and_reloaded_roundtrip(tmp_path):
    policy = Policy()
    path = tmp_path / "policy.json"
    path.write_text(json.dumps(policy.to_dict()))
    loaded = load_policy(path)
    assert loaded.to_dict() == policy.to_dict()
