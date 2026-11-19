"""Directory scanning, parallel execution, output naming and failure roll-up."""

from __future__ import annotations

import os
from concurrent.futures import ThreadPoolExecutor, as_completed
from dataclasses import dataclass
from pathlib import Path

from .config import Config
from .errors import InputError, UsageError
from .pipeline import FileResult, failure_result, output_name, process_file

WAV_SUFFIXES = {".wav"}


def discover_inputs(root: Path, *, exclude: list[Path]) -> list[tuple[Path, str]]:
    """Recursively collect WAV files, case-insensitive, in stable sorted order.

    ``exclude`` directories (the output tree, temp trees) are pruned so a
    second run never re-ingests its own products.
    """
    if not root.exists():
        raise UsageError(f"input path does not exist: {root}", reason="missing_input")
    if root.is_file():
        if root.suffix.lower() not in WAV_SUFFIXES:
            raise UsageError(f"not a WAV file: {root.name}", reason="unsupported_extension")
        return [(root, root.name)]
    if not root.is_dir():
        raise UsageError(f"input is neither file nor directory: {root}", reason="bad_input")

    excluded = {_resolve(p) for p in exclude}
    found: list[tuple[Path, str]] = []
    for dirpath, dirnames, filenames in os.walk(root):
        here = Path(dirpath)
        dirnames[:] = sorted(
            d for d in dirnames if _resolve(here / d) not in excluded and not d.startswith(".")
        )
        for name in sorted(filenames):
            if Path(name).suffix.lower() in WAV_SUFFIXES:
                path = here / name
                found.append((path, path.relative_to(root).as_posix()))
    found.sort(key=lambda item: item[1])
    if not found:
        raise UsageError(f"no WAV files found under {root}", reason="empty_input")
    return found


def _resolve(path: Path) -> Path:
    try:
        return path.resolve()
    except OSError:
        return path.absolute()


@dataclass
class BatchOutcome:
    results: list[FileResult]
    output_root: str
    limited_by_peak: int = 0
    silent: int = 0

    @property
    def failed(self) -> list[FileResult]:
        return [r for r in self.results if not r.ok]

    @property
    def succeeded(self) -> list[FileResult]:
        return [r for r in self.results if r.ok]

    @property
    def exit_code(self) -> int:
        return 1 if self.failed else 0


def _unique_output(
    relative: str, taken: dict[str, str], *, input_relative: str
) -> tuple[Path, str]:
    """Map a relative input path to a collision-free output relative path."""
    rel = Path(relative)
    candidate = rel.with_name(output_name(rel))
    key = candidate.as_posix().lower()
    if key in taken and taken[key] != input_relative:
        raise InputError(
            f"output name collides with {taken[key]}",
            reason="output_collision",
        )
    taken[key] = input_relative
    return candidate, candidate.as_posix()


def run_batch(
    input_root: Path,
    output_root: Path,
    config: Config,
    *,
    jobs: int = 1,
    max_bytes: int,
    progress=None,
) -> BatchOutcome:
    """Process every discovered file; one failure never aborts the batch."""
    if jobs < 1:
        raise UsageError("jobs must be >= 1")
    out_abs = _resolve(output_root)
    # Pruning the output tree is what keeps reruns from re-ingesting products.
    entries = discover_inputs(input_root, exclude=[out_abs])

    taken: dict[str, str] = {}
    planned: list[tuple[Path, Path, str, str]] = []
    results: list[FileResult] = []
    for path, relative in entries:
        try:
            rel_out, rel_out_str = _unique_output(relative, taken, input_relative=relative)
        except InputError as exc:
            results.append(failure_result(relative, exc))
            continue
        planned.append((path, output_root / rel_out, relative, rel_out_str))

    outcome = BatchOutcome(results=results, output_root=output_root.as_posix())

    def work(item: tuple[Path, Path, str, str]) -> FileResult:
        path, target, relative, rel_out_str = item
        try:
            return process_file(
                path,
                target,
                relative,
                rel_out_str,
                config,
                max_bytes=max_bytes,
            )
        except Exception as exc:  # per-file isolation is a hard requirement
            return failure_result(relative, exc)

    if jobs == 1 or len(planned) <= 1:
        done = [work(item) for item in planned]
    else:
        with ThreadPoolExecutor(max_workers=jobs) as pool:
            futures = {pool.submit(work, item): item for item in planned}
            done = []
            for future in as_completed(futures):
                done.append(future.result())
    outcome.results.extend(done)

    def sort_key(result: FileResult) -> str:
        return result.input_path

    outcome.results.sort(key=sort_key)
    outcome.limited_by_peak = sum(1 for r in outcome.results if r.reason == "peak_limited")
    outcome.silent = sum(1 for r in outcome.results if r.reason == "silent")
    if progress is not None:
        for result in outcome.results:
            progress(result)
    return outcome
