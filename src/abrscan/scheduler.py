"""Concurrent, resumable task scheduling.

Each worker thread owns a SQLite connection and repeatedly claims one task
inside a short ``BEGIN IMMEDIATE`` transaction.  Encoding and metric
computation happen outside any transaction, so claims never block for the
duration of a clip.  Completed measurements are written atomically with the
state transition to ``done``.
"""

from __future__ import annotations

import sqlite3
import threading
import time
from concurrent.futures import ThreadPoolExecutor
from dataclasses import dataclass, field
from typing import Sequence

from .codec import decode_clip, encode_clip, payload_digest
from .fixture import Fixture
from .metrics import compute_metrics
from .store import (
    DONE,
    FAILED,
    PENDING,
    RUNNING,
    Measurement,
    RunInfo,
    Store,
    StoreError,
    Task,
)

CODEC_TAG = "abrscan-blockdct-v1"
LEASE_SECONDS = 60.0


def make_task_key(content_hash: str, quant_step: int, block_size: int) -> str:
    """Stable unique key: identical parameters never yield a second task."""
    return f"{CODEC_TAG}:{content_hash[:20]}:q{quant_step:03d}:b{block_size:02d}"


@dataclass(frozen=True)
class Failure:
    task_key: str
    quant_step: int
    block_size: int
    error: str


@dataclass
class ScanOutcome:
    run: RunInfo
    planned: int
    inserted: int
    done_before: int
    completed_now: int
    resumed: bool
    interrupted: bool
    counts: dict[str, int]
    failures: list[Failure] = field(default_factory=list)

    @property
    def incomplete(self) -> int:
        return self.counts.get(PENDING, 0) + self.counts.get(RUNNING, 0) + self.counts.get(
            FAILED, 0
        )


def measure_task(fixture: Fixture, task: Task) -> Measurement:
    """Encode, decode and score one parameter task."""
    clip = encode_clip(
        fixture.frames, task.quant_step, task.block_size, fixture.fps
    )
    decoded = decode_clip(clip)
    measured = compute_metrics(
        fixture.frames, decoded, clip.encoded_bytes, fixture.fps
    )
    return Measurement(
        task_key=task.task_key,
        quant_step=task.quant_step,
        block_size=task.block_size,
        encoded_bytes=measured.encoded_bytes,
        actual_kbps=measured.actual_kbps,
        quality_db=measured.quality_db,
        payload_sha256=payload_digest(clip.payload),
    )


class _Progress:
    def __init__(self, stop_after: int | None):
        self.lock = threading.Lock()
        self.completed = 0
        self.failures: list[Failure] = []
        self.stopped_early = False
        self.stop_after = stop_after


def _worker_loop(
    store: Store,
    run: RunInfo,
    fixture: Fixture,
    name: str,
    progress: _Progress,
    stop_event: threading.Event,
    lease_seconds: float,
) -> None:
    conn = store.connect()
    lock_retries = 0
    try:
        while not stop_event.is_set():
            with progress.lock:
                reached = (
                    progress.stop_after is not None
                    and progress.completed >= progress.stop_after
                )
                if reached:
                    progress.stopped_early = True
            if reached:
                stop_event.set()
                return
            try:
                task = store.claim_next(
                    conn, run.run_id, name, lease_seconds=lease_seconds
                )
                lock_retries = 0
            except sqlite3.OperationalError as exc:
                lock_retries += 1
                if lock_retries >= 100:
                    raise StoreError(f"{name} could not claim work: {exc}") from exc
                time.sleep(0.05)
                continue
            if task is None:
                return
            try:
                measurement = measure_task(fixture, task)
                store.complete_task(conn, task, measurement)
            except Exception as exc:  # one bad task must not kill the scan
                message = f"{type(exc).__name__}: {exc}"
                try:
                    store.fail_task(conn, task, message)
                except Exception:
                    pass
                with progress.lock:
                    progress.failures.append(
                        Failure(
                            task_key=task.task_key,
                            quant_step=task.quant_step,
                            block_size=task.block_size,
                            error=message,
                        )
                    )
                continue
            with progress.lock:
                progress.completed += 1
    finally:
        conn.close()


def execute_scan(
    store: Store,
    run: RunInfo,
    fixture: Fixture,
    combos: Sequence[tuple[int, int]],
    workers: int = 1,
    stop_after: int | None = None,
    lease_seconds: float = LEASE_SECONDS,
) -> ScanOutcome:
    """Plan, claim and execute every scan combination.

    ``stop_after`` intentionally forces one worker so the interruption point
    is deterministic.  In-flight clips are always finished before a worker
    exits, so records are never left half-written.
    """
    task_keys = {
        (quant, block): make_task_key(run.content_hash, quant, block)
        for quant, block in combos
    }
    before = store.task_counts(run.run_id)
    done_before = before[DONE]
    inserted = store.sync_tasks(run.run_id, task_keys)
    store.recover_expired_leases(run.run_id)

    effective_workers = 1 if stop_after is not None else max(1, workers)
    progress = _Progress(stop_after)
    stop_event = threading.Event()

    try:
        with ThreadPoolExecutor(max_workers=effective_workers) as pool:
            futures = [
                pool.submit(
                    _worker_loop,
                    store,
                    run,
                    fixture,
                    f"worker-{index + 1}",
                    progress,
                    stop_event,
                    lease_seconds,
                )
                for index in range(effective_workers)
            ]
            for future in futures:
                future.result()
    except KeyboardInterrupt:
        stop_event.set()
        raise

    counts = store.task_counts(run.run_id)
    incomplete = counts[PENDING] + counts[RUNNING] + counts[FAILED]
    interrupted = bool(progress.stopped_early and incomplete > 0)

    failures = [
        Failure(
            task_key=str(row["task_key"]),
            quant_step=int(row["quant_step"]),
            block_size=int(row["block_size"]),
            error=str(row["last_error"] or ""),
        )
        for row in store.failed_tasks(run.run_id)
    ]
    return ScanOutcome(
        run=run,
        planned=len(task_keys),
        inserted=inserted,
        done_before=done_before,
        completed_now=progress.completed,
        resumed=done_before > 0,
        interrupted=interrupted,
        counts=counts,
        failures=failures,
    )
