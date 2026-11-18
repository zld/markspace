"""Command line interface: inspect, validate and deterministic fixtures.

Exit codes (stable contract):
  0 - every input file passed validation
  1 - one or more files parsed but failed content validation
  2 - argument error, unreadable file or structural parse failure
A failing file in a batch never aborts processing of the remaining files.
"""

from __future__ import annotations

import argparse
import json
import os
import sys
from dataclasses import dataclass

from .errors import MediaIndexError
from .pipeline import (
    PipelineError,
    inspect_summary,
    report_parse_failure,
    run_pipeline,
)

# Deterministic fixtures are materialized here on first CLI use.
DEFAULT_GEN_DIR = os.path.join(".venv", "generated")
_VALID_NAME = "valid.mp4"
_NESTED_NAME = "nested.mp4"
_EMPTY_NAME = "empty.mp4"
_BAD_NAMES = (
    "bad_misaligned.mp4",
    "bad_oversized.mp4",
    "bad_duplicate_offset.mp4",
    "bad_truncated_field.mp4",
    "bad_truncated_header.mp4",
    "bad_timescale.mp4",
    "bad_cycle.mp4",
)


@dataclass
class BatchOutcome:
    bodies: list[dict]
    exit_code: int


def generate_fixtures(out_dir: str) -> dict[str, str]:
    """Write deterministic fixtures unless they already exist (idempotent)."""

    from .fixtures import (
        build_empty,
        build_nested,
        build_valid,
        inject_bad_timescale,
        inject_cycle,
        inject_duplicate_chunk_offset,
        inject_misaligned,
        inject_oversized_reference,
        inject_truncated_field,
        inject_truncated_header,
    )
    from .fixture_writer import ensure_dir, write_if_missing

    ensure_dir(out_dir)
    valid = build_valid()
    nested = build_nested()
    written: dict[str, str] = {}
    plan = {
        _VALID_NAME: valid,
        _NESTED_NAME: nested,
        _EMPTY_NAME: build_empty(),
        _BAD_NAMES[0]: inject_misaligned(valid),
        _BAD_NAMES[1]: inject_oversized_reference(valid),
        _BAD_NAMES[2]: inject_duplicate_chunk_offset(valid),
        _BAD_NAMES[3]: inject_truncated_field(valid),
        _BAD_NAMES[4]: inject_truncated_header(valid),
        _BAD_NAMES[5]: inject_bad_timescale(valid),
        _BAD_NAMES[6]: inject_cycle(nested),
    }
    for name, blob in plan.items():
        path = os.path.join(out_dir, name)
        write_if_missing(path, bytes(blob))
        written[name] = path
    return written


def _run_one(path: str) -> tuple[dict, int]:
    try:
        result = run_pipeline(path)
    except MediaIndexError as exc:
        return report_parse_failure(path, exc), 2
    body = result.report.to_dict()
    return body, (0 if result.report.failed == 0 else 1)


def _run_batch(paths: list[str], as_json: bool) -> BatchOutcome:
    bodies: list[dict] = []
    exit_code = 0
    for path in sorted(paths):
        if not os.path.exists(path):
            body, code = _missing_body(path), 2
        else:
            body, code = _run_one(path)
        bodies.append(body)
        exit_code = max(exit_code, code)
        if not as_json:
            _print_text(body, code)
    return BatchOutcome(bodies, exit_code)


def _missing_body(path: str) -> dict:
    from .validate import CHK_BOX_STRUCTURE, STATUS_FAIL, ValidationReport

    report = ValidationReport(path=path)
    report.add(CHK_BOX_STRUCTURE, STATUS_FAIL, -1, "file not found")
    return report.to_dict()


def _print_text(body: dict, code: int) -> None:
    head = "PASS" if code == 0 else ("FAIL" if code == 1 else "ERROR")
    print(f"[{head}] {body['path']}")
    print(f"  samples: {body['sample_entries']}  "
          f"offset checks pass/fail: "
          f"{body['index_offset_checks']['passed']}/"
          f"{body['index_offset_checks']['failed']}  "
          f"segments: {len(body['segment_boundaries'])}  errors: {len(body['errors'])}")
    for boundary in body["segment_boundaries"]:
        print(f"  segment {boundary['order']}: bytes "
              f"[{boundary['byte_start']}, {boundary['byte_end']}) "
              f"samples {boundary['first_sample']}..{boundary['last_sample']} "
              f"complete={boundary['complete']}")
    for error in body["errors"]:
        print(f"  - {error}")


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="media-index",
        description="Parse and cross-validate ISO BMFF sidx/stbl media indexes.",
    )
    sub = parser.add_subparsers(dest="command", required=True)

    p_validate = sub.add_parser("validate", help="validate one or more files")
    p_validate.add_argument("--input", action="append", required=True,
                            help="input file (repeatable for batch mode)")
    p_validate.add_argument("--json", action="store_true",
                            help="emit stable JSON instead of text")
    p_validate.add_argument("--gen-dir", default=DEFAULT_GEN_DIR,
                            help="fixture directory generated on first run")

    p_inspect = sub.add_parser("inspect", help="print parsed index summary")
    p_inspect.add_argument("--input", action="append", required=True)
    p_inspect.add_argument("--json", action="store_true")
    p_inspect.add_argument("--gen-dir", default=DEFAULT_GEN_DIR)

    p_fixtures = sub.add_parser("fixtures", help="generate deterministic fixtures")
    p_fixtures.add_argument("--gen-dir", default=DEFAULT_GEN_DIR)
    return parser


def _inspect_one(path: str) -> tuple[dict, int]:
    try:
        result = run_pipeline(path)
    except MediaIndexError as exc:
        return report_parse_failure(path, exc), 2
    return inspect_summary(result), 0


def main(argv: list[str] | None = None) -> int:
    parser = build_parser()
    args = parser.parse_args(argv)
    try:
        # First-run deterministic fixture generation, never overwriting files.
        generate_fixtures(args.gen_dir)
        if args.command == "fixtures":
            print(f"fixtures ready in {os.path.abspath(args.gen_dir)}")
            return 0
        if args.command == "inspect":
            outcome_code = 0
            summaries = []
            for path in sorted(args.input):
                if not os.path.exists(path):
                    body, code = _missing_body(path), 2
                else:
                    body, code = _inspect_one(path)
                summaries.append(body)
                outcome_code = max(outcome_code, code)
                if not args.json:
                    print(json.dumps(body, indent=2, sort_keys=True))
            if args.json:
                print(json.dumps(summaries, indent=2, sort_keys=True))
            return outcome_code
        outcome = _run_batch(args.input, args.json)
        if args.json:
            print(json.dumps(outcome.bodies, indent=2, sort_keys=True))
        return outcome.exit_code
    except MediaIndexError as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 2
    except Exception as exc:  # defensive: never leak a traceback to users
        print(f"error: unexpected failure: {exc.__class__.__name__}: {exc}",
              file=sys.stderr)
        return 2


if __name__ == "__main__":
    raise SystemExit(main())
