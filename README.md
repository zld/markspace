# frame-audit

Local, dependency-light diagnostics for video timelines **as they decode**, not as
the container claims them to be.

Outsourced cutscene media routinely carries variable frame rates, B-frame
reordering, missing or duplicated PTS values, and discontinuities in the time
base. The container-level frame count and duration cannot describe the real
decoded order, so a build tool that trusts them can produce dropped frames or
stalls on the in-game timeline. `frame-audit` decodes every frame locally,
recovers a monotonic timeline from the stream time base, and reports what it had
to repair.

## Install

```sh
python3.11 -m venv .venv
.venv/bin/python -m pip install av==12.3.0 numpy==2.1.3 pytest==8.3.3
```

The repository ships a thin import shim (`frame_audit/`) so the CLI can be run
straight from a checkout without an editable install:

```sh
.venv/bin/python -m frame_audit.cli audit .tmp/sample.mp4 --json-out .tmp/report.jsonl
```

## Fixtures

Sample media is generated locally; no external asset files are referenced.

```sh
.venv/bin/python tools/make_fixture.py \
    --out .tmp/sample.mp4 --frames 96 --gop 24 --b-frames 2 --inject-missing-pts 3
```

## Commands

```
frame-audit audit PATH... [options]
```

* `PATH` may be a single video file or a directory (batch mode).
  Directories are scanned recursively in relative-path order; non-video files
  are skipped and the reason is reported.
* `--json-out FILE` writes one JSON object per audited file (JSON Lines).
  Terminal output and JSON output are produced from the **same** repaired
  record set.
* `--max-frames N` caps decoding. When the cap is reached the result is marked
  `truncated: true`; it is never reported as a full scan.
* `--on-timestamp-error repair|report` selects whether anomalies are repaired
  into a monotonic timeline or only reported.
* `--json` prints the JSON Lines document to stdout instead of the table.

## Exit codes

| Code | Meaning |
|------|---------|
| 0 | Every requested input scanned successfully |
| 1 | Input missing / unreadable / undecodable |
| 2 | Invalid command-line arguments |
| 3 | Output file could not be written |

A per-file failure in batch mode is recorded in that file's entry and does not
erase the results of other files.

## Output fields

`decoded_frames`, `i_frames`, `p_frames`, `b_frames`, `repaired_timestamps`,
`max_timestamp_jump_us`, `truncated`, plus the original and repaired PTS for
every frame in the JSON Lines output.
