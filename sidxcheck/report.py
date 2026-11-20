"""Terminal and JSON rendering of check / rebuild results."""

from __future__ import annotations

import json
from typing import Any

from .rewrite import RewriteResult
from .segments import SegmentPlan
from .verify import CheckResult


def _segment_rows(plan: SegmentPlan) -> list[dict[str, Any]]:
    return [
        {
            "index": segment.index,
            "sample_start": segment.sample_start + 1,
            "sample_end": segment.sample_end,
            "sample_count": segment.sample_count,
            "dts_start": segment.dts_start,
            "duration_ticks": segment.duration_ticks,
            "byte_start": segment.byte_start,
            "byte_end": segment.byte_end,
            "byte_size": segment.byte_size,
            "starts_with_sap": segment.starts_with_sap,
            "sap_type": segment.sap_type,
        }
        for segment in plan.segments
    ]


def check_payload(result: CheckResult) -> dict[str, Any]:
    primary = result.primary_index
    indexes = []
    for check in result.index_checks:
        indexes.append(
            {
                "box": check.box_path,
                "box_start": check.box_start,
                "box_end": check.box_end,
                "version": check.version,
                "reference_count": check.reference_count,
                "reference_id": check.reference_id,
                "timescale": check.timescale,
                "first_offset": check.first_offset,
                "anchor_offset": check.anchor_offset,
                "expected_anchor": check.expected_anchor,
                "index_offset_delta": check.offset_delta,
                "index_offset_ok": check.offset_ok,
                "duration_ok": check.duration_ok,
                "samples_covered": check.sample_count,
                "ok": check.ok,
                "problems": list(check.problems),
            }
        )
    return {
        "file": result.path,
        "file_size": result.file_size,
        "track_id": result.track_id,
        "timescale": result.timescale,
        "media_duration": result.media_duration,
        "sample_entries": result.sample_entries,
        "sidx_box_count": result.sidx_box_count,
        "ok": result.ok,
        "index_offset_ok": bool(primary.offset_ok) if primary else False,
        "index_offset_delta": primary.offset_delta if primary else None,
        "indexes": indexes,
        "problems": list(result.problems),
        "warnings": list(result.warnings),
    }


def rebuild_payload(
    result: RewriteResult,
    *,
    plan: SegmentPlan | None = None,
    source_check: CheckResult | None = None,
    rebuilt_check: CheckResult | None = None,
) -> dict[str, Any]:
    payload: dict[str, Any] = {
        "source": result.source,
        "destination": result.destination,
        "source_size": result.source_size,
        "destination_size": result.destination_size,
        "new_sidx_offset": result.new_sidx_offset,
        "new_sidx_size": result.new_sidx_size,
        "index_version": result.index_version,
        "chunk_offset_kind": result.chunk_offset_kind,
        "sample_entries": result.samples,
        "segment_count": result.segments,
        "layout_passes": result.layout_passes,
        "offset_shift": result.offset_shift,
        "warnings": list(result.warnings),
    }
    if source_check is not None:
        payload["old_index_offset_ok"] = (
            source_check.primary_index.offset_ok if source_check.primary_index else False
        )
        payload["old_index_offset_delta"] = (
            source_check.primary_index.offset_delta if source_check.primary_index else None
        )
    if rebuilt_check is not None:
        primary = rebuilt_check.primary_index
        payload["rebuilt_index_offset_ok"] = bool(primary.offset_ok) if primary else False
        payload["rebuilt_index_offset_delta"] = primary.offset_delta if primary else None
        payload["rebuilt_ok"] = rebuilt_check.ok
        payload["rebuilt_problems"] = list(rebuilt_check.problems) + [
            problem for check in rebuilt_check.index_checks for problem in check.problems
        ]
    if plan is not None:
        payload["segment_ticks"] = plan.target_ticks
        payload["max_drift_ticks"] = plan.max_drift_ticks
        payload["boundaries"] = _segment_rows(plan)
    return payload


def demo_payload(
    *,
    source: str,
    destination: str,
    result: RewriteResult,
    plan: SegmentPlan,
    source_check: CheckResult,
    rebuilt_check: CheckResult,
) -> dict[str, Any]:
    payload = rebuild_payload(
        result,
        plan=plan,
        source_check=source_check,
        rebuilt_check=rebuilt_check,
    )
    payload["source"] = source
    payload["destination"] = destination
    payload["old_index_offset_ok"] = (
        source_check.primary_index.offset_ok if source_check.primary_index else False
    )
    payload["rebuilt_index_offset_ok"] = (
        rebuilt_check.primary_index.offset_ok if rebuilt_check.primary_index else False
    )
    payload["index_offset_delta"] = (
        rebuilt_check.primary_index.offset_delta
        if rebuilt_check.primary_index is not None
        else None
    )
    payload["sample_entries"] = rebuilt_check.sample_entries
    return payload


def render_check(result: CheckResult, *, as_json: bool) -> str:
    if as_json:
        return json.dumps(check_payload(result), indent=2, sort_keys=True)
    lines = [
        f"file            {result.path}",
        f"size            {result.file_size} bytes",
        f"track           {result.track_id} (timescale {result.timescale}, "
        f"duration {result.media_duration} ticks)",
        f"samples         {result.sample_entries}",
        f"sidx boxes      {result.sidx_box_count}",
    ]
    for check in result.index_checks:
        status = "OK" if check.ok else "FAIL"
        lines.append(
            f"index {check.box_path}  {status}  v{check.version}  "
            f"{check.reference_count} refs"
        )
        lines.append(
            f"  anchor        {check.anchor_offset} (expected {check.expected_anchor}, "
            f"delta {check.offset_delta:+d})" if check.offset_delta is not None else
            f"  anchor        {check.anchor_offset}"
        )
        lines.append(f"  samples       {check.sample_count} covered")
        for problem in check.problems:
            lines.append(f"  problem       {problem}")
    for warning in result.warnings:
        lines.append(f"warning         {warning}")
    lines.append("result          " + ("OK" if result.ok else "FAILED"))
    return "\n".join(lines)


def render_rebuild(
    result: RewriteResult,
    *,
    plan: SegmentPlan | None,
    source_check: CheckResult | None,
    rebuilt_check: CheckResult | None,
    as_json: bool,
    demo: bool = False,
) -> str:
    if as_json:
        if demo:
            payload = demo_payload(
                source=result.source,
                destination=result.destination,
                result=result,
                plan=plan,
                source_check=source_check,
                rebuilt_check=rebuilt_check,
            )
        else:
            payload = rebuild_payload(
                result,
                plan=plan,
                source_check=source_check,
                rebuilt_check=rebuilt_check,
            )
        return json.dumps(payload, indent=2, sort_keys=True)
    lines = [
        f"source          {result.source}",
        f"rebuilt         {result.destination}",
        f"size            {result.source_size} -> {result.destination_size} bytes",
        f"new sidx        offset {result.new_sidx_offset}, {result.new_sidx_size} bytes, "
        f"version {result.index_version}",
        f"samples         {result.samples} across {result.segments} segment(s)",
        f"layout          {result.layout_passes} pass(es), shift {result.offset_shift:+d} bytes",
    ]
    if source_check and source_check.primary_index:
        lines.append(
            f"old index       offset_ok={source_check.primary_index.offset_ok} "
            f"delta={source_check.primary_index.offset_delta:+d}"
        )
    if rebuilt_check and rebuilt_check.primary_index:
        lines.append(
            f"rebuilt index   offset_ok={rebuilt_check.primary_index.offset_ok} "
            f"delta={rebuilt_check.primary_index.offset_delta:+d}"
        )
    if plan is not None:
        lines.append("boundaries")
        for segment in plan.segments:
            lines.append(
                f"  [{segment.index}] samples {segment.sample_start + 1}-"
                f"{segment.sample_end} ({segment.sample_count}), "
                f"dts {segment.dts_start}, {segment.duration_ticks} ticks, "
                f"bytes {segment.byte_start}..{segment.byte_end}"
            )
    for warning in result.warnings:
        lines.append(f"warning         {warning}")
    ok = rebuilt_check.ok if rebuilt_check else True
    lines.append("result          " + ("OK" if ok else "FAILED"))
    return "\n".join(lines)
