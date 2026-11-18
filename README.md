# mp4_repack

A repeatable, self-contained repackager for fragmented ISO BMFF (fMP4) media.
It reads an initialization segment plus media fragments produced by different
encoders, validates their compatibility, and emits a fresh initialization
segment and a run of continuously numbered media fragments whose sample bytes
and per-track decode order are unchanged.

No FFmpeg or other media tool is invoked: every box is parsed and rewritten by
this codebase. Sample data is copied byte-for-byte; only structural boxes
(`mfhd`, `tfhd`, `tfdt`, `trun`) are regenerated.

## Build

```sh
brew install cmake catch2
cmake -S . -B build -DCMAKE_BUILD_TYPE=Release
cmake --build build --parallel
```

## One-command demonstration

```sh
./scripts/demo.sh
```

This builds everything, regenerates deterministic fixtures, repacks the
healthy set, exercises every fault fixture, verifies idempotency and runs the
test suite.

## Manual run

```sh
./build/make_fixtures --out build/sample
./build/mp4_repack --input build/sample/input --output build/sample/output --json
ctest --test-dir build --output-on-failure
```

## Exit codes

| Code | Meaning                                                        |
|------|----------------------------------------------------------------|
| 0    | success                                                        |
| 2    | parameter or file error (bad flags, missing input directory)   |
| 3    | structural or compatibility error; nothing is written          |
| 4    | repackaging or atomic write failure                            |

## Module layout

| File                        | Responsibility                                              |
|-----------------------------|-------------------------------------------------------------|
| `src/box_reader.*`          | box boundaries, 32/64-bit and size=0 boxes, nested tree     |
| `src/fragment_index*.{hpp,cpp}` | moov/trak/trex config and moof/traf/tfhd/tfdt/trun index |
| `src/compatibility.*`       | config drift, duplicate/out-of-order/missing sequences      |
| `src/rewriter*`             | continuous renumbering, timeline rewrite, atomic output     |
| `src/sha256.cpp`            | codec-configuration fingerprint                             |
| `src/main.cpp`              | CLI, exit-code mapping, human/JSON summaries                |
| `tools/fixture_builder*`    | deterministic in-memory fMP4 and fault fixtures             |
| `tests/`                    | Catch2 pipeline tests (24 cases)                            |

The compatibility checker, the CLI and the tests all consume the same
`MediaIndex`, so validation cannot drift from the data used for rewriting.

## What is validated

- normal, extended (`largesize`) and `size=0` boxes; truncated headers,
  out-of-range lengths and unknown critical nested boxes are rejected.
- `moov`, `moof`, `traf`, `tfhd`, `tfdt`, `trun`, `mdat` indexed by track and
  sequence.
- track id, timescale, codec configuration (raw `stsd` SHA-256) and trex/tfhd
  default sample parameters compared against the initialization segment.
- `tfdt` version 0 (32-bit) and version 1 (64-bit); all `trun` versions and
  optional fields, including negative composition offsets and default-value
  inheritance.
- per-track decode timelines accumulated from sample durations, detecting
  gaps, overlaps and integer overflow, with a maximum absolute deviation
  report in ticks.
- missing fragments reported with their expected sequence and the previous
  and next actual sequences.
- encryption boxes (`senc`, `saiz`, `saio`, `pssh`, `encv`/`enca`/...) are
  refused explicitly; protected media is never "repaired".

## Safety limits

Configurable through `Limits` / `--max-samples` / `--max-tracks`:

- single file capped at 2 GiB,
- maximum sample count and track count bounded,
- file offsets, box lengths and time arithmetic use overflow checks
  (`checked_add`, bounded cursors and 32-bit output size validation).

## Output integrity

Every output file is first written to a unique temp file, re-read and
re-parsed. Only after all temps verify are targets swapped atomically, with
the previous file backed up during the swap. A failed verification leaves the
existing target untouched, and temp/backup leftovers from interrupted runs
are cleaned on the next run.
