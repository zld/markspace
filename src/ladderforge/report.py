"""Terminal summary and JSON reporting.

The terminal output is line-oriented ``key=value`` so the demo is easy to scan
and easy to verify by eye.  The JSON file carries the same data with full
precision for downstream tooling.
"""

from __future__ import annotations

import json
from dataclasses import dataclass
from pathlib import Path

from .ladder import Ladder
from .scheduler import RunSummary


@dataclass
class ReportPayload:
    run: dict[str, object]
    ladder: dict[str, object] | None
    failed: list[dict[str, object]]

    def to_json(self) -> str:
        return json.dumps(self, default=_as_json, indent=2, sort_keys=True)


def _as_json(value):
    if hasattr(value, "to_dict"):
        return value.to_dict()
    raise TypeError(f"cannot serialise {type(value).__name__}")


def build_payload(
    summary: RunSummary,
    ladder: Ladder | None,
    failed: list[dict[str, object]],
    *,
    plan: dict[str, object] | None = None,
) -> ReportPayload:
    run = {
        "jobs_total": summary.jobs_total,
        "completed": summary.completed,
        "pending": summary.pending,
        "failed": summary.failed,
        "running": summary.running,
        "reused": summary.reused,
        "newly_completed": summary.newly_completed,
        "state": summary.state,
        "stop_reason": summary.stop_reason,
        "errors": summary.errors,
    }
    if plan is not None:
        run["plan"] = plan
    return ReportPayload(run=run, ladder=None if ladder is None else ladder.to_dict(), failed=failed)


def print_summary(payload: ReportPayload) -> None:
    run = payload.run
    print(
        "jobs_total={jobs_total} completed={completed} pending={pending} "
        "failed={failed} running={running} reused={reused} "
        "newly_completed={newly_completed} state={state}".format(**run)
    )
    if run.get("stop_reason"):
        print(f"stop_reason={run['stop_reason']}")

    ladder = payload.ladder
    if ladder:
        targets = ladder.get("rungs", [])
        monotonicity = ladder.get("monotonicity")
        print(
            f"targets={len(targets)} duplicate_targets="
            f"{run.get('plan', {}).get('duplicate_targets', 0)} "
            f"monotonicity={monotonicity}"
        )
        for rung in targets:
            print(
                "target_bps={target_bps} actual_bps={actual_bps} "
                "quality_psnr_db={quality_psnr_db} aliases={aliases} "
                "resolution={resolution} preset={preset} "
                "monotonicity={monotonicity}".format(
                    monotonicity=monotonicity, **rung
                )
            )

    errors = payload.run.get("errors") or []
    if errors:
        print(f"errors={len(errors)}")
        for item in errors[:10]:
            print(
                "  error task={task_key} resolution={resolution} "
                "bitrate_kbps={bitrate_kbps} detail={error}".format(**item)
            )

    if payload.failed:
        print(f"failed_jobs={len(payload.failed)}")
        for item in payload.failed[:10]:
            print(
                "  failed target={bitrate_kbps}kbps resolution={resolution} "
                "preset={preset} detail={error}".format(**item)
            )


def write_json(payload: ReportPayload, path: Path) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    temporary = path.with_suffix(path.suffix + ".tmp")
    temporary.write_text(payload.to_json(), encoding="utf-8")
    temporary.replace(path)
