"""CLI behavior tests: exit codes, batch ordering, JSON contract, fixtures."""

from __future__ import annotations

import json
import os

import pytest

from media_index import cli
from media_index.fixtures import (
    build_nested,
    build_valid,
    inject_cycle,
    inject_misaligned,
)


def _write(tmp_path: str, name: str, blob: bytes) -> str:
    path = os.path.join(tmp_path, name)
    with open(path, "wb") as handle:
        handle.write(blob)
    return path


def test_validate_valid_file_exit_zero_json(tmp_path):
    gen_dir = tmp_path / "gen"
    target = _write(str(tmp_path), "valid.mp4", build_valid())
    code = cli.main(["validate", "--input", target, "--json",
                     "--gen-dir", str(gen_dir)])
    assert code == 0
    assert os.path.exists(os.path.join(str(gen_dir), "valid.mp4"))


def test_validate_misaligned_exit_one_with_offset(tmp_path, capsys):
    gen_dir = tmp_path / "gen"
    target = _write(str(tmp_path), "bad.mp4",
                    inject_misaligned(build_valid()))
    code = cli.main(["validate", "--input", target, "--json",
                     "--gen-dir", str(gen_dir)])
    assert code == 1
    out = capsys.readouterr().out
    bodies = json.loads(out)
    body = bodies[0]
    assert body["index_offset_checks"]["failed"] >= 1
    assert body["errors"]
    assert any(str(c["source_offset"]).isdigit()
               for c in body["checks"] if c["status"] == "fail")


def test_missing_file_exit_two_does_not_abort_batch(tmp_path, capsys):
    gen_dir = tmp_path / "gen"
    good = _write(str(tmp_path), "a.mp4", build_valid())
    missing = str(tmp_path / "ghost.mp4")
    code = cli.main(["validate", "--input", missing, "--input", good,
                     "--json", "--gen-dir", str(gen_dir)])
    assert code == 2
    bodies = json.loads(capsys.readouterr().out)
    # Sorted paths: ghost before a? Sorting is lexical; assert both present.
    assert {b["path"] for b in bodies} == {missing, good}
    assert any(b["status"] == "fail" and "not found" in b["errors"][0]
               for b in bodies)


def test_batch_inputs_processed_in_sorted_order(tmp_path, capsys):
    gen_dir = tmp_path / "gen"
    paths = [
        _write(str(tmp_path), f"z{i}.mp4", build_valid()) for i in range(3)
    ]
    # Pass them in reverse order; output ordering must still be sorted.
    args = ["validate", "--gen-dir", str(gen_dir), "--json"]
    for path in reversed(paths):
        args.extend(["--input", path])
    code = cli.main(args)
    assert code == 0
    bodies = json.loads(capsys.readouterr().out)
    assert [b["path"] for b in bodies] == sorted(paths)


def test_text_output_contains_summary_lines(tmp_path, capsys):
    gen_dir = tmp_path / "gen"
    target = _write(str(tmp_path), "valid.mp4", build_valid())
    code = cli.main(["validate", "--input", target,
                     "--gen-dir", str(gen_dir)])
    assert code == 0
    text = capsys.readouterr().out
    assert "[PASS]" in text and "samples:" in text and "segment 1" in text


def test_json_field_contract_is_stable(tmp_path, capsys):
    gen_dir = tmp_path / "gen"
    target = _write(str(tmp_path), "valid.mp4", build_valid())
    code = cli.main(["validate", "--input", target, "--json",
                     "--gen-dir", str(gen_dir)])
    assert code == 0
    body = json.loads(capsys.readouterr().out)[0]
    for key in ("path", "status", "sample_entries", "index_offset_checks",
                "segment_boundaries", "checks", "errors"):
        assert key in body
    assert set(body["index_offset_checks"]) == {"passed", "failed"}
    boundary = body["segment_boundaries"][0]
    for key in ("order", "source_offset", "byte_start", "byte_end",
                "first_sample", "last_sample", "complete"):
        assert key in boundary


def test_inspect_subcommand_summary(tmp_path, capsys):
    gen_dir = tmp_path / "gen"
    target = _write(str(tmp_path), "valid.mp4", build_valid())
    code = cli.main(["inspect", "--input", target, "--json",
                     "--gen-dir", str(gen_dir)])
    assert code == 0
    summary = json.loads(capsys.readouterr().out)[0]
    assert summary["sidx"]["timescale"] == 90000
    assert summary["stbl"]["sample_count"] == 6
    assert summary["stbl"]["has_ctts"] is True


def test_fixture_generation_is_idempotent(tmp_path):
    gen_dir = tmp_path / "gen"
    first = cli.generate_fixtures(str(gen_dir))
    mtime = os.path.getmtime(first["valid.mp4"])
    second = cli.generate_fixtures(str(gen_dir))
    assert os.path.getmtime(second["valid.mp4"]) == mtime
    assert set(first) == set(second)


def test_cycle_injection_is_rejected(tmp_path):
    from media_index.pipeline import PipelineError, run_pipeline

    blob = inject_cycle(build_nested())
    with pytest.raises(PipelineError):
        run_pipeline("cycle", bytes(blob))


def test_no_uncaught_exception_on_garbage(tmp_path):
    gen_dir = tmp_path / "gen"
    target = _write(str(tmp_path), "garbage.mp4",
                    b"\x00" * 37 + b"not-a-box")
    code = cli.main(["validate", "--input", target, "--json",
                     "--gen-dir", str(gen_dir)])
    # Garbage is an input/structural error -> exit 2, no traceback printed.
    assert code == 2
