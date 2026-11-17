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
    _print_plan(plan)

    encoder: EncoderInfo = detect_encoder()
    print(f"encoder={encoder.version}")

    sources_dir, outputs_dir = _work_dirs(args.db, args.work_dir)
    removed = cleanup_stale_partials(outputs_dir)
    if removed:
        print(f"cleaned_partials={removed}")

    clips = {
        resolution: generate_clip(
            resolution, plan.frames, plan.fps, args.content_seed, sources_dir
        )
        for resolution in plan.resolutions
    }
    for clip in clips.values():
        print(
            f"source resolution={clip.resolution.label} frames={clip.frames} "
            f"sha256={clip.sha256[:16]}"
        )

    jobs = materialise_jobs(
        plan, {res: clip.sha256 for res, clip in clips.items()}, encoder.version
    )

    control = Control()
    previous_sigint = _install_sigint(control)

    store = Store(args.db)
    try:
        store.init_schema()
        signature = Scheduler.signature_for(jobs)
        recorded = store.get_meta("plan_signature")
        if args.resume:
            if recorded is not None and recorded != signature:
                raise ConfigError(
                    "refusing to resume: the recorded scan was built from a "
                    "different source or parameters (plan signature changed). "
                    "Start a new --db or rerun without --resume."
                )
            if recorded is None and store.has_rows():
                raise ConfigError(
                    "refusing to resume: this database has rows but no plan "
                    "signature to verify them against"
                )
        elif recorded is not None and recorded != signature and store.has_rows():
            print(
                "note=plan changed; previously completed rows will not be "
                "reused and stay in the database"
            )
        store.set_meta("plan_signature", signature)

        if args.retry_failed:
            requeued = store.requeue_failed()
            print(f"requeued_failed={requeued}")
        store.sync_plan(jobs)

        scheduler = Scheduler(
            store=store,
            encoder=encoder,
            jobs=jobs,
            clips=clips,
            output_dir=outputs_dir,
            control=control,
            timeout=args.timeout_seconds,
        )
        summary = scheduler.run(workers=args.workers, limit_jobs=args.limit_jobs)
        keys = tuple(job.key for job in jobs)

        ladder = None
        failed = _failed_rows(store, keys)
        candidates = _candidates(store, keys)
        exit_code = EXIT_OK
        if summary.state == "COMPLETE":
            try:
                ladder = build_ladder(candidates, plan.targets)
            except NonMonotonicLadderError as exc:
                print(f"ladder_error={exc}")
                exit_code = EXIT_NON_MONOTONIC
        elif summary.state in ("PARTIAL", "PENDING"):
            # A controlled pause or a --limit-jobs slice is not a failure; the
            # next run resumes from the rows already committed.
            print("ladder_skipped=scan incomplete; resume to build the ladder")
        else:
            exit_code = EXIT_FAILURE

        if failed:
            exit_code = EXIT_FAILURE
        if control.stop_reason == "interrupt":
            exit_code = EXIT_INTERRUPTED

        payload = build_payload(summary, ladder, failed, plan=plan.to_dict())
        print_summary(payload)
        if args.json_out is not None:
            write_json(payload, args.json_out)
            print(f"json_out={args.json_out}")
        return exit_code
    finally:
        store.close()
        _restore_sigint(previous_sigint)


def _install_sigint(control: Control):
    def on_sigint(signum, frame):  # noqa: ARG001
        if control.stopped:
            raise KeyboardInterrupt
        control.stop("interrupt")
        print(
            "\nSIGINT: cancelling in-flight encodes and releasing their leases",
            file=sys.stderr,
        )

    def on_sigterm(signum, frame):  # noqa: ARG001
        # SIGTERM is the controlled pause: stop claiming, let work finish.
        if control.draining:
            return
        control.drain("paused")
        print(
            "\nSIGTERM: pausing scan; in-flight jobs will finish",
            file=sys.stderr,
        )

    previous = {
        signal.SIGINT: signal.getsignal(signal.SIGINT),
        signal.SIGTERM: signal.getsignal(signal.SIGTERM),
    }
    signal.signal(signal.SIGINT, on_sigint)
    signal.signal(signal.SIGTERM, on_sigterm)
    return previous


def _restore_sigint(previous) -> None:
    for signum, handler in previous.items():
        signal.signal(signum, handler)


def main(argv: list[str] | None = None) -> int:
    parser = build_parser()
    args = parser.parse_args(argv)
    try:
        if args.command == "run":
            return _run(args)
        parser.error(f"unknown command {args.command}")
        return EXIT_FAILURE
    except LadderForgeError as exc:
        print(f"error={exc}", file=sys.stderr)
        return exc.exit_code
    except StoreLockError as exc:
        print(f"error=database_lock {exc}", file=sys.stderr)
        return EXIT_FAILURE
    except KeyboardInterrupt:
        print("interrupted", file=sys.stderr)
        return EXIT_INTERRUPTED


if __name__ == "__main__":
    raise SystemExit(main())
