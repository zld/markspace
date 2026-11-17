"""Command-line entry point: argument parsing, orchestration and exit codes.

Exit codes: 0 complete or controlled pause, 1 configuration or scan failure,
2 non-monotonic ladder, 130 user interrupt.
"""

from __future__ import annotations

import argparse
import signal
import sys
from pathlib import Path

from .encode import EncoderInfo, cleanup_stale_partials, detect_encoder
from .errors import (
    EXIT_FAILURE,
    EXIT_INTERRUPTED,
    EXIT_NON_MONOTONIC,
    EXIT_OK,
    ConfigError,
    LadderForgeError,
    NonMonotonicLadderError,
    StoreLockError,
)
from .ladder import Candidate, build_ladder
from .plan import Plan, build_plan, materialise_jobs
from .report import build_payload, print_summary, write_json
from .scheduler import Control, Scheduler
from .store import Store
from .synth import generate_clip

DEFAULT_RUN_DIR = Path(".runs")
DEFAULT_CONTENT_SEED = 20240517


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="ladderforge",
        description="Scan encoding combinations and build a bitrate ladder.",
    )
    sub = parser.add_subparsers(dest="command", required=True)

    run = sub.add_parser("run", help="generate samples, scan combinations, build ladder")
    run.add_argument("--db", required=True, type=Path, help="SQLite database path")
    run.add_argument("--frames", type=int, default=48)
    run.add_argument("--fps", type=int, default=24)
    run.add_argument("--resolutions", default="426x240,640x360")
    run.add_argument("--bitrates-kbps", default="250,500,1000")
    run.add_argument("--presets", default="ultrafast,medium")
    run.add_argument("--workers", type=int, default=2)
    run.add_argument(
        "--limit-jobs",
        type=int,
        default=None,
        help="claim at most this many jobs in this run (0 or unset = all)",
    )
    run.add_argument(
        "--resume",
        action="store_true",
        help="reuse completed results from an earlier run against the same db",
    )
    run.add_argument(
        "--retry-failed",
        action="store_true",
        help="explicitly requeue failed jobs before scanning",
    )
    run.add_argument(
        "--work-dir",
        type=Path,
        default=None,
        help="directory for synthetic sources and encoded outputs (default: "
        "alongside the database)",
    )
    run.add_argument(
        "--json-out",
        type=Path,
        default=None,
        help="write the run report as JSON to this path",
    )
    run.add_argument(
        "--content-seed",
        type=int,
        default=DEFAULT_CONTENT_SEED,
        help="seed for the synthetic source content",
    )
    run.add_argument(
        "--timeout-seconds",
        type=float,
        default=300.0,
        help="per-encode timeout passed to FFmpeg",
    )
    return parser


def _work_dirs(db_path: Path, explicit: Path | None) -> tuple[Path, Path]:
    root = explicit if explicit is not None else db_path.parent / "work"
    sources = root / "sources"
    outputs = root / "encoded"
    sources.mkdir(parents=True, exist_ok=True)
    outputs.mkdir(parents=True, exist_ok=True)
    return sources, outputs


def _failed_rows(store: Store, keys: tuple[str, ...]) -> list[dict[str, object]]:
    return [
        {
            "task_key": row["task_key"],
            "resolution": row["resolution"],
            "bitrate_kbps": int(row["bitrate_kbps"]),
            "preset": row["preset"],
            "error": row["error"] or "",
        }
        for row in store.failed_jobs(keys)
    ]


def _candidates(store: Store, keys: tuple[str, ...]) -> list[Candidate]:
    candidates = []
    for row in store.results(keys):
        candidates.append(
            Candidate(
                task_key=str(row["task_key"]),
                resolution=str(row["resolution"]),
                width=int(row["width"]),
                height=int(row["height"]),
                target_bps=int(row["target_bps"]),
                preset=str(row["preset"]),
                actual_bps=int(row["actual_bps"]),
                quality_psnr_db=float(row["quality_psnr_db"]),
            )
        )
    return candidates


def _print_plan(plan: Plan) -> None:
    print(
        f"plan frames={plan.frames} fps={plan.fps} "
        f"resolutions={','.join(r.label for r in plan.resolutions)} "
        f"presets={','.join(plan.presets)} "
        f"targets={','.join(str(t.bitrate_kbps) for t in plan.targets)} "
        f"duplicate_targets={plan.duplicate_targets} jobs={len(plan.jobs)}"
    )


def _run(args: argparse.Namespace) -> int:
    if args.workers < 1:
        raise ConfigError("--workers must be at least 1")
    if args.frames < 1:
        raise ConfigError("--frames must be at least 1")
    if args.limit_jobs is not None and args.limit_jobs < 0:
        raise ConfigError("--limit-jobs cannot be negative")

    plan = build_plan(
        frames=args.frames,
        resolutions=args.resolutions,
        bitrates=args.bitrates_kbps,
        presets=args.presets,
        workers=args.workers,
        fps=args.fps,
    )
