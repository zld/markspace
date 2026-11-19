"""Tests for the individual pipeline stages: probe, decode, audit and report.

These run against generated media so container quirks (B-frame reordering,
missing/duplicate PTS, corrupt payloads) are exercised for real rather than
mocked.
"""

from __future__ import annotations

import json
from fractions import Fraction
from pathlib import Path

import pytest

from frame_audit.audit import audit_file
from frame_audit.decode import decode_frames
from frame_audit.errors import DecodeError, InputError
from frame_audit.model import Policy
from frame_audit.probe import probe
from frame_audit.report import result_to_json, summary_fields
from frame_audit.sinks import render_table


# ------------------------------------------------------------------- probing


def test_probe_reads_basic_stream_facts(clean_mp4: Path) -> None:
    info = probe(clean_mp4)
    assert info.format_name.startswith("mov")
    assert info.codec_name == "h264"
    assert (info.width, info.height) == (160, 96)
    assert info.time_base > 0
    assert info.average_rate == 24
    assert info.container_frames == 48
    assert info.container_duration_us is not None


def test_probe_uses_container_units_for_duration(clean_ts: Path) -> None:
    info = probe(clean_ts)
    # MPEG-TS reports no per-stream frame count; the audit must not fabricate one.
    assert info.container_frames is None
    assert info.time_base == Fraction(1, 90_000)
    assert info.container_duration_us is not None


def test_probe_rejects_missing_file(tmp_path: Path) -> None:
    with pytest.raises(InputError) as excinfo:
        probe(tmp_path / "nope.mp4")
    assert excinfo.value.reason == "not_found"
    assert excinfo.value.exit_code == 1


def test_probe_rejects_garbage(tmp_path: Path) -> None:
    junk = tmp_path / "junk.mp4"
    junk.write_bytes(b"not a container" * 100)
    with pytest.raises(InputError) as excinfo:
        probe(junk)
    assert excinfo.value.reason == "open_failed"


def test_probe_rejects_audio_only(audio_only: Path) -> None:
    with pytest.raises(InputError) as excinfo:
        probe(audio_only)
    assert excinfo.value.reason == "no_video_stream"


# ------------------------------------------------------------------ decoding


def test_decode_reports_frame_types_and_time_base(clean_mp4: Path) -> None:
    outcome = decode_frames(clean_mp4)
    assert outcome.count == 48
    assert not outcome.truncated
    assert not outcome.decode_errors
    assert {frame.pict_type for frame in outcome.frames} <= {"I", "P", "B"}
    assert all(frame.time_base > 0 for frame in outcome.frames)


def test_decode_preserves_missing_pts_in_ts(missing_ts: Path) -> None:
    outcome = decode_frames(missing_ts)
    missing = [frame for frame in outcome.frames if frame.pts is None]
    assert missing, "MPEG-TS should keep dropped PTS absent"
    # The decoder invents no PTS for those frames: the audit has to fill them.
    assert len(missing) == 3


def test_decode_counts_b_frames_from_real_decoding(clean_mp4: Path) -> None:
    outcome = decode_frames(clean_mp4)
    types = [frame.pict_type for frame in outcome.frames]
    assert types.count("B") > 0
    assert types.count("I") >= 2


def test_decode_stops_at_the_limit_and_flags_truncation(clean_mp4: Path) -> None:
    outcome = decode_frames(clean_mp4, max_frames=10)
    assert outcome.count == 10
    assert outcome.truncated is True


def test_decode_at_exact_frame_count_is_not_truncated(clean_mp4: Path) -> None:
    outcome = decode_frames(clean_mp4, max_frames=48)
    assert outcome.count == 48
    assert outcome.truncated is False


def test_decode_rejects_non_positive_limit(clean_mp4: Path) -> None:
    with pytest.raises(ValueError):
        decode_frames(clean_mp4, max_frames=0)


def test_decode_records_corrupt_packets_and_continues(corrupt_mp4: Path) -> None:
    outcome = decode_frames(corrupt_mp4)
    assert outcome.decode_errors, "corrupt payload should produce decode errors"
    # Whatever decoded before/after the damage must still be reported.
    assert outcome.count > 0


def test_decode_requires_a_usable_time_base(
    tmp_path: Path, clean_mp4: Path
) -> None:
    with pytest.raises(DecodeError) as excinfo:
        decode_frames(clean_mp4, time_base=Fraction(0))
    assert excinfo.value.reason == "invalid_time_base"
    assert excinfo.value.exit_code == 1


# ---------------------------------------------------------- audit behaviour


def test_audit_counts_match_decoded_frames(sample_mp4: Path) -> None:
    result = audit_file(sample_mp4)
    assert result.ok
    assert result.decoded_frames == 96
    assert result.i_frames + result.p_frames + result.b_frames == 96
    assert result.repaired_timestamps >= 3
    assert result.max_timestamp_jump_us > 0
    assert result.truncated is False


def test_audit_emits_a_strictly_increasing_timeline(sample_mp4: Path) -> None:
    result = audit_file(sample_mp4)
    timeline = [frame.repaired_pts for frame in result.frames]
    assert all(value is not None for value in timeline)
    assert all(b > a for a, b in zip(timeline, timeline[1:]))


def test_audit_keeps_original_timestamps_alongside_repairs(sample_mp4: Path) -> None:
    result = audit_file(sample_mp4)
    repaired = [frame for frame in result.frames if frame.applied]
    assert repaired
    for frame in repaired:
        assert frame.kind.value != "none"
        assert frame.original_pts != frame.repaired_pts


def test_audit_repairs_regressions(regression_mp4: Path) -> None:
    result = audit_file(regression_mp4)
    assert result.repaired_timestamps >= 1
    assert result.repair_breakdown["regression"] + result.repair_breakdown["duplicate"] >= 1


def test_audit_report_policy_keeps_originals(missing_ts: Path) -> None:
    result = audit_file(missing_ts, policy=Policy.REPORT)
    assert result.repaired_timestamps == 0
    assert result.detected_anomalies >= 1
    assert all(not frame.applied for frame in result.frames)


def test_audit_truncation_is_never_reported_as_a_complete_scan(clean_mp4: Path) -> None:
    result = audit_file(clean_mp4, max_frames=12)
    assert result.decoded_frames == 12
    assert result.truncated is True
    assert any("partial scan" in warning for warning in result.warnings)


def test_audit_records_failures_instead_of_raising(tmp_path: Path) -> None:
    result = audit_file(tmp_path / "absent.mp4")
    assert not result.ok
    assert result.error_reason == "not_found"
    assert result.decoded_frames == 0


def test_audit_flags_an_empty_video_stream(zero_frame_mp4: Path) -> None:
    result = audit_file(zero_frame_mp4)
    assert not result.ok
    assert result.error_reason == "no_frames"
    assert result.decoded_frames == 0
    assert result.stream is not None and result.stream.codec_name == "h264"


def test_audit_counts_corrupt_packets_but_keeps_good_frames(corrupt_mp4: Path) -> None:
    result = audit_file(corrupt_mp4)
    assert result.decode_errors
    assert result.decoded_frames > 0
    assert any("failed to decode" in warning for warning in result.warnings)


# ---------------------------------------------------------------- reporting


def test_report_matches_the_rendered_table(sample_mp4: Path) -> None:
    result = audit_file(sample_mp4)
    summary = summary_fields(result)

    row = [line for line in render_table([result]).splitlines() if str(sample_mp4) in line]
    assert len(row) == 1
    cells = row[0].split()
    assert cells[0] == summary["file"]
    assert int(cells[1]) == summary["decoded_frames"]
    assert int(cells[2]) == summary["i_frames"]
    assert int(cells[3]) == summary["p_frames"]
    assert int(cells[4]) == summary["b_frames"]
    assert int(cells[5]) == summary["repaired_timestamps"]
    assert int(cells[6]) == summary["max_timestamp_jump_us"]
    assert cells[7] == ("yes" if summary["truncated"] else "no")


def test_json_document_is_serialisable_and_complete(sample_mp4: Path) -> None:
    result = audit_file(sample_mp4)
    document = result_to_json(result)
    round_tripped = json.loads(json.dumps(document))
    assert round_tripped["decoded_frames"] == result.decoded_frames
    assert len(round_tripped["frames"]) == result.decoded_frames
    assert round_tripped["stream"]["codec"] == "h264"
    assert round_tripped["repair_breakdown"] == result.repair_breakdown


def test_json_and_table_agree_for_every_output_field(
    clean_mp4: Path, missing_ts: Path, regression_mp4: Path
) -> None:
    results = [audit_file(path) for path in (clean_mp4, missing_ts, regression_mp4)]
    table_lines = {
        line.split()[0]: line.split()
        for line in render_table(results).splitlines()
        if line and not line.startswith(("*", "!", "-")) and "----" not in line
    }
    for result in results:
        document = result_to_json(result)
        cells = table_lines[document["file"]]
        assert int(cells[1]) == document["decoded_frames"]
        assert int(cells[2]) == document["i_frames"]
        assert int(cells[3]) == document["p_frames"]
        assert int(cells[4]) == document["b_frames"]
        assert int(cells[5]) == document["repaired_timestamps"]
        assert int(cells[6]) == document["max_timestamp_jump_us"]
        assert (cells[7] == "yes") is document["truncated"]
