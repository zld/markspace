"""Implementations of the compare/scan/snapshot/pack commands."""

from __future__ import annotations

import argparse
import os
from pathlib import Path
from typing import Sequence

from . import __version__
from .compare import CompareReport, compare_containers
from .inputs import (
    UsageError,
    baseline_for,
    collect_media,
    explicit_baselines,
    relative_path,
)
from .pack import build_manifest
from .report import build_compare_payload, print_summary
from .sniff import sniff_file
from .store import Store, StoreError
from .util import MAX_FILE_SIZE, atomic_write_json, is_within, sha256_file

EXIT_OK = 0
EXIT_MISMATCH = 1
EXIT_USAGE = 2
EXIT_STORAGE = 3


def check_args(args: argparse.Namespace) -> None:
    if getattr(args, "duration_tolerance", 0.0) < 0:
        raise UsageError("--duration-tolerance must not be negative")
    for value in (getattr(args, "json_path", None), getattr(args, "out", None)):
        if value is not None and not str(value).strip():
            raise UsageError("output path must not be empty")


def emit(payload: dict, json_path: str | None) -> None:
    if json_path:
        atomic_write_json(Path(json_path), payload)
    print_summary(payload)


def cmd_compare(args: argparse.Namespace) -> int:
    files, roots = collect_media(args.paths)
    explicit: dict[str, SniffResult] | None = None
    if args.baseline:
        baseline_dir = Path(args.baseline).resolve()
        if not baseline_dir.is_dir():
            raise UsageError(f"--baseline is not a directory: {args.baseline}")
        explicit = explicit_baselines(baseline_dir)

    tolerance_ms = round(float(args.duration_tolerance) * 1000.0, 6)
    report = CompareReport(duration_tolerance_ms=tolerance_ms)
    reused = 0
    with Store(args.db) as store:
        for path in files:
            relative = relative_path(path, roots)
            if not any(is_within(root, path) for root in roots):
                report.errors.append(
                    {"path": relative, "detail": "path escapes the input root"}
                )
                continue
            try:
                size = path.stat().st_size
            except OSError as exc:
                report.errors.append({"path": relative, "detail": str(exc)})
                continue
            if size > MAX_FILE_SIZE:
                report.errors.append(
                    {
                        "path": relative,
                        "detail": f"file exceeds {MAX_FILE_SIZE} byte limit",
                    }
                )
                continue
            try:
                digest = sha256_file(path)
            except OSError as exc:
                report.errors.append(
                    {"path": relative, "detail": f"cannot hash: {exc}"}
                )
                continue
            expected = baseline_for(relative, path, explicit, store)
            expected_id = (
                store.find_scan_by_path(relative) if expected is not None else None
            )
            actual = sniff_file(path)
            stored = store.record_scan(digest, actual)
            reused += 1 if stored.reused else 0
            file_report = compare_containers(
                expected,
                actual,
                path=relative,
                duration_tolerance_ms=tolerance_ms,
            )
            file_report.expected_scan_id = expected_id
            file_report.actual_scan_id = stored.scan_id
            report.files.append(file_report)
            if actual.error:
                report.errors.append({"path": relative, "detail": actual.error})
            else:
                # Only healthy scans become the next run's baseline, so a
                # one-off corrupt file cannot erase a good reference.
                store.remember_path(relative, stored.scan_id)
        run_id = store.record_compare(report) if report.files else None

    payload = build_compare_payload(report)
    payload["summary"]["reused_snapshots"] = reused
    payload["run_id"] = run_id
    emit(payload, args.json_path)
    print(f"result: {'clean' if report.ok else 'inconsistent'}")
    return EXIT_OK if report.ok else EXIT_MISMATCH


def cmd_scan(args: argparse.Namespace) -> int:
    files, roots = collect_media(args.paths)
    records: list[dict] = []
    errors: list[dict] = []
    reused = 0
    with Store(args.db) as store:
        for path in files:
            relative = relative_path(path, roots)
            if not any(is_within(root, path) for root in roots):
                errors.append(
                    {"path": relative, "detail": "path escapes the input root"}
                )
                continue
            try:
                digest = sha256_file(path)
            except OSError as exc:
                errors.append({"path": relative, "detail": f"cannot hash: {exc}"})
                continue
            result = sniff_file(path)
            stored = store.record_scan(digest, result)
            reused += 1 if stored.reused else 0
            records.append(
                {
                    "path": relative,
                    "sha256": digest,
                    "scan_id": stored.scan_id,
                    "reused": stored.reused,
                    **result.to_dict(),
                }
            )
            if result.error:
                errors.append({"path": relative, "detail": result.error})
    payload = {
        "tool": "mediacheck",
        "tool_version": __version__,
        "summary": {
            "files_scanned": len(records),
            "tracks": sum(len(r["tracks"]) for r in records),
            "reused_snapshots": reused,
            "errors": len(errors),
        },
        "files": records,
        "errors": errors,
    }
    emit(payload, args.json_path)
    print(f"result: {'inconsistent' if errors else 'clean'}")
    return EXIT_MISMATCH if errors else EXIT_OK


def cmd_snapshot(args: argparse.Namespace) -> int:
    root = Path(args.path).resolve()
    if not root.is_dir():
        raise UsageError(f"snapshot source is not a directory: {args.path}")
    files, _ = collect_media([str(root)])
    entries = []
    errors = 0
    for path in files:
        relative = path.resolve().relative_to(root).as_posix()
        result = sniff_file(path)
        if result.error:
            errors += 1
        entries.append(
            {
                "path": relative,
                "sha256": sha256_file(path),
                **result.to_dict(),
            }
        )
    payload = {
        "tool": "mediacheck",
        "tool_version": __version__,
        "summary": {
            "files": len(entries),
            "errors": errors,
        },
        "files": entries,
    }
    out = Path(args.out) if args.out else root / "baseline.json"
    atomic_write_json(out, payload)
    print(f"files: {len(entries)}")
    print(f"errors: {errors}")
    print(f"baseline: {out}")
    return EXIT_OK if errors == 0 else EXIT_MISMATCH


def cmd_pack(args: argparse.Namespace) -> int:
    files, _roots = collect_media(args.paths)
    root = pack_root(args.paths, files)
    manifest = build_manifest(root, files)
    payload = manifest.to_dict()
    payload["summary"] = {
        "packed_files": len(manifest.files),
        "rejected_files": len(manifest.rejected),
    }
    atomic_write_json(Path(args.out), payload)
    print(f"packed_files: {len(manifest.files)}")
    print(f"rejected_files: {len(manifest.rejected)}")
    for rejection in manifest.rejected:
        print(f"rejected: {rejection.relative_path}: {rejection.reason}")
    print(f"manifest: {args.out}")
    return EXIT_OK if not manifest.rejected else EXIT_MISMATCH


def pack_root(raw_paths: Sequence[str], files: Sequence[Path]) -> Path:
    """Directory packed paths are recorded relative to."""
    if len(raw_paths) == 1 and Path(raw_paths[0]).is_dir():
        return Path(raw_paths[0]).resolve()
    if not files:
        return Path(".").resolve()
    parents = [str(path.resolve().parent) for path in files]
    return Path(os.path.commonpath(parents))
