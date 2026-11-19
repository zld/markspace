"""Directory scanning and batch orchestration.

A single detector implementation serves both single-file and batch modes.
Corrupt or unreadable files are recorded as failures and never stop the run;
each file is persisted in its own savepoint so one failure cannot roll back
rows already written for successful files.
"""
from __future__ import annotations

from dataclasses import dataclass, field
from pathlib import Path

from .detector import detect
from .models import AudioData, DetectParams, DetectionResult
from .store import Store, sha256_file
from .wav import WavError, read_wav


@dataclass
class FileOutcome:
    rel_path: str
    status: str  # "ok" | "failed"
    error: str | None = None
    result: DetectionResult | None = None
    audio: AudioData | None = None
    content_sha256: str = ""


@dataclass
class BatchSummary:
    run_id: int
    outcomes: list[FileOutcome] = field(default_factory=list)

    @property
    def files_ok(self) -> int:
        return sum(1 for o in self.outcomes if o.status == "ok")

    @property
    def files_failed(self) -> int:
        return sum(1 for o in self.outcomes if o.status == "failed")

    @property
    def segment_count(self) -> int:
        return sum(
            o.result.segment_count() for o in self.outcomes if o.result is not None
        )

    def silence_total_ms(self) -> float:
        return sum(
            o.result.silence_total_ms()
            for o in self.outcomes
            if o.result is not None
        )


def find_wav_files(root: str | Path) -> list[Path]:
    """Recursively collect .wav files, sorted by relative path for stability."""
    root = Path(root)
    if not root.exists():
        raise FileNotFoundError(f"input path does not exist: {root}")
    if root.is_file():
        return [root]
    found = [
        path
        for path in root.rglob("*")
        if path.is_file() and path.suffix.lower() == ".wav"
    ]
    return sorted(found, key=lambda p: p.relative_to(root).as_posix())


def _relative(path: Path, root: Path) -> str:
    if root.is_file():
        return path.name
    return path.relative_to(root).as_posix()


def process_file(path: Path, params: DetectParams) -> FileOutcome:
    """Read, validate and detect one file, never raising on bad input."""
    rel_path = path.name
    try:
        sha = sha256_file(path)
    except OSError as exc:
        return FileOutcome(rel_path, "failed", error=f"unreadable file: {exc}")
    try:
        audio = read_wav(str(path))
    except WavError as exc:
        return FileOutcome(rel_path, "failed", error=str(exc), content_sha256=sha)
    try:
        result = detect(audio, params)
    except Exception as exc:  # defensive: one bad file must not kill the batch
        return FileOutcome(rel_path, "failed", error=f"detection error: {exc}",
                           content_sha256=sha)
    return FileOutcome(rel_path, "ok", result=result, audio=audio,
                       content_sha256=sha)


def run_batch(
    input_path: str | Path,
    store: Store,
    params: DetectParams,
) -> BatchSummary:
    """Scan, detect and persist every WAV under ``input_path``."""
    params.validate()
    root = Path(input_path)
    files = find_wav_files(root)
    run_id = store.start_run(params)
    summary = BatchSummary(run_id=run_id)

    for path in files:
        rel_path = _relative(path, root)
        outcome = process_file(path, params)
        outcome.rel_path = rel_path
        # Savepoint isolates this file's writes inside the run transaction.
        store.conn.execute("SAVEPOINT file_write")
        try:
            if outcome.status == "ok" and outcome.audio is not None and outcome.result:
                store.record_result(
                    run_id, rel_path, outcome.content_sha256,
                    outcome.audio, outcome.result,
                )
            else:
                store.record_failure(
                    run_id, rel_path, outcome.content_sha256,
                    outcome.error or "unknown error",
                )
        except Exception:
            store.conn.execute("ROLLBACK TO SAVEPOINT file_write")
            raise
        finally:
            store.conn.execute("RELEASE SAVEPOINT file_write")
    store.commit()
    return summary
