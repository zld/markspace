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
    """Pause / drain / stop signalling shared by the CLI, scheduler and workers.

    * ``pause`` freezes claiming but leaves leases intact.
    * ``drain`` stops claiming new jobs and lets in-flight work finish: a
      controlled pause that exits with success.
    * ``stop`` cancels in-flight work so a SIGINT can unwind promptly.
    """

    def __init__(self) -> None:
        self._run = threading.Event()
        self._run.set()
        self._stop = threading.Event()
        self._draining = threading.Event()
        self._reason: str | None = None
        self._lock = threading.Lock()

    def pause(self) -> None:
        self._run.clear()

    def resume(self) -> None:
        self._run.set()

    def _set_reason(self, reason: str | None) -> None:
        with self._lock:
            if self._reason is None and reason is not None:
                self._reason = reason

    def drain(self, reason: str | None = None) -> None:
        self._set_reason(reason)
        self._draining.set()
        self._run.set()

    def stop(self, reason: str | None = None) -> None:
        self._set_reason(reason)
        self._draining.set()
        self._run.set()
        self._stop.set()

    @property
    def stopped(self) -> bool:
        return self._stop.is_set()

    @property
    def draining(self) -> bool:
        return self._draining.is_set()

    @property
    def stop_reason(self) -> str | None:
        return self._reason

    def wait_until_runnable(self, timeout: float = 0.2) -> bool:
        """Block while paused; returns False when stopped or draining."""
        while not self.stopped and not self.draining:
            if self._run.wait(timeout):
                return not (self.stopped or self.draining)
        return False


class _Limit:
    """Caps how many *newly claimed* jobs a run may start.

    A slot is reserved before claiming and refunded when the claim comes back
    empty, so concurrent workers can never exceed the limit and an idle worker
    does not burn part of another worker's budget.
    """

    def __init__(self, limit: int | None) -> None:
        self._limit = limit
        self._claimed = 0
        self._lock = threading.Lock()

    def reserve(self) -> bool:
        with self._lock:
            if self._limit is not None and self._claimed >= self._limit:
                return False
            self._claimed += 1
            return True

    def refund(self) -> None:
        with self._lock:
            if self._claimed > 0:
                self._claimed -= 1


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
        timeout: float = 300.0,
    ) -> None:
        self.store = store
        self.encoder = encoder
        self.jobs = jobs
        self.clips = clips
        self.output_dir = output_dir
        self.control = control or Control()
        self.timeout = timeout
        self._inflight = _InFlight()
        self._errors: list[dict[str, str]] = []
        self._errors_lock = threading.Lock()
        self._heartbeat: threading.Thread | None = None
        self._references: dict[Resolution, object] = {}

    # -- helpers ---------------------------------------------------------
    @staticmethod
    def signature_for(jobs: tuple[PlannedJob, ...]) -> str:
        payload = "|".join(
            sorted(
                f"{j.key}:{j.source_sha256}:{j.ffmpeg_version}:{j.frames}:{j.fps}"
                for j in jobs
            )
        )
        return hashlib.sha256(payload.encode()).hexdigest()[:16]

    def plan_signature(self) -> str:
        return self.signature_for(self.jobs)

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

        def beat() -> None:
            while not self.control.stopped:
                time.sleep(HEARTBEAT_SECONDS)
                for key, worker in self._inflight.snapshot():
                    if self.control.stopped:
                        return
                    try:
                        self.store.renew(key, worker, LEASE_SECONDS)
                    except StoreLockError:
                        continue

        self._heartbeat = threading.Thread(
            target=beat, name="ladderforge-heartbeat", daemon=True
        )
        self._heartbeat.start()

    # -- job execution ---------------------------------------------------
    def _process(self, row, worker_id: str, frames: int) -> bool:
        key = str(row["task_key"])
        resolution = Resolution(int(row["width"]), int(row["height"]))
        bitrate = int(row["bitrate_kbps"])
        preset = str(row["preset"])
        label = resolution.label
        try:
            clip = self.clips[resolution]
            job = JobSpec(resolution=resolution, bitrate_kbps=bitrate, preset=preset)
            out_path = self._output_path(
                PlannedJob(
                    job=job,
                    source_sha256=str(row["source_sha256"]),
                    ffmpeg_version=str(row["ffmpeg_version"]),
                    frames=frames,
                    fps=int(row["fps"]),
                    key=key,
                )
            )
            outcome = encode(
                self.encoder,
                clip,
                job,
                output_path=out_path,
                worker_id=worker_id,
                timeout=self.timeout,
                cancel_check=lambda: self.control.stopped,
            )
            measurement = measure(
                self.encoder,
                outcome.output_path,
                resolution,
                frames=frames,
                reference_luma=self._reference(resolution),
                timeout=120.0,
            )
            if self.control.stopped:
                return False
            committed = self._store_call(
                self.store.complete,
                task_key=key,
                worker_id=worker_id,
                actual_bps=measurement.actual_bps,
                quality_psnr_db=measurement.quality_psnr_db,
                encoded_bytes=measurement.encoded_bytes,
                duration_s=measurement.duration_s,
                output_path=str(outcome.output_path),
            )
            if not committed:
                self._record_error(key, label, bitrate, preset, "lease lost before commit")
            return bool(committed)
        except CancelledError:
            # Deliberate stop: hand the job back without recording a failure.
            try:
                self._store_call(self.store.release, key, worker_id, "interrupted")
            except StoreLockError as exc:
                self._record_error(key, label, bitrate, preset, f"release failed: {exc}")
            return False
        except TaskError as exc:
            self._fail(key, worker_id, label, bitrate, preset, str(exc))
            return False
        except StoreLockError as exc:
            self._fail(key, worker_id, label, bitrate, preset, f"database lock: {exc}")
            return False
        except Exception as exc:  # noqa: BLE001 - isolate per-job failures
            self._fail(
                key, worker_id, label, bitrate, preset, f"{type(exc).__name__}: {exc}"
            )
            return False

    def _fail(self, key: str, worker_id: str, label: str, bitrate: int, preset: str, error: str) -> None:
        self._record_error(key, label, bitrate, preset, error)
        try:
            self._store_call(self.store.fail, key, worker_id, error[:800])
        except StoreLockError as exc:
            self._record_error(key, label, bitrate, preset, f"could not record failure: {exc}")

    # -- worker loop -----------------------------------------------------
    def _worker(self, index: int, limit: _Limit, frames: int) -> None:
        worker_id = f"w{index}"
        try:
            while not self.control.stopped and not self.control.draining:
                if not self.control.wait_until_runnable():
                    break
                if not limit.reserve():
                    break
                try:
                    rows = self._store_call(
                        self.store.claim,
                        worker_id,
                        limit=1,
                        lease_seconds=LEASE_SECONDS,
                    )
                except StoreLockError as exc:
                    limit.refund()
                    self._record_error("", "claim", 0, worker_id, f"claim failed: {exc}")
                    break
                if not rows:
                    limit.refund()
                    break
                for row in rows:
                    self._inflight.add(str(row["task_key"]), worker_id)
                    try:
                        self._process(row, worker_id, frames)
                    finally:
                        self._inflight.discard(str(row["task_key"]))
        finally:
            self.store.release_owned(worker_id, "worker stopped")

    # -- entry point -----------------------------------------------------
    def run(self, *, workers: int, limit_jobs: int | None = None) -> RunSummary:
        total = len(self.jobs)
        keys = tuple(job.key for job in self.jobs)
        reused = len(self.store.completed_keys(keys))
        before_completed = reused

        self._start_heartbeat()
        limit = _Limit(limit_jobs if limit_jobs and limit_jobs > 0 else None)
        threads = [
            threading.Thread(
                target=self._worker,
                args=(i + 1, limit, self.jobs[0].frames),
                name=f"ladderforge-worker-{i + 1}",
                daemon=True,
            )
            for i in range(workers)
        ]
        for thread in threads:
            thread.start()
        for thread in threads:
            while thread.is_alive():
                thread.join(timeout=0.2)

        counts = self.store.state_counts(keys)
        completed = counts["completed"]
        total = len(keys)
        state = _state_from_counts(completed, total, counts["failed"])
        return RunSummary(
            jobs_total=total,
            completed=completed,
            pending=counts["pending"],
            failed=counts["failed"],
            running=counts["running"],
            reused=before_completed,
            newly_completed=max(0, completed - before_completed),
            state=state,
            errors=list(self._errors),
            stop_reason=self.control.stop_reason,
        )
