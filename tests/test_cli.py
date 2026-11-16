"""End-to-end CLI tests covering exit codes and batch resilience."""

from __future__ import annotations

import json
import os
import sys

sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "tools"))

import make_samples as samples  # noqa: E402

from media_audit.cli import main  # noqa: E402


def _build_sample_tree(root: str) -> str:
    os.makedirs(root, exist_ok=True)
    for name, builder in samples.SAMPLES.items():
        with open(os.path.join(root, name), "wb") as handle:
            handle.write(builder())
    with open(os.path.join(root, "delivery_notes.txt"), "w") as handle:
        handle.write("not a container\n")
    manifest_path = os.path.join(root, "manifest.json")
    with open(manifest_path, "w") as handle:
        json.dump(samples.build_manifest(), handle)
    return manifest_path


def test_audit_exit_code_3_on_parse_failure(tmp_path):
    root = os.path.join(str(tmp_path), "media")
    manifest = _build_sample_tree(root)
    db = os.path.join(str(tmp_path), "audit.sqlite")
    report = os.path.join(str(tmp_path), "report.json")
    code = main([
        "audit", "--input", root, "--manifest", manifest,
        "--db", db, "--json", report, "--duration-tolerance-ms", "80",
    ])
    assert code == 3  # truncated sample guarantees a parse failure
    payload = json.load(open(report))
    assert len(payload["files"]) == 6
    assert len(payload["tracks"]) == 12
    assert payload["duration_delta_ms"] == 120
    assert len(payload["mismatches"]) == 4
    assert len(payload["errors"]) == 1
    assert payload["errors"][0]["offset"] is not None


def test_audit_exit_code_1_for_clean_parse_with_mismatch(tmp_path):
    root = os.path.join(str(tmp_path), "media")
    os.makedirs(root)
    # Only two well-formed files with a duration gap beyond tolerance.
    with open(os.path.join(root, "a.mp4"), "wb") as handle:
        handle.write(samples.sample_cutscene_a())
    with open(os.path.join(root, "b.mp4"), "wb") as handle:
        handle.write(samples.sample_cutscene_b())
    manifest = {
        "groups": [
            {"name": "g", "files": ["a.mp4", "b.mp4"],
             "expect": [{"type": "video", "count": 1}]}
        ]
    }
    manifest_path = os.path.join(root, "manifest.json")
    json.dump(manifest, open(manifest_path, "w"))
    code = main([
        "audit", "--input", root, "--manifest", manifest_path,
        "--db", os.path.join(str(tmp_path), "a.sqlite"),
        "--duration-tolerance-ms", "10",
    ])
    assert code == 1


def test_audit_exit_code_0_when_clean(tmp_path):
    root = os.path.join(str(tmp_path), "media")
    os.makedirs(root)
    # Two copies of the same well-formed file -> identical metadata.
    data = samples.sample_cutscene_a()
    with open(os.path.join(root, "a.mp4"), "wb") as handle:
        handle.write(data)
    with open(os.path.join(root, "b.mp4"), "wb") as handle:
        handle.write(data)
    manifest = {
        "groups": [
            {"name": "g", "files": ["a.mp4", "b.mp4"], "expect": []}
        ]
    }
    manifest_path = os.path.join(root, "manifest.json")
    json.dump(manifest, open(manifest_path, "w"))
    code = main([
        "audit", "--input", root, "--manifest", manifest_path,
        "--db", os.path.join(str(tmp_path), "a.sqlite"),
    ])
    assert code == 0


def test_audit_exit_code_2_for_bad_manifest(tmp_path):
    root = os.path.join(str(tmp_path), "media")
    os.makedirs(root)
    manifest_path = os.path.join(root, "manifest.json")
    with open(manifest_path, "w") as handle:
        handle.write('{"groups": [{}]}')
    code = main([
        "audit", "--input", root, "--manifest", manifest_path,
        "--db", os.path.join(str(tmp_path), "a.sqlite"),
    ])
    assert code == 2


def test_audit_exit_code_2_for_missing_input(tmp_path):
    code = main([
        "audit", "--input", os.path.join(str(tmp_path), "missing"),
        "--manifest", "x", "--db", os.path.join(str(tmp_path), "a.sqlite"),
    ])
    assert code == 2


def test_audit_refuses_json_inside_input_root(tmp_path):
    root = os.path.join(str(tmp_path), "media")
    manifest = _build_sample_tree(root)
    code = main([
        "audit", "--input", root, "--manifest", manifest,
        "--db", os.path.join(str(tmp_path), "a.sqlite"),
        "--json", os.path.join(root, "report.json"),
    ])
    assert code == 2


def test_audit_skips_unknown_extension(tmp_path):
    root = os.path.join(str(tmp_path), "media")
    manifest = _build_sample_tree(root)
    report = os.path.join(str(tmp_path), "report.json")
    main([
        "audit", "--input", root, "--manifest", manifest,
        "--db", os.path.join(str(tmp_path), "a.sqlite"),
        "--json", report, "--duration-tolerance-ms", "80",
    ])
    payload = json.load(open(report))
    skipped_paths = {item["path"] for item in payload["skipped"]}
    assert "delivery_notes.txt" in skipped_paths


def test_export_reproduces_report(tmp_path):
    root = os.path.join(str(tmp_path), "media")
    manifest = _build_sample_tree(root)
    db = os.path.join(str(tmp_path), "audit.sqlite")
    first_report = os.path.join(str(tmp_path), "first.json")
    main([
        "audit", "--input", root, "--manifest", manifest, "--db", db,
        "--json", first_report, "--duration-tolerance-ms", "80",
    ])
    second_report = os.path.join(str(tmp_path), "second.json")
    code = main([
        "export", "--input", root, "--manifest", manifest,
        "--db", db, "--json", second_report,
    ])
    assert code == 0
    first = json.load(open(first_report))
    second = json.load(open(second_report))
    assert first["files"] == second["files"]
    assert first["tracks"] == second["tracks"]
    assert first["mismatches"] == second["mismatches"]


def test_audit_continues_when_file_vanishes(tmp_path, monkeypatch):
    root = os.path.join(str(tmp_path), "media")
    manifest = _build_sample_tree(root)

    real_exists = os.path.exists

    def flaky_exists(path):
        if path.endswith("cutscene_a.mp4"):
            return False
        return real_exists(path)

    monkeypatch.setattr("media_audit.cli.os.path.exists", flaky_exists)
    report = os.path.join(str(tmp_path), "report.json")
    code = main([
        "audit", "--input", root, "--manifest", manifest,
        "--db", os.path.join(str(tmp_path), "a.sqlite"),
        "--json", report, "--duration-tolerance-ms", "80",
    ])
    monkeypatch.undo()
    # Batch still completes: missing file is a stored event, not a crash.
    assert code in (1, 3)
    payload = json.load(open(report))
    error_files = {item["file"] for item in payload["errors"]}
    assert "cutscene_a.mp4" in error_files
