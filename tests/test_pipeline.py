"""End-to-end tests: fixture -> remux (both modes) -> read-back verify.

Also covers CLI exit codes, explicit track selection, subtitle rejection,
atomic output replacement and the guarantee that a corrupt input never
overwrites an existing destination. Run with
``.venv/bin/python tests/test_pipeline.py``.
"""

from __future__ import annotations

import json
import os
import subprocess
import sys
import tempfile

HERE = os.path.dirname(__file__)
ROOT = os.path.join(HERE, "..")
PYTHON = sys.executable
CLI = [PYTHON, "-m", "mkvmp4.cli"]
PASS = 0


def check(condition: bool, label: str) -> None:
    global PASS
    if not condition:
        raise AssertionError(label)
    PASS += 1
    print(f"  ok: {label}")


def run(*args: str) -> tuple[int, str, str]:
    proc = subprocess.run(CLI + list(args), cwd=ROOT,
                          capture_output=True, text=True)
    return proc.returncode, proc.stdout, proc.stderr


def main() -> int:
    workdir = tempfile.mkdtemp(prefix="mkvmp4-e2e-")
    mkv = os.path.join(workdir, "demo.mkv")
    mp4_seg = os.path.join(workdir, "demo-seg.mp4")
    mp4_one = os.path.join(workdir, "demo-one.mp4")

    print("test_fixture_generation")
    rc, out, err = run("inspect", "--input", mkv, "--json")
    check(rc == 2, "missing input file returns exit 2")

    gen = subprocess.run(
        [PYTHON, os.path.join(HERE, "make_fixtures.py"), "--output", mkv],
        cwd=ROOT, capture_output=True, text=True)
    check(gen.returncode == 0 and os.path.isfile(mkv), "fixture generated")

    print("test_inspect")
    rc, out, err = run("inspect", "--input", mkv, "--json")
    check(rc == 0, "inspect exits 0")
    summary = json.loads(out)
    check(30 <= summary["element_count"] <= 200,
          "element_count within expected range")
    check(summary["unknown_length_count"] >= 1,
          "at least one unknown-length element")
    check(2 <= len(summary["tracks"]) <= 4, "track count in range")
    check(all("first_pts_ms" in t and "last_pts_ms" in t
              for t in summary["tracks"]),
          "per-track PTS endpoints present")

    print("test_remux_segmented")
    rc, out, err = run("remux", "--input", mkv, "--output", mp4_seg,
                       "--mode", "segmented", "--json")
    check(rc == 0, f"segmented remux exits 0 ({err.strip()})")
    result = json.loads(out)
    check(len(result["track_map"]) >= 2, "track_map has >=2 entries")
    check(result["verification"]["index_valid"] is True,
          "built-in verification valid")
    check(20 <= result["verification"]["sample_count"] <= 500,
          "sample_count within expected range")
    for entry in result["track_map"]:
        check("rewritten_first_pts_ms" in entry
              and "rewritten_last_pts_ms" in entry,
              "track map carries rewritten timestamp endpoints")

    print("test_remux_single")
    rc, out, err = run("remux", "--input", mkv, "--output", mp4_one,
                       "--mode", "single", "--json")
    check(rc == 0, f"single remux exits 0 ({err.strip()})")
    single = json.loads(out)
    check(single["segments"] == 1, "single mode writes one segment")
    check(single["verification"]["index_valid"] is True,
          "single output verifies")

    print("test_verify_command")
    for mp4 in (mp4_seg, mp4_one):
        rc, out, err = run("verify", "--input", mp4, "--json")
        check(rc == 0, f"verify exits 0 for {os.path.basename(mp4)}")
        verified = json.loads(out)
        check(verified["index_valid"] is True, "index_valid true")
        check("manifest" in verified, "audit manifest present")
        manifest_counts = [t["sample_count"]
                           for t in verified["manifest"]["tracks"]]
        index_counts = [t["sample_count"] for t in verified["tracks"]]
        check(manifest_counts == index_counts,
              "manifest and index sample counts agree")

    print("test_track_selection_and_rejection")
    sub_mkv = os.path.join(workdir, "sub.mkv")
    subprocess.run([PYTHON, os.path.join(HERE, "make_fixtures.py"),
                    "--output", sub_mkv, "--with-subtitle"],
                   cwd=ROOT, capture_output=True, text=True)
    rc, _out, err = run("remux", "--input", sub_mkv,
                        "--output", os.path.join(workdir, "s.mp4"))
    check(rc == 2 and "subtitle" in err,
          "subtitle track rejected with exit 2")
    rc, out, err = run("remux", "--input", mkv,
                       "--output", os.path.join(workdir, "sel.mp4"),
                       "--tracks", "1,2", "--json")
    check(rc == 0 and len(json.loads(out)["track_map"]) == 2,
          "explicit two-track selection honored")
    rc, _out, err = run("remux", "--input", mkv,
                        "--output", os.path.join(workdir, "x.mp4"),
                        "--tracks", "99")
    check(rc == 2, "unknown track number rejected")
    rc, _out, err = run("remux", "--input", mkv,
                        "--output", os.path.join(workdir, "y.mp4"),
                        "--tracks", "")
    check(rc == 2, "empty track selection rejected")

    print("test_safety_and_atomicity")
    rc, _out, err = run("remux", "--input", mkv, "--output", mkv)
    check(rc == 2, "refusing to overwrite the source file")
    target = os.path.join(workdir, "keep.mp4")
    with open(target, "wb") as handle:
        handle.write(b"precious-output")
    corrupt = os.path.join(workdir, "corrupt.mkv")
    with open(corrupt, "wb") as handle:
        handle.write(b"not an ebml document at all")
    rc, _out, err = run("remux", "--input", corrupt, "--output", target)
    check(rc == 2, "corrupt input fails before output")
    with open(target, "rb") as handle:
        check(handle.read() == b"precious-output",
              "existing destination left untouched on failure")
    leftovers = [name for name in os.listdir(workdir)
                 if name.startswith(".tmp-") or name.endswith(".part0")]
    check(not leftovers, "temporary files cleaned up after failure")

    print("test_limits")
    rc, _out, err = run("--max-elements", "3", "inspect",
                        "--input", mkv, "--json")
    check(rc == 2, "element count limit returns exit 2")
    rc, _out, err = run("--max-sample-bytes", "5", "remux",
                        "--input", mkv,
                        "--output", os.path.join(workdir, "z.mp4"))
    check(rc == 2, "sample size limit returns exit 2")

    print(f"\n{sys.argv[0]}: {PASS} checks passed")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
