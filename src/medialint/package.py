"""Packager: re-verify scanned assets and emit a package manifest.

Reuses the same extraction entry point as the audit. Every asset must
still match its catalog hash (a change since the scan refuses packaging)
and pass comparison; only then is the package manifest written, atomically,
so an interrupted run never leaves something that looks like a delivery.
"""
from __future__ import annotations

import json
import shutil
from datetime import datetime, timezone
from pathlib import Path

from .catalog import Catalog
from .compare import compare_asset, load_manifest
from .model import Inconsistency, ManifestError
from .sniff import extract_file
from .util import atomic_write_text, safe_join


def _failures_to_issues(rel: str, error) -> list[Inconsistency]:
    return [Inconsistency(code=error.code, path=rel, message=error.message, offset=error.offset)]


def run_package(manifest_path: Path, db_path: str, out_dir: Path, tolerance_ms: int) -> int:
    """Returns the process exit code: 0 packaged, 2 inconsistencies, 3 refused."""
    from .model import MediaError, StorageError

    doc = load_manifest(manifest_path)
    root = manifest_path.resolve().parent
    manifest_key = str(manifest_path.resolve())
    try:
        catalog = Catalog(db_path)
    except StorageError as e:
        print(f"error: {e}")
        return 3

    issues: list[Inconsistency] = []
    verified = []
    try:
        for asset in doc["assets"]:
            rel = asset["path"]
            record = catalog.lookup_file(manifest_key, rel)
            if record is None:
                print(f"error: {rel}: no scan record in catalog {db_path}; run 'medialint audit' first")
                return 3
            try:
                media, problems = extract_file(root, rel)
            except MediaError as e:
                issues.extend(_failures_to_issues(rel, e))
                continue
            if media.sha256 != record["sha256"]:
                print(f"error: {rel}: file changed since the audit scan (hash mismatch); refusing to package")
                return 3
            asset_issues, _ = compare_asset(rel, media, problems, asset, tolerance_ms)
            issues.extend(asset_issues)
            verified.append((rel, media))
    finally:
        catalog.close()

    if issues:
        print(f"inconsistencies: {len(issues)}")
        for i in issues:
            loc = f" [{i.track}]" if i.track else ""
            off = f" @0x{i.offset:x}" if i.offset is not None else ""
            print(f"  - [{i.code}] {i.path}{loc}{off}: {i.message}")
        print("package refused: resolve inconsistencies and re-audit first")
        return 2

    out_dir.mkdir(parents=True, exist_ok=True)
    entries = []
    for rel, media in verified:
        dest = safe_join(out_dir, rel)
        dest.parent.mkdir(parents=True, exist_ok=True)
        shutil.copy2(safe_join(root, rel), dest)
        entries.append({
            "path": rel,
            "sha256": media.sha256,
            "size_bytes": media.size_bytes,
            "format": media.format,
            "tracks": [t.to_dict() for t in media.tracks],
        })
    package_doc = {
        "package": doc.get("bundle", manifest_path.stem),
        "source_manifest": manifest_key,
        "generated_at": datetime.now(timezone.utc).isoformat(timespec="seconds"),
        "assets": entries,
    }
    atomic_write_text(out_dir / "package.json", json.dumps(package_doc, indent=2) + "\n")
    print(f"packaged {len(entries)} asset(s) -> {out_dir / 'package.json'}")
    return 0
