"""Command-line entry point: ``python -m cutline.cli <command>``.

Commands
--------
``demo``       generate a fixture, resolve it and print/emit a summary
``validate``   load an exchange document and report model validity
``resolve``    resolve overlaps and report intervals, delta and render samples
``export-otio``  write resolved material as an OTIO JSON timeline

Exit codes: ``0`` ok, ``2`` bad arguments or exchange format, ``3`` model,
resolution or export feasibility failure.  Diagnostics always go to stderr.
"""

from __future__ import annotations

import argparse
import json
import os
import sys
import tempfile
from pathlib import Path
from typing import Any

from . import exchange
from .errors import CutlineError, ExchangeError
from .model import Sequence
from .overlap import scan_sequence
from .render import RenderSummary, render
from .resolve import STRATEGIES, ResolutionResult, resolve


def _guard_outputs(args: argparse.Namespace) -> None:
    """Refuse to overwrite the input document with an output file."""

    outputs = [getattr(args, "output", None)]
    source = getattr(args, "input", None)
    if not source:
        return
    source_path = Path(source).resolve()
    for output in outputs:
        if output and Path(output).resolve() == source_path:
            raise ExchangeError(f"refusing to overwrite input file {source!r}")


def _atomic_write_text(text: str, target: Path) -> None:
    target.parent.mkdir(parents=True, exist_ok=True)
    fd, temp_name = tempfile.mkstemp(prefix=f".{target.name}.", suffix=".tmp", dir=target.parent)
    try:
        with os.fdopen(fd, "w", encoding="utf-8") as handle:
            handle.write(text)
            handle.flush()
            os.fsync(handle.fileno())
        os.replace(temp_name, target)
    except BaseException:
        try:
            os.unlink(temp_name)
        except FileNotFoundError:
            pass
        raise


def _emit(payload: dict[str, Any], as_json: bool, output: str | None) -> None:
    text = json.dumps(payload, indent=2, ensure_ascii=False) if as_json else _human(payload)
    if output:
        _atomic_write_text(text + "\n", Path(output))
    else:
        print(text)


def _human(payload: dict[str, Any]) -> str:
    lines = [f"{key}: {value}" for key, value in payload.items() if key != "samples"]
    samples = payload.get("samples") or []
    if samples:
        lines.append("samples:")
        for sample in samples[:10]:
            lines.append(
                f"  {sample['index']:>5}  {sample['track_id']:<4} {sample['clip_id']:<16} "
                f"src={sample['source_frame']:<6} fade={sample['fade_weight'][0]}/"
                f"{sample['fade_weight'][1]}"
            )
        if len(samples) > 10:
            lines.append(f"  ... {len(samples) - 10} more")
    return "\n".join(lines)


def _load(path: str) -> Sequence:
    return exchange.load(path)


def _resolve_flow(sequence: Sequence, strategy: str, *, step: int,
                  include_samples: bool) -> tuple[ResolutionResult, RenderSummary, dict[str, Any]]:
    result = resolve(sequence, strategy)
    summary = render(result.sequence, step=step)
    payload: dict[str, Any] = {
        "conflict_count": result.conflicts_before,
        "remaining_conflicts": result.conflicts_after,
        "resolved_intervals": [interval.to_json() for interval in result.intervals],
        "interval_count": len(result.intervals),
        "duration_before_frames": result.duration_before,
        "duration_after_frames": result.duration_after,
        "duration_delta_frames": result.duration_delta_frames,
        "render_sample_count": summary.sample_count,
        "render": summary.to_json(include_samples=include_samples),
    }
    if not include_samples:
        payload["render"].pop("samples", None)
    return result, summary, payload


def _cmd_validate(args: argparse.Namespace) -> int:
    sequence = _load(args.input)
    report = scan_sequence(sequence)
    payload = {
        "valid": True,
        "sequence_id": sequence.id,
        "frame_rate": str(sequence.frame_rate),
        "track_count": len(sequence.tracks),
        "clip_count": sum(len(track.clips) for track in sequence.tracks),
        "duration_frames": sequence.duration,
        "conflict_count": report.conflict_count,
        "conflicts": [conflict.to_json() for conflict in report.conflicts],
    }
    _emit(payload, args.json, args.output)
    return 0


def _cmd_resolve(args: argparse.Namespace) -> int:
    sequence = _load(args.input)
    _, _, payload = _resolve_flow(
        sequence, args.strategy, step=args.step, include_samples=not args.no_samples
    )
    _emit(payload, args.json, args.output)
    return 0


def _cmd_demo(args: argparse.Namespace) -> int:
    sys.path.insert(0, str(Path(__file__).resolve().parents[2] / "scripts"))
    from make_fixture import build_sequence  # local generator, no media access

    sequence = build_sequence()
    if args.input:
        sequence = _load(args.input)
    _, _, payload = _resolve_flow(
        sequence, args.strategy, step=args.step, include_samples=not args.no_samples
    )
    payload["fixture"] = "built-in"
    _emit(payload, args.json, args.output)
    return 0


def _cmd_export_otio(args: argparse.Namespace) -> int:
    from .otio_bridge import to_otio, verify_round_trip

    sequence = _load(args.input)
    if args.resolve:
        sequence = resolve(sequence, args.strategy).sequence
    timeline = to_otio(sequence)
    infos = verify_round_trip(sequence, timeline)
    if args.output:
        import opentimelineio as otio

        target = Path(args.output)
        target.parent.mkdir(parents=True, exist_ok=True)
        # The temp name keeps the target suffix so OTIO resolves its adapter.
        fd, temp_name = tempfile.mkstemp(prefix=f".{target.name}.", suffix=target.suffix,
                                         dir=target.parent)
        os.close(fd)
        try:
            otio.adapters.write_to_file(timeline, temp_name)
            os.replace(temp_name, target)
        except BaseException:
            try:
                os.unlink(temp_name)
            except FileNotFoundError:
                pass
            raise
    payload = {
        "track_count": len(timeline.tracks),
        "tracks": [info.to_json() for info in infos],
        "output": str(args.output),
        "resolved": bool(args.resolve),
    }
    print(json.dumps(payload, indent=2, ensure_ascii=False) if args.json
          else f"wrote {args.output} ({len(infos)} tracks)")
    return 0


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(prog="cutline", description=__doc__.splitlines()[0])
    sub = parser.add_subparsers(dest="command", required=True)

    def common(p: argparse.ArgumentParser) -> None:
        p.add_argument("--json", action="store_true", help="emit JSON instead of text")
        p.add_argument("--output", help="write the summary to this path")

    validate = sub.add_parser("validate", help="validate an exchange document")
    validate.add_argument("--input", required=True)
    common(validate)
    validate.set_defaults(func=_cmd_validate)

    res = sub.add_parser("resolve", help="resolve overlaps for an exchange document")
    res.add_argument("--input", required=True)
    res.add_argument("--strategy", choices=STRATEGIES, default="priority")
    res.add_argument("--step", type=int, default=1, help="render sampling stride")
    res.add_argument("--no-samples", action="store_true", help="omit per-frame samples")
    common(res)
    res.set_defaults(func=_cmd_resolve)

    demo = sub.add_parser("demo", help="run the built-in fixture through resolution")
    demo.add_argument("--input", help="optional exchange document to use instead")
    demo.add_argument("--strategy", choices=STRATEGIES, default="priority")
    demo.add_argument("--step", type=int, default=1)
    demo.add_argument("--no-samples", action="store_true")
    common(demo)
    demo.set_defaults(func=_cmd_demo)

    exp = sub.add_parser("export-otio", help="export an OTIO JSON timeline")
    exp.add_argument("--input", required=True)
    exp.add_argument("--output", required=True)
    exp.add_argument("--strategy", choices=STRATEGIES, default="priority")
    exp.add_argument("--resolve", action="store_true", help="resolve overlaps before export")
    exp.add_argument("--json", action="store_true", help="print a JSON summary")
    exp.set_defaults(func=_cmd_export_otio)
    return parser


def main(argv: list[str] | None = None) -> int:
    parser = build_parser()
    args = parser.parse_args(argv)
    try:
        _guard_outputs(args)
        return args.func(args)
    except CutlineError as exc:
        print(f"cutline: {exc.render() if hasattr(exc, 'render') else exc}", file=sys.stderr)
        return exc.exit_code
    except OSError as exc:
        print(f"cutline: {exc}", file=sys.stderr)
        return 2


if __name__ == "__main__":
    raise SystemExit(main())
