"""Candidate-task scheduler with bounded worker threads.

Each worker owns its SQLite connection, claims tasks inside
transactions and writes results idempotently. ``stop_after`` bounds
how many tasks a single run completes so interrupted scans can be
resumed later with the same fingerprint.
"""
from __future__ import annotations

import threading
from dataclasses import dataclass, field

from .codec import Params
from .fixture import Clip
from .metrics import evaluate_params
from . import store

MAX_WORKERS = 8


@dataclass
class ScanStats:
    planned: int = 0
    done: int = 0
    failed: int = 0
    completed_this_run: int = 0
    reclaimed: int = 0
    errors: dict[str, str] = field(default_factory=dict)

    @property
    def complete(self) -> bool:
        return self.planned > 0 and self.done == self.planned and self.failed == 0


def run_scan(
    db_path: str,
    clips: list[Clip],
    params: list[Params],
    workers: int = 2,
    stop_after: int | None = None,
) -> ScanStats:
    """Process pending tasks until none remain or ``stop_after`` hits."""
    if not 1 <= workers <= MAX_WORKERS:
        raise ValueError(f"workers must be in 1..{MAX_WORKERS}")
    by_key = {p.key: p for p in params}
    stats = ScanStats()
    lock = threading.Lock()
    stop = threading.Event()
    started = 0  # claims issued this run; gates stop_after exactly

    def worker() -> None:
        nonlocal started
        conn = store.connect(db_path)
        try:
            while not stop.is_set():
                with lock:
                    if stop_after is not None and started >= stop_after:
                        stop.set()
                        return
                    key = store.claim_task(conn)
                    if key is not None:
                        started += 1
                if key is None:
                    return
                try:
                    result = evaluate_params(clips, by_key[key])
                except Exception as exc:  # recorded, not raised
                    store.fail_task(conn, key, f"{type(exc).__name__}: {exc}")
                    with lock:
                        stats.errors[key] = str(exc)
                else:
                    store.complete_task(conn, result)
                finally:
                    with lock:
                        stats.completed_this_run += 1
        finally:
            conn.close()

    threads = [threading.Thread(target=worker, daemon=True) for _ in range(workers)]
    for t in threads:
        t.start()
    for t in threads:
        t.join()

    conn = store.connect(db_path)
    try:
        counts = store.task_counts(conn)
    finally:
        conn.close()
    stats.planned = counts["total"]
    stats.done = counts["done"]
    stats.failed = counts["failed"]
    return stats
