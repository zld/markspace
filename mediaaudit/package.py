"""Capture archive builder.

Runs only after the inputs passed verification (the caller reuses the
same extraction and comparison pipeline). The ZIP has sorted entries and
fixed timestamps for reproducibility, and embeds a JSON manifest. The
archive is written to a temporary file and atomically moved into place;
on failure no partial archive is left behind.
"""
from __future__ import annotations

import json
import os
import zipfile

_EPOCH = (1980, 1, 1, 0, 0, 0)


def _zip_info(name: str) -> zipfile.ZipInfo:
    info = zipfile.ZipInfo(name, date_time=_EPOCH)
    info.compress_type = zipfile.ZIP_DEFLATED
    info.external_attr = 0o644 << 16
    return info


def package_manifest(report_dict: dict) -> dict:
    return {
        "show_id": report_dict["show_id"],
        "generated_by": "mediaaudit",
        "files": [
            {
                "path": pf["path"],
                "container": pf.get("container", ""),
                "sha256": pf.get("sha256", ""),
                "size": pf.get("size", 0),
                "tracks": pf["tracks"],
            }
            for pf in report_dict["per_file"]
        ],
    }


def build_archive(out_path: str, base_dir: str, rel_files: list,
                  report_dict: dict) -> None:
    out_dir = os.path.dirname(os.path.abspath(out_path))
    os.makedirs(out_dir, exist_ok=True)
    tmp = os.path.join(out_dir, f".{os.path.basename(out_path)}.tmp{os.getpid()}")
    try:
        with zipfile.ZipFile(tmp, "w") as zf:
            for rel in sorted(rel_files):
                with open(os.path.join(base_dir, rel), "rb") as fh:
                    zf.writestr(_zip_info(rel), fh.read())
            payload = json.dumps(package_manifest(report_dict),
                                 indent=2, sort_keys=True) + "\n"
            zf.writestr(_zip_info("manifest.json"), payload)
        os.replace(tmp, out_path)
    except BaseException:
        try:
            os.unlink(tmp)
        except OSError:
            pass
        raise
