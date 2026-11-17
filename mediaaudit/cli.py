"""Command line interface: argument parsing and subcommand dispatch."""
from __future__ import annotations

import argparse
import json
import os
import sqlite3
import sys

from . import compare, package, sniff, store

EXIT_OK = 0
EXIT_MISMATCH = 1
EXIT_FATAL = 2


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="mediaaudit",
        description="Verify capture media sets against a show manifest.",
    )
    sub = parser.add_subparsers(dest="command", required=True)

    audit = sub.add_parser("audit", help="extract metadata and compare against the manifest")
    audit.add_argument("--manifest", required=True, help="path to the show manifest JSON")
    audit.add_argument("--db", required=True, help="SQLite database path")
    audit.add_argument("--json", dest="json_out", help="write the JSON report here")
    audit.add_argument("--workers", type=int, default=1, help="parallel extraction workers")
    audit.add_argument("--max-bytes", type=int, default=None, help="per-file size limit")

    pack = sub.add_parser("pack", help="verify inputs, then build a capture archive")
    pack.add_argument("--manifest", required=True, help="path to the show manifest JSON")
    pack.add_argument("--db", required=True, help="SQLite database path")
    pack.add_argument("--out", required=True, help="output ZIP archive path")
    pack.add_argument("--workers", type=int, default=1, help="parallel extraction workers")
    pack.add_argument("--max-bytes", type=int, default=None, help="per-file size limit")
    return parser


def _print_report(report) -> None:
    print(f"show_id: {report.show_id}")
    print(f"status: {report.status}")
    print(f"files: {report.files}")
    print(f"tracks: {report.tracks}")
    print("track_count: " + " ".join(
        f"{k}={v}" for k, v in report.track_count.items()))
    print("duration_delta_ms: " + " ".join(
        f"{k}={v}" for k, v in report.duration_delta_ms.items()))
    print(f"max_duration_delta_ms: {report.max_duration_delta_ms}")
    print("codec_summary: " + " ".join(
        f"{k}={','.join(v)}" for k, v in report.codec_summary.items()))
    print(f"mismatches: {len(report.mismatches)}")
    for issue in report.mismatches:
        role = f" role={issue.role}" if issue.role else ""
        print(f"  [{issue.kind}] {issue.path}{role}: {issue.detail}")


def _write_json_atomic(path: str, payload: dict) -> None:
    tmp = f"{path}.tmp{os.getpid()}"
    with open(tmp, "w", encoding="utf-8") as fh:
        json.dump(payload, fh, indent=2, sort_keys=True)
        fh.write("\n")
    os.replace(tmp, path)


def _prepare(args, kind: str):
    """Shared pipeline: load manifest, extract, compare, persist the run."""
    try:
        manifest = compare.load_manifest(args.manifest)
    except compare.ManifestError as exc:
        print(f"error: {exc}", file=sys.stderr)
        return None
    manifest_dir = os.path.dirname(os.path.abspath(args.manifest))
    max_bytes = args.max_bytes or sniff.DEFAULT_MAX_BYTES
    records = compare.extract_all(manifest_dir, manifest,
                                  workers=max(1, args.workers),
                                  max_bytes=max_bytes)
    report = compare.compare_records(records, manifest)
    try:
        db = store.Store(args.db)
        try:
            db.save_run(kind, report, records)
        finally:
            db.close()
    except (store.StoreError, sqlite3.Error, OSError) as exc:
        print(f"error: database: {exc}", file=sys.stderr)
        return None
    return manifest, manifest_dir, records, report


def _cmd_audit(args) -> int:
    prepared = _prepare(args, "audit")
    if prepared is None:
        return EXIT_FATAL
    _, _, _, report = prepared
    _print_report(report)
    if args.json_out:
        try:
            _write_json_atomic(args.json_out, report.to_dict())
        except OSError as exc:
            print(f"error: cannot write JSON report: {exc}", file=sys.stderr)
            return EXIT_FATAL
        print(f"report: {args.json_out}")
    return EXIT_MISMATCH if report.mismatches else EXIT_OK


def _cmd_pack(args) -> int:
    prepared = _prepare(args, "pack")
    if prepared is None:
        return EXIT_FATAL
    manifest, manifest_dir, _, report = prepared
    _print_report(report)
    if report.mismatches:
        print("pack aborted: input verification failed", file=sys.stderr)
        return EXIT_MISMATCH
    try:
        package.build_archive(args.out, manifest_dir, manifest["files"],
                              report.to_dict())
    except OSError as exc:
        print(f"error: cannot write archive: {exc}", file=sys.stderr)
        return EXIT_FATAL
    print(f"archive: {args.out}")
    return EXIT_OK


def main(argv=None) -> int:
    args = build_parser().parse_args(argv)
    if args.command == "audit":
        return _cmd_audit(args)
    return _cmd_pack(args)
