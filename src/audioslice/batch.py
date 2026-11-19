"""Batch scheduling: one detector instance reused across every input file.

The processor discovers ``.wav`` files, streams each file's SHA-256 without
loading it twice into memory, decodes/analyses it with a single shared
:class:`~audioslice.detector.ActivityDetector`, and records the outcome in
one SQLite transaction. Failures are isolated per file: with
``continue_on_error`` the rest of the batch still completes and successful
results are committed; without it the whole transaction is rolled back.
"""

from __future__ import annotations

import hashlib
from dataclasses import dataclass, field
from pathlib import Path
from typing import Callable, Sequence

from .detector import ActivityDetector, DetectorConfig, DetectionResult
from .export import FileExport
from .features import extract_features
from .store import Store
from .wavio import WavFormatError, read_wav


@dataclass
class FileOutcome:
    path: str
    status: str  # "ok" | "failed" | "skipped"
    sha256: str = ""
    sample_rate: int | None = None
    channels: int | None = None
    n_samples: int | None = None
    duration_seconds: float = 0.0
    silence_total_seconds: float = 0.0
    result: DetectionResult | None = None
    failures: list[str] = field(default_factory=list)

    def to_export(self) -> FileExport:
        segments: list[dict] = []
        if self.result is not None:
            for seg in self.result.activity:
                segments.append({
                    "start_sample": int(seg.start_sample),
                    "end_sample": int(seg.end_sample),
                    "kind": seg.kind,
                    "reason": seg.reason,
                    "candidates": [list(c) for c in seg.candidates],
                })
        return FileExport(
            path=self.path, status=self.status, sha256=self.sha256,
            sample_rate=self.sample_rate, channels=self.channels,
            n_samples=self.n_samples, duration_seconds=self.duration_seconds,
            silence_total_seconds=self.silence_total_seconds,
            segments=segments, failures=list(self.failures),
        )


@dataclass
class BatchResult:
    outcomes: list[FileOutcome]

    @property
    def ok(self) -> list[FileOutcome]:
        return [o for o in self.outcomes if o.status == "ok"]

    @property
    def failed(self) -> list[FileOutcome]:
        return [o for o in self.outcomes if o.status == "failed"]

    @property
    def skipped(self) -> list[FileOutcome]:
        return [o for o in self.outcomes if o.status == "skipped"]

    def to_exports(self) -> list[FileExport]:
        return [o.to_export() for o in self.outcomes]


def discover_wavs(input_dir: str) -> list[str]:
    """Return sorted .wav paths under ``input_dir`` (recursive)."""
    root = Path(input_dir)
    if not root.exists():
        raise FileNotFoundError(f"input directory does not exist: {input_dir}")
    if not root.is_dir():
        raise NotADirectoryError(f"input path is not a directory: {input_dir}")
    return sorted(str(p) for p in root.rglob("*.wav") if p.is_file())


def sha256_file(path: str, chunk_size: int = 1 << 20) -> str:
    """Stream a file's SHA-256 so memory use stays independent of file size."""
    digest = hashlib.sha256()
    with open(path, "rb") as fh:
        for chunk in iter(lambda: fh.read(chunk_size), b""):
            digest.update(chunk)
    return digest.hexdigest()


class BatchProcessor:
    """Runs one configured detector over many files and persists outcomes."""

    def __init__(self, config: DetectorConfig, store: Store,
                 continue_on_error: bool = True,
                 progress: Callable[[str], None] | None = None):
        config.validate()  # fail before touching any file
        self._detector = ActivityDetector(config)  # ONE instance per batch
        self._config = config
        self._store = store
        self.continue_on_error = continue_on_error
        self._progress = progress or (lambda msg: None)

    @property
    def config(self) -> DetectorConfig:
        return self._config

    def run(self, paths: Sequence[str]) -> BatchResult:
        outcomes: list[FileOutcome] = []
        run_id = self._store.create_run(
            self._config.signature(), self._config.frame_ms,
            self._config.noise_window_ms, self._config.min_event_ms,
            self._config.merge_gap_ms, self.continue_on_error,
        )
        try:
            for path in paths:
                outcome = self._process_one(path)
                outcomes.append(outcome)
                self._persist(run_id, outcome)
                if outcome.status == "failed" and not self.continue_on_error:
                    raise _BatchAborted(outcome)
        except _BatchAborted:
            self._store.rollback()
            return BatchResult(outcomes)
        except BaseException:
            # Any storage/OS surprise rolls the whole batch back; no half
            # batch of segments may survive an interrupted write.
            self._store.rollback()
            raise
        self._store.commit()
        return BatchResult(outcomes)

    # -- internals --------------------------------------------------------

    def _process_one(self, path: str) -> FileOutcome:
        try:
            digest = sha256_file(path)
        except OSError as exc:
            return FileOutcome(path=path, status="failed", failures=[f"cannot read file: {exc}"])

        existing = self._store.find_existing_file(digest, self._config.signature())
        if existing is not None:
            self._progress(f"skip (already imported as {existing}): {path}")
            return FileOutcome(path=path, status="skipped", sha256=digest,
                               failures=[f"duplicate of {existing} for this configuration"])

        try:
            wav = read_wav(path)
            feats = extract_features(
                wav.samples, wav.sample_rate,
                self._config.frame_ms, self._config.noise_window_ms,
                self._config.noise_quantile,
            )
            result = self._detector.detect(feats)
        except WavFormatError as exc:
            return FileOutcome(path=path, status="failed", sha256=digest,
                               failures=[exc.reason])
        except (ValueError, OSError) as exc:
            return FileOutcome(path=path, status="failed", sha256=digest,
                               failures=[str(exc)])

        silence_seconds = sum(
            s.length_samples for s in result.silence
        ) / float(wav.sample_rate)
        return FileOutcome(
            path=path, status="ok", sha256=digest,
            sample_rate=wav.sample_rate, channels=wav.channels,
            n_samples=wav.n_samples, duration_seconds=wav.duration_seconds,
            silence_total_seconds=silence_seconds, result=result,
        )

    def _persist(self, run_id: int, outcome: FileOutcome) -> None:
        if outcome.status == "skipped":
            return
        if outcome.status == "failed":
            self._store.add_file(
                run_id, outcome.path, outcome.sha256, "failed",
                reason="; ".join(outcome.failures),
            )
            return
        file_id = self._store.add_file(
            run_id, outcome.path, outcome.sha256, "ok",
            sample_rate=outcome.sample_rate, channels=outcome.channels,
            n_samples=outcome.n_samples, duration_seconds=outcome.duration_seconds,
            silence_total_seconds=outcome.silence_total_seconds,
        )
        assert outcome.result is not None
        self._store.add_segments(
            file_id, (*outcome.result.activity, *outcome.result.silence)
        )


class _BatchAborted(Exception):
    """Internal signal: non-continue mode hit a failing file."""
