"""Command line interface: make-fixture, inspect, remux and inspect-mp4.

Exit codes follow the project contract:
  0 - success
  1 - parsing / mapping / muxing / verification failure
  2 - invalid command line arguments
"""

from __future__ import annotations

import argparse
import json
import os
import sys
import tempfile

from . import MAX_TOTAL_INPUT_SIZE
from .mapping import MappingError, select_tracks
from .matroska import MatroskaError, parse_matroska
from .mux import MuxError, remux_to_stream, verify_mp4
from .report import Mp4InspectionError, build_mkv_report, inspect_mp4
from .timeline import TimelineError, build_timelines

EXIT_OK = 0
EXIT_FAILURE = 1
EXIT_USAGE = 2


def _open_checked(path: str, mode: str):
    if "r" in mode and not os.path.isfile(path):
        raise FileNotFoundError(f"input file does not exist: {path}")
    f = open(path, mode)
    if "r" in mode:
        f.seek(0, os.SEEK_END)
        size = f.tell()
        f.seek(0)
        if size > MAX_TOTAL_INPUT_SIZE:
            f.close()
            raise MatroskaError(
                f"input size {size} exceeds limit {MAX_TOTAL_INPUT_SIZE}")
    return f


def _parse_input(path: str):
    f = _open_checked(path, "rb")
    try:
        return parse_matroska(f), f
    except Exception:
        f.close()
        raise


def _build(mkv, *, strict: bool):
    mapping = select_tracks(mkv.tracks, strict=strict)
    if not mapping.accepted:
        return mapping, {}, 0
    timelines, _, shift_ns = build_timelines(
        mkv, mapping.accepted_numbers)
    return mapping, timelines, shift_ns


def _cmd_make_fixture(args) -> int:
    # tests/ is not an installed package; load the module directly by path.
    import importlib.util

    root = os.getcwd()
    candidate = os.path.join(root, "tests", "fixture_builder.py")
    if not os.path.exists(candidate):
        candidate = os.path.join(
            os.path.dirname(os.path.dirname(os.path.dirname(
                os.path.abspath(__file__)))), "tests", "fixture_builder.py")
    spec = importlib.util.spec_from_file_location("fixture_builder", candidate)
    if spec is None or spec.loader is None:
        raise MatroskaError(f"cannot load fixture builder from {candidate}")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    path = module.build_sample_mkv(args.output)
    print(f"fixture written: {path}")
    return EXIT_OK


def _cmd_inspect(args) -> int:
    mkv, src = _parse_input(args.input)
    try:
        mapping, timelines, shift_ns = _build(mkv, strict=False)
        report = build_mkv_report(
            mkv, mapping, timelines, shift_ns, extra_warnings=[])
    finally:
        src.close()
    if args.json:
        print(json.dumps(report, indent=2, sort_keys=False))
    else:
        _print_mkv_summary(report)
    return EXIT_OK


def _cmd_remux(args) -> int:
    mkv, src = _parse_input(args.input)
    try:
        mapping = select_tracks(mkv.tracks, strict=True)
        timelines, _, shift_ns = build_timelines(
            mkv, mapping.accepted_numbers)
        out_dir = os.path.dirname(os.path.abspath(args.output)) or "."
        os.makedirs(out_dir, exist_ok=True)
        tmp_fd, tmp_path = tempfile.mkstemp(
            prefix=".mkvmp4-", suffix=".tmp", dir=out_dir)
        os.close(tmp_fd)
        try:
            with open(tmp_path, "w+b") as dst:
                result = remux_to_stream(
                    src, dst, mkv, mapping, timelines)
                box_count, box_tree = verify_mp4(dst, result)
                result.total_boxes = box_count
                result.box_tree = box_tree
                dst.flush()
                os.fsync(dst.fileno())
            report = build_mkv_report(
                mkv, mapping, timelines, shift_ns,
                extra_warnings=[], mux=result)
            os.replace(tmp_path, args.output)  # atomic swap
        except Exception:
            if os.path.exists(tmp_path):
                os.unlink(tmp_path)  # keep any pre-existing target intact
            raise
    finally:
        src.close()
    if args.json:
        print(json.dumps(report, indent=2, sort_keys=False))
    else:
        _print_mkv_summary(report)
        print(f"mp4 written: {args.output} "
              f"({report['mp4']['box_count']} boxes)")
    return EXIT_OK


def _cmd_inspect_mp4(args) -> int:
    with _open_checked(args.input, "rb") as f:
        report = inspect_mp4(f)
    if args.json:
        print(json.dumps(report, indent=2, sort_keys=False))
    else:
        print(f"file_size={report['file_size']} "
              f"box_count={report['box_count']}")
        for tr in report["tracks"]:
            print(f"  track {tr['mp4_track_id']} ({tr['handler']}): "
                  f"{tr['sample_count']} samples, "
                  f"{tr['first_timestamp_seconds']} -> "
                  f"{tr['last_timestamp_seconds']} s")
    return EXIT_OK


def _print_mkv_summary(report: dict) -> None:
    print(f"elements: {report['element_count']}")
    for node in report["element_tree"]:
        print(f"  {'  ' * node['depth']}{node['name']} x{node['count']}")
    print("track mapping (mkv -> mp4):")
    for entry in report["track_mapping"]:
        print(f"  {entry['mkv_track_number']} -> "
              f"{entry['mp4_track_id']}  {entry['decision']} "
              f"({entry['reason']})")
    for tr in report["tracks"]:
        print(f"  track {tr['mkv_track_number']}: "
              f"{tr['sample_count']} samples, "
              f"{tr['first_timestamp_seconds']} -> "
              f"{tr['last_timestamp_seconds']} s")
    print(f"warnings: {report['warning_count']}")
    if "mp4" in report:
        print(f"mp4 boxes: {report['mp4']['box_count']}")


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="mkvmp4",
        description="Local Matroska -> MP4 remuxer (no re-encode).")
    sub = parser.add_subparsers(dest="command", required=True)

    p_fix = sub.add_parser("make-fixture", help="generate a sample MKV")
    p_fix.add_argument("--output", required=True)
    p_fix.set_defaults(func=_cmd_make_fixture)

    p_ins = sub.add_parser("inspect", help="inspect a Matroska file")
    p_ins.add_argument("input")
    p_ins.add_argument("--json", action="store_true")
    p_ins.set_defaults(func=_cmd_inspect)

    p_rem = sub.add_parser("remux", help="remux Matroska into MP4")
    p_rem.add_argument("input")
    p_rem.add_argument("--output", required=True)
    p_rem.add_argument("--json", action="store_true")
    p_rem.set_defaults(func=_cmd_remux)

    p_mp4 = sub.add_parser("inspect-mp4", help="inspect a produced MP4")
    p_mp4.add_argument("input")
    p_mp4.add_argument("--json", action="store_true")
    p_mp4.set_defaults(func=_cmd_inspect_mp4)
    return parser


def main(argv: list[str] | None = None) -> int:
    parser = build_parser()
    args = parser.parse_args(argv)
    try:
        return args.func(args)
    except (MatroskaError, TimelineError, MappingError, MuxError,
            Mp4InspectionError, FileNotFoundError, OSError) as exc:
        print(f"error: {exc}", file=sys.stderr)
        return EXIT_FAILURE


if __name__ == "__main__":
    raise SystemExit(main())
