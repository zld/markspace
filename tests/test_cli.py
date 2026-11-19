import json
from pathlib import Path

import pytest

from mediacheck import cli
from mediacheck.extract import (ExtractError, discover_media, summarize_file,
                                summarize_many)


def test_summary_has_null_reasons_for_missing_fields(sample_dir):
    summary = summarize_file(sample_dir / "level_intro_vo.wav")
    track = summary["tracks"][0]
    # A WAV has no width/height; those must be null with no false reason noise.
    assert track["width"] is None
    assert track["height"] is None
    assert track["duration_seconds"] == pytest.approx(2.0)
    assert track["start_seconds"] == 0.0
    assert summary["codec_summary"] == ["pcm_s16le"]


def test_summary_duration_uses_track_timescale(sample_dir):
    summary = summarize_file(sample_dir / "dialogue_scene2.mp4")
    video = next(t for t in summary["tracks"] if t["kind"] == "video")
    audio = next(t for t in summary["tracks"] if t["kind"] == "audio")
    # 48000 units at 24000 Hz and 88200 units at 44100 Hz are both 2s.
    assert video["duration_seconds"] == pytest.approx(2.0)
    assert audio["duration_seconds"] == pytest.approx(2.0)
    assert summary["duration_seconds"] == pytest.approx(2.0)


def test_edit_list_offsets_presentation_start(edge_dir):
    summary = summarize_file(edge_dir / "offset_start.mp4")
    track = summary["tracks"][0]
    assert track["presentation_offset_seconds"] == pytest.approx(1.0)
    assert track["start_seconds"] == pytest.approx(1.0)
    assert track["duration_seconds"] == pytest.approx(2.0)


def test_discover_is_sorted_and_recursive(tmp_path):
    (tmp_path / "z").mkdir()
    (tmp_path / "a").mkdir()
    for rel in ("z/b.wav", "a/c.mp4", "a/d.txt"):
        path = tmp_path / rel
        path.write_bytes(b"x" * 32)
    found = [p.relative_to(tmp_path).as_posix() for p in discover_media(tmp_path)]
    assert found == ["a/c.mp4", "z/b.wav"]
    flat = [p.name for p in discover_media(tmp_path, recursive=False)]
    assert flat == []


def test_discover_missing_path(tmp_path):
    with pytest.raises(ExtractError) as info:
        discover_media(tmp_path / "nope")
    assert info.value.code == "not_found"


def test_extract_error_has_location(tmp_path):
    path = tmp_path / "bad.mp4"
    path.write_bytes(b"\x00\x00\x00\x20ftyp" + b"isom" + b"\x00" * 12)
    with pytest.raises(ExtractError) as info:
        summarize_file(path)
    assert info.value.code
    assert info.value.path == str(path)


def test_summarize_av_drift_flags_mismatch(edge_dir):
    summary = summarize_file(edge_dir / "av_drift.mp4")
    durations = {t["kind"]: t["duration_seconds"] for t in summary["tracks"]}
    assert durations["video"] == pytest.approx(1.0)
    assert durations["audio"] == pytest.approx(1.4)
    assert summary["duration_seconds"] == pytest.approx(1.4)


def test_summarize_many_isolates_failures(edge_dir):
    paths = discover_media(edge_dir)
    summaries, failures = summarize_many(paths)
    names = {Path(s["path"]).name for s in summaries}
    failed = {Path(f["path"]).name for f in failures}
    assert "corrupt_truncated.mp4" in failed
    assert "corrupt_truncated.mp4" not in names
    assert len(summaries) == len(paths) - len(failures)
    assert failures[0]["offset"] is not None


def run(capsys, *argv):
    code = cli.main(list(argv))
    captured = capsys.readouterr()
    return code, captured.out, captured.err


def test_scan_success_and_exit_zero(tmp_path, capsys, valid_mp4):
    code, out, _err = run(capsys, "scan", str(tmp_path))
    assert code == cli.EXIT_OK
    assert "files_scanned: 1" in out
    assert "tracks_total:  2" in out
    assert "codec_summary:" in out
    assert "inconsistency_count:" in out


def test_scan_with_corrupt_file_exits_three(tmp_path, capsys, sample_dir):
    code, out, err = run(capsys, "scan", str(sample_dir))
    assert code == cli.EXIT_BATCH_FAILED
    assert "files_failed:  1" in out
    assert "corrupt_truncated.mp4" in err
    assert "failed files" in out


def test_scan_json_and_database(tmp_path, capsys, sample_dir):
    db = tmp_path / "m.sqlite"
    out_json = tmp_path / "scan.json"
    code, _out, _err = run(capsys, "scan", str(sample_dir), "--db", str(db),
                           "--json", str(out_json))
    assert code == cli.EXIT_BATCH_FAILED
    document = json.loads(out_json.read_text())
    assert document["files_scanned"] == 4
    assert document["tracks_total"] == 5
    assert "duration_delta_seconds" in document
    assert "duration_delta_ratio" in document
    assert document["batch"]["id"] == 1


def test_scan_av_drift_exits_one(tmp_path, capsys, edge_dir):
    import shutil
    shutil.copy(edge_dir / "av_drift.mp4", tmp_path / "av_drift.mp4")
    code, out, _err = run(capsys, "scan", str(tmp_path), "--av-tolerance", "0.1")
    assert code == cli.EXIT_INCONSISTENT
    assert "av_duration_mismatch" in out
    assert "duration_delta_seconds: 0.400" in out
    assert "duration_delta_ratio:   0.400" in out


def test_scan_av_drift_within_default_tolerance(tmp_path, capsys, edge_dir):
    import shutil
    shutil.copy(edge_dir / "av_drift.mp4", tmp_path / "av_drift.mp4")
    code, out, _err = run(capsys, "scan", str(tmp_path))
    assert code == cli.EXIT_OK
    assert "av_duration_mismatch" not in out


def test_scan_video_without_audio_warns(tmp_path, capsys, edge_dir):
    import shutil
    shutil.copy(edge_dir / "offset_start.mp4", tmp_path / "v.mp4")
    code, out, _err = run(capsys, "scan", str(tmp_path))
    assert code == cli.EXIT_OK
    assert "no_audio_track" in out


def test_scan_negative_tolerance_is_usage_error(tmp_path, capsys, valid_mp4):
    code, _out, err = run(capsys, "scan", str(valid_mp4), "--av-tolerance", "-1")
    assert code == cli.EXIT_USAGE
    assert "av-tolerance" in err


def test_scan_failures_take_precedence_over_drift(tmp_path, capsys, edge_dir):
    import shutil
    shutil.copy(edge_dir / "av_drift.mp4", tmp_path / "av_drift.mp4")
    shutil.copy(edge_dir / "corrupt_truncated.mp4", tmp_path / "bad.mp4")
    code, _out, _err = run(capsys, "scan", str(tmp_path), "--av-tolerance", "0.1")
    assert code == cli.EXIT_BATCH_FAILED


def test_scan_duplicate_manifest_paths_is_usage_error(tmp_path, capsys,
                                                     valid_mp4):
    manifest = tmp_path / "m.json"
    run(capsys, "pack", str(valid_mp4), "--out", str(manifest))
    data = json.loads(manifest.read_text())
    data["media"] = data["media"] * 2
    dup = tmp_path / "dup.json"
    dup.write_text(json.dumps(data))
    code, _out, err = run(capsys, "compare", str(dup), str(manifest))
    assert code == cli.EXIT_USAGE
    assert "more than once" in err


def test_scan_missing_target_is_usage_error(tmp_path, capsys):
    code, _out, err = run(capsys, "scan", str(tmp_path / "missing"))
    assert code == cli.EXIT_USAGE
    assert "scan:" in err


def test_scan_unwritable_db_exits_three(tmp_path, capsys, valid_mp4):
    blocker = tmp_path / "blocker"
    blocker.write_text("x")
    code, out, err = run(capsys, "scan", str(valid_mp4),
                         "--db", str(blocker / "sub" / "m.sqlite"))
    assert code == cli.EXIT_BATCH_FAILED
    assert "files_scanned: 1" in out
    assert "cannot open database" in err


def test_scan_json_write_failure_exits_three(tmp_path, capsys, valid_mp4):
    blocker = tmp_path / "blocker"
    blocker.write_text("x")
    code, _out, err = run(capsys, "scan", str(valid_mp4),
                          "--json", str(blocker / "out.json"))
    assert code == cli.EXIT_BATCH_FAILED
    assert "cannot write JSON" in err


def test_pack_and_compare_identical(tmp_path, capsys, sample_dir):
    manifest_path = tmp_path / "manifest.json"
    code, out, _err = run(capsys, "pack", str(sample_dir),
                          "--out", str(manifest_path))
    assert code == cli.EXIT_BATCH_FAILED  # sample set contains one bad file
    assert manifest_path.exists()
    manifest = json.loads(manifest_path.read_text())
    assert manifest["manifest_version"] == 1
    assert manifest["files_ok"] == 3
    assert manifest["tracks_total"] == 5

    out_json = tmp_path / "compare.json"
    code, out, _err = run(capsys, "compare", str(manifest_path),
                          str(manifest_path), "--duration-tolerance", "0.02",
                          "--json", str(out_json))
    assert code == cli.EXIT_OK
    assert "inconsistency_count: 0" in out
    assert "duration_delta_seconds" in out
    assert "codec_summary" in out
    result = json.loads(out_json.read_text())
    assert result["ok"] is True
    assert result["policy_version"] == 1


def test_pack_clean_tree_exits_zero(tmp_path, capsys, valid_mp4, valid_wav):
    out = tmp_path / "m.json"
    code, _out, _err = run(capsys, "pack", str(tmp_path), "--out", str(out))
    assert code == cli.EXIT_OK


def test_pack_write_failure_exits_three(tmp_path, capsys, valid_mp4):
    blocker = tmp_path / "blocker"
    blocker.write_text("x")
    code, _out, err = run(capsys, "pack", str(valid_mp4),
                          "--out", str(blocker / "out.json"))
    assert code == cli.EXIT_BATCH_FAILED
    assert "cannot write manifest" in err


def test_compare_codec_difference_exits_one(tmp_path, capsys, valid_mp4):
    ref = tmp_path / "ref.json"
    tgt = tmp_path / "tgt.json"
    run(capsys, "pack", str(valid_mp4), "--out", str(ref))
    data = json.loads(ref.read_text())
    for media in data["media"]:
        for track in media["tracks"]:
            if track["kind"] == "video":
                track["codec"] = "hevc"
    tgt.write_text(json.dumps(data))
    code, out, _err = run(capsys, "compare", str(ref), str(tgt))
    assert code == cli.EXIT_INCONSISTENT
    assert "codec_mismatch" in out
    assert "result: HARD_INCONSISTENCIES" in out


def test_compare_duration_difference_exits_one(tmp_path, capsys, valid_mp4):
    ref = tmp_path / "ref.json"
    tgt = tmp_path / "tgt.json"
    run(capsys, "pack", str(valid_mp4), "--out", str(ref))
    data = json.loads(ref.read_text())
    for media in data["media"]:
        media["duration_seconds"] = 4.0
        for track in media["tracks"]:
            track["duration_seconds"] = 4.0
    tgt.write_text(json.dumps(data))
    code, out, _err = run(capsys, "compare", str(ref), str(tgt),
                          "--duration-tolerance", "0.02")
    assert code == cli.EXIT_INCONSISTENT
    assert "duration_delta_exceeded" in out
    assert "duration_delta_seconds: 2.000" in out


def test_compare_with_example_policy(tmp_path, capsys, valid_mp4):
    import json as _json
    root = Path(__file__).resolve().parent.parent
    policy = root / "examples" / "delivery-policy.json"
    assert _json.loads(policy.read_text())["name"] == "level-delivery"
    manifest = tmp_path / "m.json"
    run(capsys, "pack", str(valid_mp4), "--out", str(manifest))
    code, out, _err = run(capsys, "compare", str(manifest), str(manifest),
                          "--policy", str(policy))
    assert code == cli.EXIT_OK
    assert "result: OK" in out


def test_compare_bad_policy_exits_two(tmp_path, capsys, valid_mp4):
    manifest = tmp_path / "m.json"
    run(capsys, "pack", str(valid_mp4), "--out", str(manifest))
    policy = tmp_path / "policy.json"
    policy.write_text('{"duration": {"tolerance": 0.1}}')
    code, _out, err = run(capsys, "compare", str(manifest), str(manifest),
                          "--policy", str(policy))
    assert code == cli.EXIT_USAGE
    assert "policy error" in err
    assert "duration.tolerance" in err


def test_compare_negative_tolerance_is_usage_error(tmp_path, capsys,
                                                  valid_mp4):
    manifest = tmp_path / "m.json"
    run(capsys, "pack", str(valid_mp4), "--out", str(manifest))
    code, _out, err = run(capsys, "compare", str(manifest), str(manifest),
                          "--duration-tolerance", "-0.5")
    assert code == cli.EXIT_USAGE
    assert "duration-tolerance" in err


def test_compare_bad_policy_syntax_reports_position(tmp_path, capsys,
                                                    valid_mp4):
    manifest = tmp_path / "m.json"
    run(capsys, "pack", str(valid_mp4), "--out", str(manifest))
    policy = tmp_path / "policy.json"
    policy.write_text('{\n  "duration": ,\n}\n')
    code, _out, err = run(capsys, "compare", str(manifest), str(manifest),
                          "--policy", str(policy))
    assert code == cli.EXIT_USAGE
    assert "line 2" in err


def test_compare_missing_manifest_is_usage_error(tmp_path, capsys):
    code, _out, err = run(capsys, "compare", str(tmp_path / "nope.json"),
                          str(tmp_path / "nope.json"))
    assert code == cli.EXIT_USAGE
    assert "cannot read" in err


def test_compare_accepts_directories(capsys, sample_dir):
    code, out, _err = run(capsys, "compare", str(sample_dir), str(sample_dir))
    assert code == cli.EXIT_OK
    assert "files_compared: 3" in out


def test_compare_against_database(tmp_path, capsys, sample_dir):
    db = tmp_path / "m.sqlite"
    run(capsys, "scan", str(sample_dir), "--db", str(db))
    manifest = tmp_path / "m.json"
    run(capsys, "pack", str(sample_dir), "--out", str(manifest))
    code, out, _err = run(capsys, "compare", str(manifest), str(manifest),
                          "--ref-db", str(db))
    assert code == cli.EXIT_OK
    assert "files_compared: 3" in out


def test_compare_database_matches_nested_tree_manifest(tmp_path, capsys,
                                                      valid_mp4, valid_wav):
    import shutil
    root = tmp_path / "tree"
    (root / "nested").mkdir(parents=True)
    shutil.copy(valid_mp4, root / "nested" / "clip.mp4")
    shutil.copy(valid_wav, root / "vo.wav")
    db = tmp_path / "m.sqlite"
    manifest = tmp_path / "m.json"
    run(capsys, "scan", str(root), "--db", str(db))
    run(capsys, "pack", str(root), "--out", str(manifest))
    code, out, _err = run(capsys, "compare", str(manifest), str(manifest),
                          "--ref-db", str(db))
    assert code == cli.EXIT_OK
    assert "missing_target_media" not in out
    assert "files_compared: 2" in out


def test_compare_without_any_completed_batch_is_usage_error(tmp_path, capsys,
                                                            valid_mp4):
    db = tmp_path / "empty.sqlite"
    from mediacheck.store import MediaStore
    MediaStore(db).close()
    manifest = tmp_path / "m.json"
    run(capsys, "pack", str(valid_mp4), "--out", str(manifest))
    code, _out, err = run(capsys, "compare", str(manifest), str(manifest),
                          "--ref-db", str(db))
    assert code == cli.EXIT_USAGE
    assert "no completed scan batch" in err


def test_usage_error_exit_two(capsys):
    code, _out, _err = run(capsys, "scan")
    assert code == cli.EXIT_USAGE


def test_report_latest_batch(tmp_path, capsys, sample_dir):
    db = tmp_path / "m.sqlite"
    run(capsys, "scan", str(sample_dir), "--db", str(db))
    code, out, _err = run(capsys, "report", "--db", str(db))
    assert code == cli.EXIT_BATCH_FAILED
    assert "files_scanned: 4" in out
    assert "tracks_total:  5" in out


def test_report_json_output(tmp_path, capsys, sample_dir):
    db = tmp_path / "m.sqlite"
    run(capsys, "scan", str(sample_dir), "--db", str(db))
    out_json = tmp_path / "report.json"
    run(capsys, "report", "--db", str(db), "--json", str(out_json))
    document = json.loads(out_json.read_text())
    assert document["kind"] == "mediacheck.scan"
    assert "codec_summary" in document


def test_report_missing_database_is_usage_error(tmp_path, capsys):
    blocker = tmp_path / "blocker"
    blocker.write_text("x")
    code, _out, err = run(capsys, "report", "--db",
                          str(blocker / "x" / "m.sqlite"))
    assert code == cli.EXIT_USAGE
    assert "report:" in err
