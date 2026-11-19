"""fragcheck command line: inspect, rebuild, fixture.

Exit codes: 0 = clean, 2 = structurally malformed input (or unreadable
file / unsafe output path), 3 = validation failure or repair ambiguity.
"""
from __future__ import annotations

import argparse
import json
import os
import sys

from . import repair, validate
from .boxes import StructuralError
from .fixture import CORRUPTIONS, write_fixture


def _print_text(report):
    print(f"file: {report['file']} ({report['file_size']} bytes)")
    for t in report["tracks"]:
        print(f"track {t['track_id']}: timescale={t['timescale']} "
              f"sample_entries={t['sample_entries']} "
              f"chunks={t['chunk_count']} "
              f"duration={t['duration_ticks']}/{t['timescale']} "
              f"consistent={str(t['consistent']).lower()}")
    s = report["sidx"]
    print(f"sidx: present={str(s['present']).lower()} "
          f"references={s['references']} "
          f"hierarchical={s['hierarchical_references']} "
          f"offset_valid={str(report['sidx_offset_valid']).lower()}")
    print(f"fragments: {len(report['fragments'])}")
    for f in report["fragments"]:
        tail = "" if f["closed"] else f" UNCLOSED ({f['note']})"
        print(f"  [{f['index']}] {f['start_offset']}..{f['end_offset']} "
              f"({f['byte_length']} bytes){tail}")
    for w in report["warnings"]:
        print(f"warning: {w}")
    print(f"errors: {len(report['errors'])}")


def _emit(report, as_json):
    if as_json:
        print(json.dumps(report, indent=2, sort_keys=True))
    else:
        _print_text(report)
    for err in report["errors"]:
        print(f"error: {err}", file=sys.stderr)


def _emit_rebuild(summary, as_json):
    if as_json:
        print(json.dumps(summary, indent=2, sort_keys=True))
    else:
        print(f"file: {summary['file']} -> {summary['out']} "
              f"({summary['written_bytes']} bytes)")
        print(f"rebuilt: true "
              f"sidx_offset_valid={str(summary['sidx_offset_valid']).lower()} "
              f"output_sidx_offset_valid="
              f"{str(summary['output_sidx_offset_valid']).lower()}")
        for t in summary["tracks"]:
            print(f"track {t['track_id']}: sample_entries="
                  f"{t['sample_entries']} timescale={t['timescale']}")
        print(f"fragments: {len(summary['fragments'])}")
        for f in summary["fragments"]:
            print(f"  [{f['index']}] {f['start_offset']}..{f['end_offset']} "
                  f"({f['byte_length']} bytes)")
    for err in summary["errors"]:
        print(f"error: {err}", file=sys.stderr)


def _analyze_or_none(path):
    """Analyze a file, printing structural/read errors; None on failure."""
    try:
        return validate.analyze(path)
    except StructuralError as exc:
        print(f"error: {exc}", file=sys.stderr)
        return None
    except OSError as exc:
        print(f"error: cannot read {path}: {exc}", file=sys.stderr)
        return None


def cmd_inspect(args):
    analysis = _analyze_or_none(args.file)
    if analysis is None:
        return 2
    report = analysis.report()
    _emit(report, args.json)
    return 0 if not report["errors"] else 3


def cmd_rebuild(args):
    if os.path.abspath(args.file) == os.path.abspath(args.out):
        print("error: --out must differ from the input file; "
              "inputs are read-only", file=sys.stderr)
        return 2
    analysis = _analyze_or_none(args.file)
    if analysis is None:
        return 2
    try:
        plan = repair.plan_rebuild(analysis)
    except repair.RepairError as exc:
        for reason in exc.reasons:
            print(f"error: {reason}", file=sys.stderr)
        return 3
    try:
        written = repair.apply_plan(analysis, plan, args.out)
    except repair.RepairError as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 3
    except OSError as exc:
        print(f"error: cannot write {args.out}: {exc}", file=sys.stderr)
        return 2

    out_analysis = _analyze_or_none(args.out)
    if out_analysis is None:
        return 3  # rebuild produced an unreadable file: treat as failure
    out_report = out_analysis.report()
    summary = {
        "file": args.file,
        "out": args.out,
        "rebuilt": True,
        "sidx_offset_valid": analysis.sidx_offset_valid,
        "output_sidx_offset_valid": out_analysis.sidx_offset_valid,
        "tracks": out_report["tracks"],
        "fragments": out_report["fragments"],
        "written_bytes": written,
        "errors": out_report["errors"],
    }
    _emit_rebuild(summary, args.json)
    return 0 if not out_report["errors"] else 3


def cmd_fixture(args):
    try:
        write_fixture(args.out, corrupt=args.corrupt)
    except OSError as exc:
        print(f"error: cannot write {args.out}: {exc}", file=sys.stderr)
        return 2
    size = os.path.getsize(args.out)
    kind = args.corrupt or "valid"
    print(f"fixture: wrote {args.out} ({size} bytes, {kind})")
    return 0


def main(argv=None):
    parser = argparse.ArgumentParser(
        prog="fragcheck",
        description="Validate and rebuild fMP4 sidx/stbl indexes locally.")
    sub = parser.add_subparsers(dest="command", required=True)

    p_inspect = sub.add_parser("inspect", help="validate indexes, read-only")
    p_inspect.add_argument("file")
    p_inspect.add_argument("--json", action="store_true")
    p_inspect.set_defaults(func=cmd_inspect)

    p_rebuild = sub.add_parser("rebuild", help="rebuild sidx into --out")
    p_rebuild.add_argument("file")
    p_rebuild.add_argument("--out", required=True)
    p_rebuild.add_argument("--json", action="store_true")
    p_rebuild.set_defaults(func=cmd_rebuild)

    p_fixture = sub.add_parser("fixture", help="write a deterministic sample")
    p_fixture.add_argument("--out", required=True)
    p_fixture.add_argument("--corrupt", choices=CORRUPTIONS, default=None)
    p_fixture.set_defaults(func=cmd_fixture)

    args = parser.parse_args(argv)
    return args.func(args)


if __name__ == "__main__":
    sys.exit(main())
