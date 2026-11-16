"""Command line interface: argument parsing, pipeline wiring, exit codes."""

from __future__ import annotations

import argparse
import json
import sys

from .boxes import iter_boxes, parse_file
from .fixture import ALL_CASES, build_mp4
from .model import (
    DEFAULT_MAX_FILE_BYTES,
    MdatExtent,
    SidxmapError,
    UsageError,
)
from .rebuild import build_boundaries
from .sidx import compare_references, parse_sidx, rebuild_references
from .stbl import parse_tracks
from .validate import validate_sidx, validate_tracks


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="sidxmap",
        description="Offline ISO BMFF sidx/stbl byte-range manifest builder",
    )
    parser.add_argument("--max-file-bytes", type=int,
                        default=DEFAULT_MAX_FILE_BYTES,
                        help="reject input files larger than this (default 256 MiB)")
    sub = parser.add_subparsers(dest="command", required=True)

    fx = sub.add_parser("fixture", help="write a synthetic MP4 fixture")
    fx.add_argument("--case", required=True, choices=ALL_CASES)
    fx.add_argument("--out", required=True, help="output MP4 path")

    rb = sub.add_parser("rebuild", help="rebuild byte-range manifest for an MP4")
    rb.add_argument("path")
    rb.add_argument("--max-bytes", type=int, required=True,
                    help="maximum bytes per boundary")
    rb.add_argument("--max-duration", type=int, default=None,
                    help="optional maximum duration (track timescale units)")
    rb.add_argument("--json", dest="json_out", default=None,
                    help="write JSON manifest to this path")
    return parser


def collect_mdats(root_boxes) -> list[MdatExtent]:
    return [
        MdatExtent(offset=box.start + box.header_size, size=len(box.payload))
        for box in iter_boxes(root_boxes, b"mdat")
    ]


def run_rebuild(path: str, max_bytes: int, max_duration: int | None,
                json_out: str | None, max_file_bytes: int) -> int:
    if max_bytes <= 0:
        raise UsageError("--max-bytes must be a positive integer")
    if max_duration is not None and max_duration <= 0:
        raise UsageError("--max-duration must be a positive integer")
    if max_file_bytes <= 0:
        raise UsageError("--max-file-bytes must be a positive integer")
    root_boxes = parse_file(path, max_file_bytes=max_file_bytes)
    mdats = collect_mdats(root_boxes)
    tracks = parse_tracks(root_boxes, mdats)
    warnings = validate_tracks(tracks, mdats)
    declared = parse_sidx(root_boxes, mdats)
    validate_sidx(declared)
    boundaries = build_boundaries(tracks, max_bytes, max_duration)
    rebuilt_refs = rebuild_references(boundaries)
    checked_refs, mismatches = compare_references(declared, rebuilt_refs)
    index_status = "PASS" if mismatches == 0 else "FAIL"
    sample_entries = sum(t.sample_count for t in tracks)
    manifest = {
        "source": path,
        "track_count": len(tracks),
        "sample_entries": sample_entries,
        "index_offset_check": index_status,
        "checked_refs": checked_refs,
        "mismatches": mismatches,
        "reconstructed_boundaries": len(boundaries),
        "warnings": warnings,
        "boundaries": [b.as_dict() for b in boundaries],
    }
    if json_out is not None:
        try:
            with open(json_out, "w", encoding="utf-8") as handle:
                json.dump(manifest, handle, indent=2, sort_keys=True)
                handle.write("\n")
        except OSError as exc:
            raise UsageError(f"cannot write {json_out}: {exc}") from exc
    _print_summary(manifest, boundaries)
    return 3 if mismatches else 0


def _print_summary(manifest: dict, boundaries) -> None:
    print("sidxmap manifest summary")
    print(f"  track_count={manifest['track_count']}")
    print(f"  sample_entries={manifest['sample_entries']}")
    print(f"  index_offset_check={manifest['index_offset_check']}")
    print(f"  checked_refs={manifest['checked_refs']}")
    print(f"  mismatches={manifest['mismatches']}")
    print(f"  reconstructed_boundaries={manifest['reconstructed_boundaries']}")
    for warning in manifest["warnings"]:
        print(f"  WARNING: {warning}")
    print("  boundaries:")
    for b in boundaries:
        print(
            f"    track={b.track_id} index={b.index} offset={b.offset} "
            f"length={b.length} time=[{b.start_time},{b.end_time}) "
            f"samples=[{b.sample_first},{b.sample_last}] "
            f"sync={b.starts_with_sync}"
        )


def run_fixture(case: str, out: str) -> int:
    data = build_mp4(case)
    try:
        with open(out, "wb") as handle:
            handle.write(data)
    except OSError as exc:
        raise UsageError(f"cannot write {out}: {exc}") from exc
    print(f"wrote fixture case={case} bytes={len(data)} path={out}")
    return 0


def main(argv: list[str] | None = None) -> int:
    parser = build_parser()
    try:
        args = parser.parse_args(argv)
    except SystemExit as exc:
        # argparse exits 2 on usage errors; the tool contract reserves 2 for
        # invalid file structure and uses 4 for argument errors.
        code = exc.code if isinstance(exc.code, int) else 2
        return 4 if code == 2 else code
    try:
        if args.command == "fixture":
            return run_fixture(args.case, args.out)
        return run_rebuild(
            args.path,
            args.max_bytes,
            args.max_duration,
            args.json_out,
            args.max_file_bytes,
        )
    except SidxmapError as exc:
        print(f"sidxmap: error: {exc}", file=sys.stderr)
        return exc.exit_code


if __name__ == "__main__":
    sys.exit(main())
