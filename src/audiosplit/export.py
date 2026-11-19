"""CSV/JSON export of stored detection results.

Outputs are written to a temporary file in the destination directory and then
atomically renamed, so a crashed export never leaves a half-written artifact.
Coordinates come straight from the database, guaranteeing that exported
segments, silences and persisted records are identical.
"""
from __future__ import annotations

import csv
import json
import os
import tempfile
from pathlib import Path

from .store import Store


def _atomic_write(path: Path, write_fn) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    fd, tmp_name = tempfile.mkstemp(
        prefix=f".{path.name}.", suffix=".tmp", dir=str(path.parent)
    )
    try:
        with os.fdopen(fd, "w", newline="", encoding="utf-8") as handle:
            write_fn(handle)
        os.replace(tmp_name, path)
    except BaseException:
        os.unlink(tmp_name)
        raise


def _run_payload(store: Store, run_id: int) -> dict:
    grouped = store.load_run(run_id)
    files = []
    for rel_path in sorted(grouped):
        entry = grouped[rel_path]
        files.append(
            {
                "file": rel_path,
                "status": entry["status"],
                "error": entry["error"],
                "segments": entry["segments"],
                "silences": entry["silences"],
            }
        )
    return {"run_id": run_id, "files": files}


def export_json(store: Store, run_id: int, out_path: str | Path) -> Path:
    payload = _run_payload(store, run_id)
    target = Path(out_path)

    def write(handle) -> None:
        json.dump(payload, handle, indent=2, sort_keys=True)
        handle.write("\n")

    _atomic_write(target, write)
    return target


def export_csv(store: Store, run_id: int, out_path: str | Path) -> Path:
    payload = _run_payload(store, run_id)
    target = Path(out_path)

    def write(handle) -> None:
        writer = csv.writer(handle)
        writer.writerow(
            ["file", "kind", "idx", "start_sample", "end_sample", "status", "error"]
        )
        for entry in payload["files"]:
            if entry["status"] == "failed":
                writer.writerow(
                    [entry["file"], "file", "", "", "", "failed", entry["error"]]
                )
                continue
            for i, seg in enumerate(entry["segments"]):
                writer.writerow([
                    entry["file"], "segment", i,
                    seg["start_sample"], seg["end_sample"], "ok", "",
                ])
            for i, gap in enumerate(entry["silences"]):
                writer.writerow([
                    entry["file"], "silence", i,
                    gap["start_sample"], gap["end_sample"], "ok", "",
                ])

    _atomic_write(target, write)
    return target


def export(store: Store, run_id: int, out_path: str | Path) -> Path:
    """Dispatch on extension; .csv or .json (default JSON)."""
    target = Path(out_path)
    if target.suffix.lower() == ".csv":
        return export_csv(store, run_id, target)
    return export_json(store, run_id, target)
