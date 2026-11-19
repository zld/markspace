"""End-to-end tests for the audit CLI: outputs, consistency and exit codes."""

from __future__ import annotations

from itertools import pairwise

import json
from pathlib import Path

import pytest

from frame_audit import cli

SUMMARY_KEYS = (
    "decoded_frames",
    "i_frames",
    "p_frames",
    "b_frames",
    "repaired_timestamps",
    "max_timestamp_jump_us",
    "truncated",
)


def run(capsys: pytest.CaptureFixture[str], *args: str) -> tuple[int, str, str]:
    code = cli.main(list(args))
    captured = capsys.readouterr()
    return code, captured.out, captured.err


def parse_table(output: str) -> dict[str, dict[str, str]]:
    """Parse the whitespace-aligned table into ``{file: {field: value}}``.

    Column values never contain spaces (paths are relative and suffix-checked),
    so splitting on whitespace after the header and its dashed separator is
    unambiguous.
    """
    lines = [
        line
        for line in output.splitlines()
        if line.strip() and not line.startswith(("*", "!", "-"))
        and set(line.strip()) != {"-"} and "----" not in line
    ]
    assert len(lines) >= 1, output
    headers = lines[0].split()
    rows: dict[str, dict[str, str]] = {}
    for line in lines[1:]:
        cells = line.split()
        assert len(cells) == len(headers), line
        values = dict(zip(headers, cells, strict=True))
        rows[values["file"]] = values
    return rows


# ------------------------------------------------------------------ acceptance


def test_acceptance_sample_produces_expected_summary(
    capsys: pytest.CaptureFixture[str], sample_mp4: Path, tmp_path: Path
) -> None:
    json_out = tmp_path / "report.jsonl"
    code, out, err = run(
        capsys, "audit", str(sample_mp4), "--json-out", str(json_out)
    )
    assert code == 0, err

    rows = parse_table(out)
    row = rows[str(sample_mp4)]
    assert int(row["decoded_frames"]) == 96
    assert int(row["i_frames"]) + int(row["p_frames"]) + int(row["b_frames"]) == 96
    assert int(row["i_frames"]) >= 1
    assert int(row["p_frames"]) >= 1
    assert int(row["b_frames"]) >= 1
    assert int(row["repaired_timestamps"]) >= 3
    assert int(row["max_timestamp_jump_us"]) > 0
    assert row["truncated"] == "no"

    document = json.loads(json_out.read_text(encoding="utf-8").splitlines()[0])
    for key in SUMMARY_KEYS:
        expected = row[key]
        actual = document[key]
        if isinstance(actual, bool):
            assert (expected == "yes") is actual, key
        else:
            assert int(expected) == actual, key


# -------------------------------------------------------- output consistency


def test_terminal_and_json_share_repaired_frames(
    capsys: pytest.CaptureFixture[str], sample_mp4: Path, tmp_path: Path
) -> None:
    json_out = tmp_path / "report.jsonl"
    code, out, _ = run(capsys, "audit", str(sample_mp4), "--json-out", str(json_out))
    assert code == 0
    row = parse_table(out)[str(sample_mp4)]

    document = json.loads(json_out.read_text(encoding="utf-8").splitlines()[0])
    assert document["decoded_frames"] == len(document["frames"])
    assert document["repaired_timestamps"] == int(row["repaired_timestamps"])
    assert document["max_timestamp_jump_us"] == int(row["max_timestamp_jump_us"])

    repaired = [f for f in document["frames"] if f["applied"]]
    assert len(repaired) == document["repaired_timestamps"]
    for frame in repaired:
        assert frame["repair"] != "none"
        assert frame["repaired_pts"] != frame["original_pts"] or frame["original_pts"] is None

    # The emitted timeline is strictly increasing for every frame it produced.
    timeline = [f["repaired_pts"] for f in document["frames"] if f["repaired_pts"] is not None]
    assert all(b > a for a, b in pairwise(timeline))


def test_json_flag_prints_the_same_document(
    capsys: pytest.CaptureFixture[str], clean_mp4: Path, tmp_path: Path
) -> None:
    json_out = tmp_path / "out.jsonl"
    code, stdout_text, _ = run(capsys, "audit", str(clean_mp4), "--json")
    assert code == 0
    streamed = json.loads(stdout_text.splitlines()[0])

    code, _, _ = run(capsys, "audit", str(clean_mp4), "--json-out", str(json_out), "--quiet")
    assert code == 0
    written = json.loads(json_out.read_text(encoding="utf-8").splitlines()[0])
    assert streamed == written


# ------------------------------------------------------------------ batch mode


def test_batch_scans_directories_in_relative_path_order(
    capsys: pytest.CaptureFixture[str], batch_dir: Path
) -> None:
    code, out, _ = run(capsys, "audit", str(batch_dir))
    assert code == 0
    rows = parse_table(out)
    assert list(rows) == ["a.mp4", "nested/b.ts"]
    assert "notes.txt: skipped (not a video file)" in out
    assert "timing.json: skipped (not a video file)" in out
    assert "still image" in out


def test_batch_json_keeps_skipped_entries_with_reasons(
    capsys: pytest.CaptureFixture[str], batch_dir: Path, tmp_path: Path
) -> None:
    json_out = tmp_path / "batch.jsonl"
    code, _, _ = run(capsys, "audit", str(batch_dir), "--json-out", str(json_out))
    assert code == 0
    documents = [
        json.loads(line) for line in json_out.read_text(encoding="utf-8").splitlines()
    ]
    by_file = {doc["file"]: doc for doc in documents}
    assert by_file["notes.txt"]["skipped_reason"] == "not a video file"
    assert by_file["poster.png"]["skipped_reason"] == "still image, not a video"
    assert by_file["a.mp4"]["skipped_reason"] is None
    assert by_file["a.mp4"]["decoded_frames"] > 0


def test_one_failure_does_not_erase_other_results(
    capsys: pytest.CaptureFixture[str], sample_mp4: Path, tmp_path: Path
) -> None:
    missing = tmp_path / "does-not-exist.mp4"
    json_out = tmp_path / "mixed.jsonl"
    code, out, err = run(
        capsys,
        "audit",
        str(sample_mp4),
        str(missing),
        "--json-out",
        str(json_out),
    )
    assert code == 1
    assert "does-not-exist.mp4" in out
    documents = [
        json.loads(line) for line in json_out.read_text(encoding="utf-8").splitlines()
    ]
    good = next(doc for doc in documents if doc["file"] == str(sample_mp4))
    bad = next(doc for doc in documents if doc["file"] == str(missing))
    assert good["ok"] is True and good["decoded_frames"] == 96
    assert bad["ok"] is False and bad["error_reason"] == "not_found"


# --------------------------------------------------------------- max frames


def test_max_frames_marks_the_scan_as_truncated(
    capsys: pytest.CaptureFixture[str], sample_mp4: Path, tmp_path: Path
) -> None:
    json_out = tmp_path / "truncated.jsonl"
    code, out, _ = run(
        capsys, "audit", str(sample_mp4), "--max-frames", "25", "--json-out", str(json_out)
    )
    assert code == 0
    row = parse_table(out)[str(sample_mp4)]
    assert row["decoded_frames"] == "25"
    assert row["truncated"] == "yes"
    document = json.loads(json_out.read_text(encoding="utf-8").splitlines()[0])
    assert document["truncated"] is True
    assert any("partial scan" in warning for warning in document["warnings"])


def test_max_frames_that_covers_the_whole_file_is_not_truncated(
    capsys: pytest.CaptureFixture[str], clean_mp4: Path
) -> None:
    code, out, _ = run(capsys, "audit", str(clean_mp4), "--max-frames", "5000")
    assert code == 0
    row = parse_table(out)[str(clean_mp4)]
    assert row["truncated"] == "no"


# ------------------------------------------------------------- policies


def test_report_policy_detects_without_rewriting(
    capsys: pytest.CaptureFixture[str], sample_mp4: Path, tmp_path: Path
) -> None:
    json_out = tmp_path / "report-policy.jsonl"
    code, out, _ = run(
        capsys,
        "audit",
        str(sample_mp4),
        "--on-timestamp-error",
        "report",
        "--json-out",
        str(json_out),
    )
    assert code == 0
    row = parse_table(out)[str(sample_mp4)]
    assert row["repaired_timestamps"] == "0"

    document = json.loads(json_out.read_text(encoding="utf-8").splitlines()[0])
    assert document["repaired_timestamps"] == 0
    assert document["detected_anomalies"] >= 3
    assert all(not frame["applied"] for frame in document["frames"])


# --------------------------------------------------------------- exit codes


def test_exit_code_zero_for_a_successful_scan(
    capsys: pytest.CaptureFixture[str], clean_mp4: Path
) -> None:
    code, _, _ = run(capsys, "audit", str(clean_mp4))
    assert code == 0


def test_exit_code_one_for_missing_input(
    capsys: pytest.CaptureFixture[str], tmp_path: Path
) -> None:
    code, out, _ = run(capsys, "audit", str(tmp_path / "absent.mp4"))
    assert code == 1
    assert "file not found" in out


def test_exit_code_one_for_a_file_without_video(
    capsys: pytest.CaptureFixture[str], tmp_path: Path
) -> None:
    junk = tmp_path / "junk.mp4"
    junk.write_bytes(b"this is not a video" * 64)
    code, out, _ = run(capsys, "audit", str(junk))
    assert code == 1
    assert "cannot open container" in out


def test_exit_code_two_for_invalid_arguments(
    capsys: pytest.CaptureFixture[str], clean_mp4: Path
) -> None:
    with pytest.raises(SystemExit) as excinfo:
        cli.main(["audit", str(clean_mp4), "--max-frames", "0"])
    assert excinfo.value.code == 2

    with pytest.raises(SystemExit) as excinfo:
        cli.main(["audit", str(clean_mp4), "--on-timestamp-error", "explode"])
    assert excinfo.value.code == 2


def test_exit_code_two_when_no_command_is_given(capsys: pytest.CaptureFixture[str]) -> None:
    code, _, err = run(capsys)
    assert code == 2
    assert "usage" in err.lower()


def test_exit_code_three_when_the_output_cannot_be_written(
    capsys: pytest.CaptureFixture[str], clean_mp4: Path, tmp_path: Path
) -> None:
    blocker = tmp_path / "blocked"
    blocker.write_text("not a directory", encoding="utf-8")
    target = blocker / "report.jsonl"
    code, _, err = run(capsys, "audit", str(clean_mp4), "--json-out", str(target))
    assert code == 3
    assert "cannot write JSON output" in err
    assert not target.exists()


def test_exit_code_one_for_corrupt_payloads(
    capsys: pytest.CaptureFixture[str], corrupt_mp4: Path
) -> None:
    code, out, _ = run(capsys, "audit", str(corrupt_mp4))
    # Corrupt packets are survivable: whatever decoded is still reported.
    assert code in (0, 1)
    assert "failed to decode" in out


# ------------------------------------------------------------------ version


def test_version_flag(capsys: pytest.CaptureFixture[str]) -> None:
    with pytest.raises(SystemExit) as excinfo:
        cli.main(["--version"])
    assert excinfo.value.code == 0
    assert "frame-audit" in capsys.readouterr().out
