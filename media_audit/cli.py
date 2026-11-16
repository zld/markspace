"""Command line entry point: scan, audit, package and re-export.

Exit codes:
  0 - no mismatches and no parse errors
  1 - consistency mismatches found
  2 - invalid arguments or manifest
  3 - at least one file failed to parse, but the batch completed
"""

from __future__ import annotations

import argparse
import os
import sys
from typing import Any

from .compare import (
    AuditResult,
    ManifestError,
    compare_batch,
    load_manifest,
    resolve_manifest_path,
)
from .package import PackageError, package_files, validate_for_packaging
from .probe import SUPPORTED_EXTENSIONS, MediaInfo, probe_file
from .report import build_report, render_terminal, write_json
from .store import AuditStore, StoreError

EXIT_CLEAN = 0
EXIT_MISMATCH = 1
EXIT_USAGE = 2
EXIT_PARSE_FAILURE = 3


def _ensure_outside_root(path: str, root: str, label: str) -> str:
    target = os.path.abspath(path)
    abs_root = os.path.abspath(root)
    if target == abs_root or target.startswith(abs_root + os.sep):
        raise argparse.ArgumentTypeError(
            f"{label} path {path} must not be inside the input root"
        )
    return target


def scan_directory(root: str) -> tuple[list[str], list[dict[str, str]]]:
    """Walk ``root`` deterministically, separating media from unknown files."""
    media: list[str] = []
    skipped: list[dict[str, str]] = []
    for dirpath, dirnames, filenames in os.walk(root):
        dirnames.sort()
        for filename in sorted(filenames):
            full = os.path.join(dirpath, filename)
            rel = os.path.relpath(full, root)
            ext = os.path.splitext(filename)[1].lower()
            if ext in SUPPORTED_EXTENSIONS:
                media.append(full)
            else:
                skipped.append(
                    {
                        "path": rel,
                        "kind": "unknown_extension",
                        "reason": f"extension {ext or '<none>'} is not supported",
                    }
                )
    return media, skipped


def run_audit(args: argparse.Namespace) -> int:
    input_root = os.path.abspath(args.input)
    if not os.path.isdir(input_root):
        print(f"error: input directory not found: {args.input}", file=sys.stderr)
        return EXIT_USAGE
    try:
        manifest_path = resolve_manifest_path(input_root, args.manifest)
        db_path = _ensure_outside_root(args.db, input_root, "database")
        json_path = (
            _ensure_outside_root(args.json, input_root, "json report")
            if args.json
            else None
        )
        manifest = load_manifest(manifest_path)
    except (ManifestError, argparse.ArgumentTypeError) as exc:
        print(f"error: {exc}", file=sys.stderr)
        return EXIT_USAGE
    if args.duration_tolerance_ms < 0:
        print("error: --duration-tolerance-ms must be non-negative",
              file=sys.stderr)
        return EXIT_USAGE

    try:
        store = AuditStore(db_path)
    except StoreError as exc:
        print(f"error: {exc}", file=sys.stderr)
        return EXIT_USAGE

    with store:
        batch_id = store.start_batch(
            input_root, os.path.abspath(manifest_path),
            args.duration_tolerance_ms,
        )
        media_paths, skipped = scan_directory(input_root)
        for event in skipped:
            store.record_event(batch_id, event["path"], event["kind"],
                               event["reason"])

        infos: list[MediaInfo] = []
        errors: list[dict[str, Any]] = []
        for path in media_paths:
            rel = os.path.relpath(path, input_root)
            if not os.path.exists(path):  # vanished between scan and probe
                errors.append({
                    "file": rel,
                    "error": "file disappeared during scan",
                    "offset": None,
                })
                store.record_event(batch_id, rel, "vanished",
                                   "file disappeared during scan")
                continue
            info = probe_file(path)
            info.name = rel  # keep rel paths stable in reports
            try:
                store.save_probe(batch_id, rel, info)
            except StoreError as exc:
                errors.append({"file": rel, "error": str(exc), "offset": None})
                continue
            infos.append(info)
            if not info.ok:
                errors.append({
                    "file": rel,
                    "error": info.parse_error,
                    "offset": info.error_offset,
                })

        infos_by_name = {os.path.relpath(i.path, input_root): i for i in infos}
        # Manifest members may be plain file names or root-relative paths;
        # normalise them all to the relative keys used by the probe results.
        basename_index = {os.path.basename(key): key for key in infos_by_name}

        def _resolve_member(name: str) -> str:
            if name in infos_by_name:
                return name
            return basename_index.get(name, name)

        for group in manifest["groups"]:
            group["files"] = [_resolve_member(n) for n in group["files"]]
        audit: AuditResult = compare_batch(
            infos_by_name, manifest, args.duration_tolerance_ms
        )
        audit.skipped = skipped
        store.finish_batch(
            batch_id,
            "completed_with_errors" if errors else "completed",
        )

    payload = build_report(
        infos, audit, args.duration_tolerance_ms, errors
    )
    print(render_terminal(infos, audit, errors, args.duration_tolerance_ms))
    if json_path:
        try:
            write_json(json_path, payload, input_root)
        except (OSError, ValueError) as exc:
            print(f"error: cannot write JSON report: {exc}", file=sys.stderr)
            return EXIT_USAGE
    if errors:
        return EXIT_PARSE_FAILURE
    if audit.mismatches:
        return EXIT_MISMATCH
    return EXIT_CLEAN


def run_export(args: argparse.Namespace) -> int:
    """Re-export a JSON report from a previous batch stored in SQLite."""
    try:
        with AuditStore(args.db) as store:
            batch_id = store.find_batch(
                os.path.abspath(args.input), os.path.abspath(args.manifest)
            )
            if batch_id is None:
                print("error: no matching batch found in database",
                      file=sys.stderr)
                return EXIT_USAGE
            infos_map = store.load_infos(batch_id)
            events = store.load_events(batch_id)
            manifest = load_manifest(
                resolve_manifest_path(args.input, args.manifest)
            )
            tolerance = store.conn.execute(
                "SELECT tolerance_ms FROM batches WHERE id = ?", (batch_id,)
            ).fetchone()["tolerance_ms"]
    except (StoreError, ManifestError) as exc:
        print(f"error: {exc}", file=sys.stderr)
        return EXIT_USAGE

    infos = list(infos_map.values())
    audit = compare_batch(infos_map, manifest, tolerance)
    audit.skipped = [e for e in events if e["kind"] == "unknown_extension"]
    errors = [
        {
            "file": info.name,
            "error": info.parse_error,
            "offset": info.error_offset,
        }
        for info in sorted(infos, key=lambda item: item.name)
        if not info.ok
    ]
    payload = build_report(infos, audit, tolerance, errors)
    try:
        write_json(args.json, payload, args.input)
    except (OSError, ValueError) as exc:
        print(f"error: {exc}", file=sys.stderr)
        return EXIT_USAGE
    print(f"exported {len(infos)} files to {args.json}")
    return EXIT_CLEAN


def run_package(args: argparse.Namespace) -> int:
    input_root = os.path.abspath(args.input)
    if not os.path.isdir(input_root):
        print(f"error: input directory not found: {args.input}", file=sys.stderr)
        return EXIT_USAGE
    try:
        dest = _ensure_outside_root(args.dest, input_root, "package destination")
    except argparse.ArgumentTypeError as exc:
        print(f"error: {exc}", file=sys.stderr)
        return EXIT_USAGE
    media_paths, _ = scan_directory(input_root)
    try:
        result = package_files(media_paths, dest, input_root)
    except PackageError as exc:
        offset = f" at offset {exc.offset}" if exc.offset is not None else ""
        print(f"error: packaging rejected ({exc.file}): {exc}{offset}",
              file=sys.stderr)
        return EXIT_PARSE_FAILURE
    print(
        f"packaged {len(result.files)} file(s) into {result.dest}; "
        f"summary at {result.manifest_path}"
    )
    return EXIT_CLEAN


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="media-audit",
        description="Offline MP4/MOV/MKV metadata audit and packaging tool",
    )
    sub = parser.add_subparsers(dest="command", required=True)

    audit_p = sub.add_parser("audit", help="scan, probe and audit a directory")
    audit_p.add_argument("--input", required=True, help="input media root")
    audit_p.add_argument("--manifest", required=True, help="manifest JSON path")
    audit_p.add_argument("--db", required=True, help="SQLite database path")
    audit_p.add_argument("--json", help="optional JSON report output path")
    audit_p.add_argument(
        "--duration-tolerance-ms", type=int, default=0,
        help="allowed per-group duration spread in milliseconds",
    )
    audit_p.set_defaults(func=run_audit)

    export_p = sub.add_parser(
        "export", help="re-export a JSON report from a stored batch"
    )
    export_p.add_argument("--input", required=True)
    export_p.add_argument("--manifest", required=True)
    export_p.add_argument("--db", required=True)
    export_p.add_argument("--json", required=True)
    export_p.set_defaults(func=run_export)

    pkg_p = sub.add_parser(
        "package", help="validate via probe and copy files into a package"
    )
    pkg_p.add_argument("--input", required=True)
    pkg_p.add_argument("--dest", required=True)
    pkg_p.set_defaults(func=run_package)
    return parser


def main(argv: list[str] | None = None) -> int:
    parser = build_parser()
    args = parser.parse_args(argv)
    return args.func(args)


if __name__ == "__main__":
    raise SystemExit(main())
