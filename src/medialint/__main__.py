"""medialint command line: fixtures, audit, package.

Exit codes: 0 = all checks passed, 2 = media inconsistencies found,
3 = parameter, manifest or storage error.
"""
from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path

from .catalog import Catalog
from .compare import compare_asset, load_manifest
from .fixtures import FAULTS, write_bundle
from .model import ManifestError, StorageError
from .package import run_package
from .sniff import extract_many
from .util import atomic_write_text, safe_join


def _build_parser() -> argparse.ArgumentParser:
    p = argparse.ArgumentParser(prog="medialint",
                                description="Local media delivery linter: sniff, extract, compare, package.")
    sub = p.add_subparsers(dest="command", required=True)

    f = sub.add_parser("fixtures", help="generate sample media and a delivery manifest")
    f.add_argument("--out", required=True, help="output directory for the bundle")
    f.add_argument("--inject", default="",
                   help=f"comma-separated faults to inject ({', '.join(FAULTS)})")

    a = sub.add_parser("audit", help="extract media metadata and compare against a manifest")
    a.add_argument("--manifest", required=True, help="delivery manifest (bundle.json)")
    a.add_argument("--db", required=True, help="SQLite catalog path")
    a.add_argument("--json", help="write the audit report to this JSON file")
    a.add_argument("--tolerance-ms", type=int, default=80, help="duration tolerance in ms (default 80)")

    pk = sub.add_parser("package", help="package assets that pass verification")
    pk.add_argument("--manifest", required=True, help="delivery manifest (bundle.json)")
    pk.add_argument("--db", required=True, help="SQLite catalog path")
    pk.add_argument("--out", required=True, help="output directory for the package")
    pk.add_argument("--tolerance-ms", type=int, default=80, help="duration tolerance in ms (default 80)")
    return p


def _cmd_fixtures(args) -> int:
    inject = [s.strip() for s in args.inject.split(",") if s.strip()]
    try:
        written = write_bundle(Path(args.out), inject)
    except ValueError as e:
        print(f"error: {e}", file=sys.stderr)
        return 3
    for p in written:
        print(f"wrote {p}")
    if inject:
        print(f"injected faults: {', '.join(inject)}")
    return 0


def _cmd_audit(args) -> int:
    if args.tolerance_ms < 0:
        print("error: --tolerance-ms must be >= 0", file=sys.stderr)
        return 3
    manifest_path = Path(args.manifest)
    doc = load_manifest(manifest_path)  # ManifestError -> exit 3 (handled in main)
    root = manifest_path.resolve().parent
    assets = {a["path"]: a for a in doc["assets"]}
    rels = sorted(assets)
    for rel in rels:  # confine every path before any extraction or JSON output
        safe_join(root, rel)

    results = extract_many(root, rels)

    asset_reports = []
    all_issues = []
    max_delta = 0
    track_count = 0
    summaries: dict[str, None] = {}
    catalog_results = []
    for rel in rels:
        media, problems = results[rel]
        issues, delta = compare_asset(rel, media, problems, assets[rel], args.tolerance_ms)
        max_delta = max(max_delta, delta)
        tracks = []
        if media is not None:
            track_count += len(media.tracks)
            for t in media.tracks:
                summaries.setdefault(t.codec.summary())
                tracks.append(t.to_dict())
        asset_reports.append({
            "path": rel,
            "format": media.format if media else None,
            "sha256": media.sha256 if media else None,
            "size_bytes": media.size_bytes if media else None,
            "track_count": len(tracks),
            "tracks": tracks,
            "inconsistencies": [i.to_dict() for i in issues],
        })
        catalog_results.append({
            "path": rel,
            "media": {**media.to_dict(), "tracks": tracks} if media else None,
            "inconsistencies": issues,
        })
        all_issues.extend(issues)

    catalog = Catalog(args.db)  # StorageError -> exit 3
    try:
        catalog.record_scan(str(manifest_path.resolve()), args.tolerance_ms, catalog_results)
    finally:
        catalog.close()

    codec_summary = list(summaries)
    report = {
        "manifest": str(manifest_path),
        "tolerance_ms": args.tolerance_ms,
        "ok": not all_issues,
        "track_count": track_count,
        "max_duration_delta_ms": max_delta,
        "codec_summary": codec_summary,
        "inconsistencies": [i.to_dict() for i in all_issues],
        "assets": asset_reports,
    }

    print(f"track_count: {track_count}")
    print(f"max_duration_delta_ms: {max_delta}")
    print("codec_summary: " + "; ".join(codec_summary))
    if all_issues:
        print(f"inconsistencies: {len(all_issues)}")
        for i in all_issues:
            loc = f" [{i.track}]" if i.track else ""
            off = f" @0x{i.offset:x}" if i.offset is not None else ""
            print(f"  - [{i.code}] {i.path}{loc}{off}: {i.message}")
    else:
        print("inconsistencies: []")

    if args.json:
        atomic_write_text(Path(args.json), json.dumps(report, indent=2) + "\n")
        print(f"wrote {args.json}")
    return 0 if not all_issues else 2


def main(argv: list[str] | None = None) -> int:
    args = _build_parser().parse_args(argv)
    try:
        if args.command == "fixtures":
            return _cmd_fixtures(args)
        if args.command == "audit":
            return _cmd_audit(args)
        if args.command == "package":
            return run_package(Path(args.manifest), args.db, Path(args.out), args.tolerance_ms)
    except (ManifestError, StorageError) as e:
        print(f"error: {e}", file=sys.stderr)
        return 3
    return 3


if __name__ == "__main__":
    sys.exit(main())
