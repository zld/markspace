"""Concurrent scan scheduler with leases, pause/stop and resume support.

Worker threads claim one job at a time through a SQLite ``BEGIN IMMEDIATE``
transaction, so two workers can never run the same task.  A heartbeat thread
keeps leases alive while FFmpeg works; if the process dies, those leases expire
and the next run reclaims the jobs.  Completed results are never re-run, and a
single failing job is recorded and skipped rather than aborting the scan.
"""

from __future__ import annotations

import hashlib
import threading
import time
from dataclasses import dataclass, field
from pathlib import Path

from .encode import EncoderInfo, encode
from .errors import CancelledError, StoreLockError, TaskError
from .measure import measure
from .plan import JobSpec, PlannedJob, Resolution
from .store import Store
from .synth import SourceClip, read_reference_luma

LEASE_SECONDS = 30.0
HEARTBEAT_SECONDS = 8.0
STORE_RETRIES = 4


@dataclass
class RunSummary:
    jobs_total: int
    completed: int
    pending: int
    failed: int
    running: int
    reused: int
    newly_completed: int
    state: str
    errors: list[dict[str, str]] = field(default_factory=list)
    stop_reason: str | None = None


def _state_from_counts(completed: int, total: int, failed: int) -> str:
    if total == 0:
        return "EMPTY"
    if completed == total:
        return "COMPLETE"
    if completed > 0:
        return "PARTIAL"
    if failed > 0:
        return "FAILED"
    return "PENDING"


class Control:
    """Pause / stop signalling shared by the CLI, scheduler and workers."""

    def __init__(self) -> None:
        self._run = threading.Event()
        self._run.set()
        self._stop = threading.Event()
        self._stop_reason: str | None = None
        self._lock = threading.Lock()

    def pause(self) -> None:
        self._run.clear()

    def resume(self) -> None:
        self._run.set()

    def stop(self, reason: str | None = None) -> None:
        with self._lock:
            if self._stop_reason is None and reason is not None:
                self._stop_reason = reason
        self._run.set()
        self._stop.set()

    @property
    def stopped(self) -> bool:
        return self._stop.is_set()

    @property
    def stop_reason(self) -> str | None:
        return self._stop_reason

    def wait_until_runnable(self, timeout: float = 0.2) -> bool:
        """Block while paused; returns False when stopped."""
        while not self.stopped:
            if self._run.wait(timeout):
                if self.stopped:
                    return False
                return True
        return False


class _Limit:
    def __init__(self, limit: int | None) -> None:
        self._limit = limit
        self._claimed = 0
        self._lock = threading.Lock()

    def take(self) -> bool:
        with self._lock:
            if self._limit is not None and self._claimed >= self._limit:
                return False
            self._claimed += 1
            return True


class _InFlight:
    """Tracks leases held by this process so the heartbeat can renew them."""

    def __init__(self) -> None:
        self._items: dict[str, str] = {}
        self._lock = threading.Lock()

    def add(self, key: str, worker: str) -> None:
        with self._lock:
            self._items[key] = worker

    def discard(self, key: str) -> None:
        with self._lock:
            self._items.pop(key, None)

    def snapshot(self) -> list[tuple[str, str]]:
        with self._lock:
            return list(self._items.items())


class Scheduler:
    def __init__(
        self,
        *,
        store: Store,
        encoder: EncoderInfo,
        jobs: tuple[PlannedJob, ...],
        clips: dict[Resolution, SourceClip],
        output_dir: Path,
        control: Control | None = None,
    ) -> None:
        self.store = store
        self.encoder = encoder
        self.jobs = jobs
        self.clips = clips
        self.output_dir = output_dir
        self.control = control or Control()
        self._inflight = _InFlight()
        self._errors: list[dict[str, str]] = []
        self._errors_lock = threading.Lock()
        self._heartbeat: threading.Thread | None = None
        self._references: dict[Resolution, object] = {}

    # -- helpers ---------------------------------------------------------
    def plan_signature(self) -> str:
        payload = "|".join(sorted(f"{j.key}:{j.job.resolution.label}" for j in self.jobs))
        return hashlib.sha256(payload.encode()).hexdigest()[:16]

    def _reference(self, resolution: Resolution):
        if resolution not in self._references:
            self._references[resolution] = read_reference_luma(self.clips[resolution])
        return self._references[resolution]

    def _record_error(self, key: str, resolution: str, bitrate: int, preset: str, error: str) -> None:
        with self._errors_lock:
            self._errors.append(
                {
                    "task_key": key,
                    "resolution": resolution,
                    "bitrate_kbps": str(bitrate),
                    "preset": preset,
                    "error": error,
                }
            )

    def _store_call(self, fn, *args, **kwargs):
        last: Exception | None = None
        for attempt in range(STORE_RETRIES):
            try:
                return fn(*args, **kwargs)
            except StoreLockError as exc:
                last = exc
                time.sleep(0.1 * (attempt + 1))
        assert last is not None
        raise last

    def _output_path(self, planned: PlannedJob) -> Path:
        job = planned.job
        name = (
            f"{planned.key[:16]}_{job.resolution.label}_"
            f"{job.bitrate_kbps}kbps_{job.preset}.mp4"
        )
        return self.output_dir / name

    # -- heartbeat -------------------------------------------------------
    def _start_heartbeat(self) -> None:
        if self._heartbeat is not None:
            return

