"""Batch JSONL export of committed detection records (UTF-8, stable order)."""
from __future__ import annotations

import json
import os


class ExportError(Exception):
    """Global export failure (exit code 2)."""


def export_jsonl(store, out_path: str):
    """Write one JSON object per file/clip/silence, sorted by (path, idx).

    Returns (files_written, clips_total). Refuses to overwrite any file
    that is itself an indexed input.
    """
    ok_files = store.ok_files()
    resolved = os.path.realpath(out_path)
    for row in ok_files:
        if os.path.realpath(row["path"]) == resolved:
            raise ExportError(
                f"export path would overwrite input file: {out_path}")

    lines = []
    clips_total = 0
    for row in ok_files:
        params = json.loads(row["params_json"]) if row["params_json"] else {}
        lines.append({
            "type": "file",
            "path": row["path"],
            "sample_rate": row["sample_rate"],
            "channels": row["channels"],
            "bit_depth": row["bit_depth"],
            "n_samples": row["n_samples"],
            "params": params,
            "clip_count": 0,
            "silence_total_ms": row["silence_total_ms"] or 0.0,
        })
        clips = store.clips_of(row["id"])
        lines[-1]["clip_count"] = len(clips)
        for c in clips:
            lines.append({
                "type": "clip",
                "path": row["path"],
                "index": c["idx"],
                "start_sample": c["start_sample"],
                "end_sample": c["end_sample"],
            })
        for s in store.silences_of(row["id"]):
            lines.append({
                "type": "silence",
                "path": row["path"],
                "index": s["idx"],
                "start_sample": s["start_sample"],
                "end_sample": s["end_sample"],
            })
        clips_total += len(clips)

    try:
        with open(out_path, "w", encoding="utf-8", newline="\n") as fh:
            for obj in lines:
                fh.write(json.dumps(obj, ensure_ascii=False, sort_keys=True) + "\n")
    except OSError as exc:
        raise ExportError(f"cannot write export file {out_path}: {exc}") from exc
    return len(ok_files), clips_total
