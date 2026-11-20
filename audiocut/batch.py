"""Concurrent batch scheduling.

The main thread scans the input directory, hashes each file, checks
for a reusable duplicate result, and feeds the rest into a bounded
work queue. Worker threads decode and detect with the same detector
used for single files. All SQLite writes happen on the main thread as
results come back, one transaction per file, so workers never touch
the database and an exception can never leave half a file's intervals.
"""
from __future__ import annotations

import hashlib
import os
import queue
import threading
from dataclasses import dataclass, field
from typing import List, Optional

from . import DetectionResult, DetectorParams, Segment
from .detector import detect_file
from .store import Store
from .wav_reader import WavError

_HASH_BLOCK = 1 << 20


@dataclass
class FileOutcome:
    relpath: str
    sha256: Optional[str] = None
    status: str = "ok"  # "ok" | "error"
    error: Optional[str] = None
    result: Optional[DetectionResult] = None


@dataclass
class BatchSummary:
    run_id: int
    files_seen: int = 0
    succeeded: int = 0
    failed: int = 0
    reused: int = 0
    skipped: int = 0
    clip_count: int = 0
    silence_total_seconds: float = 0.0
    errors: List[str] = field(default_factory=list)

    def line(self) -> str:
        return (
            f"run_id={self.run_id} files_seen={self.files_seen} "
            f"succeeded={self.succeeded} failed={self.failed} "
            f"reused={self.reused} skipped={self.skipped} "
            f"clip_count={self.clip_count} "
            f"silence_total_seconds={self.silence_total_seconds:.2f}"
        )


def scan_wavs(input_dir: str) -> List[str]:
    """Relative paths of WAV files under input_dir, sorted, no symlinks.

    Raises FileNotFoundError if the directory does not exist and
    ValueError if it contains no WAV files at all.
    """
    if not os.path.isdir(input_dir):
        raise FileNotFoundError(f"input directory not found: {input_dir}")
    root = os.path.realpath(input_dir)
    found: List[str] = []
    for dirpath, dirnames, filenames in os.walk(root, followlinks=False):
        dirnames[:] = sorted(d for d in dirnames if not os.path.islink(
            os.path.join(dirpath, d)))
        for name in filenames:
            full = os.path.join(dirpath, name)
            if os.path.islink(full) or not name.lower().endswith(".wav"):
                continue
            # Belt and braces: never emit a path outside the input root.
            if os.path.realpath(full).startswith(root + os.sep):
                found.append(os.path.relpath(full, root))
    found.sort()
    if not found:
        raise ValueError(f"no WAV files found under {input_dir}")
    return found


def sha256_file(path: str) -> str:
    digest = hashlib.sha256()
    with open(path, "rb") as fh:
        while True:
            block = fh.read(_HASH_BLOCK)
            if not block:
                break
            digest.update(block)
    return digest.hexdigest()


def process_file(full_path: str, relpath: str, sha256: str,
                 params: DetectorParams) -> FileOutcome:
    """Worker-side: decode and detect one file, capturing any failure."""
    try:
        result = detect_file(full_path, params)
        return FileOutcome(relpath=relpath, sha256=sha256, result=result)
    except WavError as exc:
        return FileOutcome(relpath=relpath, sha256=sha256,
                           status="error", error=str(exc))
    except Exception as exc:  # never let one file kill the batch
        return FileOutcome(relpath=relpath, sha256=sha256,
                           status="error", error=f"{type(exc).__name__}: {exc}")


def _worker(work_q: "queue.Queue", result_q: "queue.Queue",
            input_dir: str, params: DetectorParams) -> None:
    while True:
        item = work_q.get()
        try:
            if item is None:
                return
            relpath, sha = item
            result_q.put(
                process_file(os.path.join(input_dir, relpath), relpath, sha, params)
            )
        finally:
            work_q.task_done()


def run_batch(
    store: Store,
    run_id: int,
    input_dir: str,
    params: DetectorParams,
    workers: int = 1,
    resume: bool = False,
) -> BatchSummary:
    """Process every WAV under input_dir into the given run.

    With resume=True, files already recorded in this run are skipped.
    """
    params.validate()
    input_dir = os.path.abspath(input_dir)
    paths = scan_wavs(input_dir)
    done = store.paths_done(run_id) if resume else set()
    digest = params.digest()

    summary = BatchSummary(run_id=run_id, files_seen=len(paths))
    work_q: "queue.Queue" = queue.Queue(maxsize=max(1, workers) * 2)
    result_q: "queue.Queue" = queue.Queue()
    threads = [
        threading.Thread(
            target=_worker, args=(work_q, result_q, input_dir, params), daemon=True
        )
        for _ in range(max(1, workers))
    ]
    for t in threads:
        t.start()

    queued = 0
    try:
        for relpath in paths:
            if relpath in done:
                summary.skipped += 1
                continue
            full = os.path.join(input_dir, relpath)
            sha = sha256_file(full)
            dup = store.find_duplicate(relpath, sha, digest)
            if dup is not None:
                store.record_reused(run_id, relpath, sha, dup)
                summary.reused += 1
                summary.succeeded += 1
                summary.clip_count += dup["clip_count"]
                rate = dup["sample_rate"] or 1
                summary.silence_total_seconds += dup["silence_frames"] / rate
                continue
            work_q.put((relpath, sha))  # blocks when the queue is full
            queued += 1
        for _ in threads:
            work_q.put(None)
        for _ in range(queued):
            outcome = result_q.get()
            _record(store, run_id, outcome, summary)
    except BaseException:
        # Drain nothing; workers are daemons and the run stays
        # resumable because committed files are already on disk.
        raise
    finally:
        for t in threads:
            t.join(timeout=5)
    return summary


def _record(store: Store, run_id: int, outcome: FileOutcome,
            summary: BatchSummary) -> None:
    store.record_file(
        run_id,
        outcome.relpath,
        outcome.sha256,
        outcome.status,
        error=outcome.error,
        result=outcome.result,
    )
    if outcome.status == "ok" and outcome.result is not None:
        summary.succeeded += 1
        summary.clip_count += outcome.result.clip_count
        summary.silence_total_seconds += outcome.result.silence_total_seconds
    else:
        summary.failed += 1
        summary.errors.append(f"{outcome.relpath}: {outcome.error}")
