"""Terminal entry point: ``python -m mkvmp4.cli <inspect|remux|verify>``.

Exit codes are part of the contract:

* 0 - success
* 2 - bad arguments / unusable option combination
* 3 - input structure or codec unsupported
* 4 - timestamp policy failure
* 5 - output write or index verification failure
"""

from __future__ import annotations

import argparse
import json
import sys
from typing import Any, Sequence

from .errors import EXIT_OK, MkvMp4Error, UsageError
from .pipeline import Mapping, RemuxOptions, inspect_file, remux_file, verify_file

PROG = "mkvmp4.cli"


def _build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog=PROG,
        description=(
            "Parse Matroska locally, rewrite timestamps and remux to MP4, with "
            "structural evidence printed as JSON or a terminal table."
        ),
    )
    sub = parser.add_subparsers(dest="command", required=True)

    inspect_p = sub.add_parser("inspect", help="summarise EBML structure and tracks")
    inspect_p.add_argument("input", help="Matroska input file")
    inspect_p.add_argument("--json", action="store_true", help="emit JSON evidence")
    inspect_p.add_argument(
        "--max-depth", type=int, default=64, help="maximum EBML nesting depth"
    )
    inspect_p.add_argument(
        "--max-elements",
        type=int,
        default=200_000,
        help="maximum number of EBML elements to parse",
    )

    remux_p = sub.add_parser("remux", help="rewrite timestamps and export MP4")
    remux_p.add_argument("input", help="Matroska input file")
    remux_p.add_argument("--out", required=True, help="MP4 output file")
    remux_p.add_argument(
        "--map",
        dest="mapping",
        default="",
        help="explicit SOURCE:TARGET track map, e.g. 1:1,2:2 (required)",
    )
    remux_p.add_argument(
        "--timescale", type=int, default=1_000_000, help="MP4 timescale in ticks/s"
    )
    remux_p.add_argument(
        "--require-map",
        action="store_true",
        help=(
            "abort unless --map covers every source track; without this flag an "
            "absent --map derives the identity table and reports it as derived"
        ),
    )
    remux_p.add_argument(
        "--offset-ms", type=int, default=0, help="uniform timestamp offset in ms"
    )
    remux_p.add_argument(
        "--repair",
        action="store_true",
        help="repair non-monotonic timestamps instead of failing",
    )
    remux_p.add_argument(
        "--rounding",
        choices=("round", "floor", "ceil"),
        default="round",
        help="rounding when converting to the MP4 timescale",
    )
    remux_p.add_argument(
        "--no-clip-negative",
        action="store_true",
        help="fail on negative timestamps instead of clipping them to zero",
    )
    remux_p.add_argument(
        "--max-gap-ms",
        type=int,
        default=None,
        help="strict mode: fail when consecutive samples differ by more than this",
    )
    remux_p.add_argument("--json", action="store_true", help="emit JSON evidence")

    verify_p = sub.add_parser("verify", help="check boxes, sample tables and offsets")
    verify_p.add_argument("input", help="MP4 file to verify")
    verify_p.add_argument("--json", action="store_true", help="emit JSON evidence")
    return parser


def _print_json(payload: Any) -> None:
    json.dump(payload, sys.stdout, indent=2, sort_keys=False)
    sys.stdout.write("\n")


def _print_inspect(report: dict[str, Any]) -> None:
    out = sys.stdout
    out.write(f"file: {report['path']} ({report['file_size']} bytes)\n")
    out.write(
        f"doc_type: {report['doc_type']}  TimestampScale: "
        f"{report['timestamp_scale_ns']} ns\n"
    )
    tree = report["tree"]
    out.write(
        f"elements: {report['element_count']}  max_depth: {tree['max_depth']}  "
        f"unknown_size: {tree['unknown_sizes']}\n"
    )
    out.write(f"tracks: {report['track_count']}  clusters: {report['cluster_count']}  "
              f"blocks: {report['block_count']}\n")
    out.write("\n-- tracks --\n")
    for track in report["tracks"]:
        detail = f"  codec={track['codec_id']} supported={track['codec_supported']}"
        if track["pixel_width"]:
            detail += f" {track['pixel_width']}x{track['pixel_height']}"
        if track["channels"]:
            detail += f" {track['channels']}ch @ {track['sampling_frequency']}Hz"
        out.write(f"  #{track['number']} {track['type']}{detail}\n")
    out.write("\n-- Segment / Cluster byte ranges --\n")
    for node in report["segments_and_clusters"]:
        flag = " (unknown size)" if node["unknown_size"] else ""
        out.write(
            f"  L{node['level']} {node['name']} [{node['offset']}, "
            f"{node['end_offset']}) {node['size']} bytes{flag}\n"
        )
    out.write("\n-- elements --\n")
    for node in tree["nodes"]:
        indent = "  " * node["level"]
        size = "unknown" if node["unknown_size"] else str(node["size"])
        out.write(
            f"  {indent}{node['name']} {node['id_hex']} "
            f"[{node['offset']}, {node['end_offset']}) size={size}\n"
        )


def _print_remux(report: dict[str, Any], *, json_mode: bool) -> None:
    if json_mode:
        _print_json(report)
        return
    out = sys.stdout
    out.write(f"output: {report['output']} ({report['file_size']} bytes)\n")
    out.write(f"timescale: {report['timescale']}\n")
    out.write("\n-- track map --\n")
    if report.get("mapping_derived"):
        out.write(
            "  (derived: no --map given, source numbers reused as target track IDs; "
            "pass --map or --require-map to pin them)\n"
        )
    for track in report["tracks"]:
        out.write(
            f"  source #{track['source_track']} -> track {track['track_id']} "
            f"{track['handler']} {track['codec_id']} ({track['sample_entry']}) "
            f"samples={track['samples']}\n"
        )
    corrections = report["corrections"]
    out.write(
        f"\nsamples: {report['sample_count']}  first_tick: {report['first_tick']}  "
        f"last_end_tick: {report['last_end_tick']}\n"
    )
    out.write(
        "corrections: clipped={clipped} rounded={rounded} "
        "non_monotonic={non_monotonic} zero_duration={zero_duration} "
        "gaps={gaps} last_duration_fallback={last_duration_fallback}\n".format(
            **corrections
        )
    )
    out.write(f"boxes: {report['box_count']}  index_ok: {report['index_ok']}\n")
    if report.get("mapping_derived"):
        out.write(
            "note: mapping was derived; pass --map SOURCE:TARGET to pin target IDs\n"
        )


def _print_verify(report: dict[str, Any], *, json_mode: bool) -> None:
    if json_mode:
        _print_json(report)
        return
    out = sys.stdout
    out.write(f"file: {report['path']} ({report['file_size']} bytes)\n")
    out.write(f"sha256: {report['sha256']}\n")
    out.write(f"boxes: {report['box_count']}  samples: {report['sample_count']}\n")
    for box in report["boxes"]:
        out.write(f"  {box['type']} [{box['offset']}, {box['offset'] + box['size']})\n")
    out.write("\n-- tracks --\n")
    for track in report["tracks"]:
        out.write(
            f"  track {track['track_id']} {track['handler']} timescale="
            f"{track['timescale']} samples={track['sample_count']} "
            f"chunks={track['chunks']} ({track['chunk_box']})\n"
        )
    out.write(
        f"\nindex_ok: {report['index_ok']}  failures: {len(report['failures'])}\n"
    )
    for failure in report["failures"]:
        out.write(f"  ! {failure}\n")


def main(argv: Sequence[str] | None = None) -> int:
    parser = _build_parser()
    args = parser.parse_args(argv)
    try:
        if args.command == "inspect":
            report = inspect_file(
                args.input, max_depth=args.max_depth, max_elements=args.max_elements
            )
            if args.json:
                _print_json(report)
            else:
                _print_inspect(report)
            return EXIT_OK
        if args.command == "remux":
            mapping = Mapping.parse(args.mapping)
            options = RemuxOptions(
                output=args.out,
                mapping=mapping,
                timescale=args.timescale,
                offset_ms=args.offset_ms,
                repair=args.repair,
                rounding=args.rounding,
                clip_negative=not args.no_clip_negative,
                max_gap_ms=args.max_gap_ms,
                derive_mapping=not args.require_map,
            )
            result = remux_file(args.input, options)
            _print_remux(result.to_dict(), json_mode=args.json)
            return EXIT_OK
        if args.command == "verify":
            report = verify_file(args.input)
            _print_verify(report, json_mode=args.json)
            return EXIT_OK
        raise UsageError(f"unknown command {args.command!r}")
    except MkvMp4Error as exc:
        payload = exc.to_dict()
        payload["command"] = args.command
        _print_json(payload)
        return exc.exit_code


if __name__ == "__main__":  # pragma: no cover - exercised via subprocess tests
    sys.exit(main())
