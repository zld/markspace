"""Batch orchestration: resume state, worker pool and report assembly."""

from __future__ import annotations

import datetime as dt
import json
import threading
from concurrent.futures import ThreadPoolExecutor
from dataclasses import dataclass
from pathlib import Path
from typing import Any

from . import REPORT_SCHEMA, __version__
from .discover import InputFile, check_output_relative, discover, sha256_file
from .errors import OutputError, TaskError
from .params import PipelineParams
from .processing import process_file, remove_file
from .report import write_json_atomic
from .resample import CoefficientCache, Resampler

STATE_SCHEMA = "audiorepro.state/1"


@dataclass
class PipelineResult:
    report: dict[str, Any]
    errors: list[TaskError]

    @property
    def exit_code(self) -> int:
        return 3 if self.errors else 0


class ResumeState:
    """SHA-256 keyed cache of completed entries, keyed by parameter hash too."""

    def __init__(self, path: Path) -> None:
        self.path = path
        self.entries: dict[str, dict[str, Any]] = {}

    def load(self) -> dict[str, dict[str, Any]]:
        try:
            with open(self.path, "r", encoding="utf-8") as handle:
                data = json.load(handle)
        except (OSError, json.JSONDecodeError):
            return {}
        if not isinstance(data, dict) or data.get("schema") != STATE_SCHEMA:
            return {}
        entries = data.get("entries")
        return entries if isinstance(entries, dict) else {}

    def save(self, params_hash: str) -> None:
        write_json_atomic(
            self.path,
            {
                "schema": STATE_SCHEMA,
                "tool_version": __version__,
                "params_hash": params_hash,
                "entries": self.entries,
            },
        )


def resume_candidate(
    entry: dict[str, Any] | None,
    item: InputFile,
    params_hash: str,
    output_dir: Path,
) -> dict[str, Any] | None:
    """Return a prior entry only if source and product are byte-identical."""
    if not entry or entry.get("params_hash") != params_hash:
        return None
    recorded = entry.get("recorded")
    if not isinstance(recorded, dict):
        return None
    target = output_dir / check_output_relative(item.relative_path)
    if not target.is_file():
        return None
    try:
        current_source = sha256_file(item.source)
        current_output = sha256_file(target)
    except OSError:
        return None
    if entry.get("source_sha256") != current_source:
        return None
    if entry.get("output_sha256") != current_output:
        return None
    return recorded


def _discard_stale(item: InputFile, params: PipelineParams, state: ResumeState) -> None:
    """A failed task must not leave a previous run's product or cache entry."""
    state.entries.pop(item.relative_path, None)
    remove_file(params.output_dir / check_output_relative(item.relative_path))


def _report(
    params: PipelineParams,
    params_hash: str,
    files: list[dict[str, Any]],
    errors: list[TaskError],
    resumed: int,
    cache_stats: dict[str, int],
) -> dict[str, Any]:
    channels: dict[str, int] = {}
    for item in files:
        key = str(item["channels"])
        channels[key] = channels.get(key, 0) + 1
    return {
        "schema": REPORT_SCHEMA,
        "tool_version": __version__,
        "generated_at": dt.datetime.now(dt.timezone.utc).replace(microsecond=0).isoformat(),
        "params": params.processing_dict(),
        "params_hash": params_hash,
        "summary": {
            "files": len(files),
            "failed": len(errors),
            "resumed": resumed,
            "sample_rate": params.sample_rate,
            "channels": dict(sorted(channels.items())),
            "coefficient_cache": cache_stats,
            "peak_ceiling_dbfs": params.peak_dbfs,
            "target_lufs": params.target_lufs,
        },
        "files": files,
        "errors": [error.as_dict() for error in errors],
    }


def run_pipeline(params: PipelineParams, report_path: Path) -> PipelineResult:
    """Discover, process in parallel, resume what is unchanged, then report."""
    try:
        params.output_dir.mkdir(parents=True, exist_ok=True)
    except OSError as exc:
        raise OutputError(f"cannot create output directory {params.output_dir}: {exc}") from exc

    discovery = discover(params.input_dir)
    errors: list[TaskError] = list(discovery.errors)
    params_hash = params.params_hash()
    state = ResumeState(params.output_dir / ".audiorepro-state.json")
    cached_entries = state.load() if params.resume else {}
    cache = CoefficientCache(params.resampler)
    resampler = Resampler(cache)
    results: dict[str, dict[str, Any]] = {}
    counters = {"resumed": 0}
    lock = threading.Lock()

    def work(item: InputFile) -> None:
        cached = resume_candidate(
            cached_entries.get(item.relative_path), item, params_hash, params.output_dir
        )
        if cached is not None:
            with lock:
                results[item.relative_path] = cached
                counters["resumed"] += 1
                state.entries[item.relative_path] = {
                    "params_hash": params_hash,
                    "source_sha256": cached.get("source_sha256"),
                    "output_sha256": cached.get("output_sha256"),
                    "recorded": cached,
                }
            return
        try:
            entry = process_file(item, params, resampler)
        except TaskError as exc:
            _record_failure(item, exc)
            return
        except Exception as exc:  # defensive: a single task never aborts the batch
            _record_failure(item, TaskError(item.relative_path, "internal", repr(exc)))
            return
        with lock:
            results[item.relative_path] = entry
            state.entries[item.relative_path] = {
                "params_hash": params_hash,
                "source_sha256": entry["source_sha256"],
                "output_sha256": entry["output_sha256"],
                "recorded": entry,
            }

    def _record_failure(item: InputFile, exc: TaskError) -> None:
        with lock:
            _discard_stale(item, params, state)
            errors.append(exc)

    if params.jobs > 1 and len(discovery.files) > 1:
        with ThreadPoolExecutor(max_workers=params.jobs) as pool:
            list(pool.map(work, discovery.files))
    else:
        for item in discovery.files:
            work(item)

    files = [results[key] for key in sorted(results)]
    errors.sort(key=lambda e: (e.path, e.stage, e.reason))
    report = _report(params, params_hash, files, errors, counters["resumed"], cache.stats())
    state.save(params_hash)
    write_json_atomic(report_path, report)
    return PipelineResult(report=report, errors=errors)
