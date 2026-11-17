# fmp4tool

Local fragmented-MP4 (ISO BMFF) index, validation and repack tool. It reads a
manifest of fragmented MP4 shards plus an init segment, builds a shared index,
validates structure and track compatibility, and can repack the shards in
manifest order while rewriting `mfhd` sequence numbers into a contiguous run.
Everything runs locally; media samples are never decoded or transcoded, only
unencrypted fragmented MP4 is handled.

## Build

```sh
brew install cmake nlohmann-json
cmake -S . -B build -DCMAKE_BUILD_TYPE=Release
cmake --build build --parallel
ctest --test-dir build --output-on-failure
```

## Commands

```sh
# Generate a two-track (video 90000/avc1 + audio 48000/mp4a) dataset.
fmp4tool fixture --dir DIR [--segments N] [--mixed-versions]
                 [--mfhd-gap-at SEQ] [--omit-file-for-seq SEQ]
                 [--corrupt-length-at SEQ] [--incompatible-init]

# Repack: rewrite mfhd to 1..N, preserve every other byte, verify, atomic commit.
fmp4tool repack --input DIR --output DIR [--json report.json]

# Verify a directory standalone or against a baseline init/timeline.
fmp4tool verify --input DIR [--baseline DIR] [--json report.json]
```

### Exit codes

| Code | Meaning |
|------|---------|
| 0 | success |
| 2 | parameter or file/path error |
| 3 | structural error, missing fragment, or compatibility failure |
| 4 | output commit failure |

## Manifest format (`fragments.manifest`)

```json
{
  "init_segment": "init.mp4",
  "fragments": [
    {"sequence": 1, "path": "frag-1.m4s"},
    {"sequence": 2, "path": "frag-2.m4s"}
  ]
}
```

Manifest paths must be relative and stay inside the directory root; absolute
paths, `..` traversal, in-tree symlinks and input/output directory overlap are
rejected.

## Architecture

```
include/fmp4/
  box_reader.hpp      bounded box parsing (32-bit/extended size, unknown skip)
  fragment_index.hpp  init + fragment index model shared by all stages
  validator.hpp       issues, mfhd continuity, per-track timeline deltas
  repacker.hpp        streaming repack with temp-dir atomic commit
  fixture_writer.hpp  synthetic dataset generator
  safe_path.hpp       traversal / symlink / overlap protection
  errors.hpp          Fmp4Error (2) / StructuralError (3) / RepackError (3,4)
src/
  box_reader.cpp      parent-boundary, overflow and truncation checks
  fragment_index.cpp  manifest loading and index assembly
  fragment_parse.cpp  mfhd/tfhd/tfdt(v0,v1)/trun(v0,v1), trun->tfhd->trex
  validator.cpp       validation and baseline comparison
  repacker.cpp        64 KiB streaming copy, 4-byte mfhd patch, re-verify
  fixture_writer.cpp  local sample data generation
  cli.cpp, cli_verify.cpp  terminal entry points
tests/fmp4_tests.cpp CTest unit tests (parsing, index, validation, repack)
```

The index is the single structure consumed by both the validator and the
repacker. Repack output is written to a hidden temporary directory, re-indexed
and re-validated, and only renamed into place after every check passes; on any
failure the temporary tree is removed so no partial output remains.

Sample durations resolve per ISO 14496-12: `trun` per-sample duration, then
`tfhd` default, then `trex` default. `trun` v1 signed composition-time offsets
are parsed and range-checked; decode timelines use `tfdt` base media decode
time (v0 u32 or v1 u64).
