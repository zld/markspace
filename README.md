# h264scan — from-scratch H.264 (Annex B) bitstream structure analyzer

A dependency-light command line tool that inspects raw H.264 Elementary
Streams produced by external vendors and emits a reviewable summary of
parameter sets, frame-type distribution and Group-of-Picture boundaries.

It does **not** decode pixels and does **not** use FFmpeg or any external
codec: the bit reader, Exp-Golomb decoder, NAL scanner, SPS/PPS and slice
parsers and access-unit assembler are all implemented in this repository.

## Requirements

- macOS (Apple Silicon or Intel), a C++17 compiler (Apple Clang)
- CMake 3.28 or newer
- Catch2 3.x (tests only)

```sh
brew install cmake catch2
```

## Build, generate a sample, scan and test

```sh
cmake -S . -B build -DCMAKE_BUILD_TYPE=Release
cmake --build build
./build/tools/fixture_gen --output /tmp/gop_fixture.h264
./build/tools/h264scan --input /tmp/gop_fixture.h264 --json --strict
ctest --test-dir build --output-on-failure
```

A convenience wrapper runs the whole sequence:

```sh
./scripts/acceptance.sh
```

The fixture generator prints a byte count in the 1,000–10,000 range. The
sample contains multiple SPS/PPS, two GOPs, multi-slice pictures, B frames
and an interlaced field pair.

## Command line

```
h264scan --input FILE [--json] [--strict] [--max-nal-bytes N]
```

| Flag | Meaning |
| --- | --- |
| `--input FILE` | Annex B `.h264` file (required) |
| `--json` | Emit machine-readable JSON instead of the text report |
| `--strict` | Return exit code 2 if any bitstream diagnostic was recorded |
| `--max-nal-bytes N` | Cap one NAL payload at N bytes (64 .. 1 GiB; default 16 MiB) |

Diagnostics always go to **stderr**; the summary always goes to **stdout**,
even when errors are present, so automated pipelines can parse both.

### Exit codes

| Code | Meaning |
| --- | --- |
| 0 | Success, no bitstream errors |
| 2 | Strict mode found one or more bitstream errors |
| 64 | Invalid command-line arguments |
| 70 | Input/output failure (missing file, read or write error) |

## What is reported

- Every NAL: absolute file offsets (start code and header byte), type,
  reference idc and raw size. Three- and four-byte start codes are both
  accepted; empty NALs and trailing bytes without a start code are errors.
- SPS: profile/level, chroma format, bit depth, cropped width/height,
  frame/field coding flag. Cropping is validated against chroma sampling and
  rejected if it removes the whole picture.
- PPS and slice headers: slices are joined by `frame_num`, IDR flag, field
  flags and parameter-set ids. Multiple slices of one picture count once.
- Frame statistics: I/P/B counts and percentages, separate frame- vs
  field-picture totals; unsupported slice types go to an `unknown` bucket.
- GOPs: each IDR starts a group at the file offset of its first NAL;
  pictures before the first IDR are reported separately and the last group is
  marked unclosed when no later IDR terminates it.

## Streaming and memory

The file is read in fixed 64 KiB chunks. The NAL scanner keeps only
byte-sized state and the single NAL currently being parsed; the summary
retains lightweight per-NAL metadata (not RBSP payloads). Memory use is
independent of total file length, and size/offset arithmetic is range
checked against 64-bit overflow.

## Layout

```
src/bit_reader.hpp       bounds-checked big-endian bit reader + ParseError
src/bit_writer.hpp       bit/Exp-Golomb writer and EBSP escaper (fixtures)
src/exp_golomb.hpp       ue(v) / se(v) decoder
src/nal_parser.hpp       streaming Annex B scanner, start codes, EPB removal
src/parameter_sets.*     SPS/PPS parsing and the parameter set cache
src/slice_parser.*       slice header extraction and picture identity
src/access_unit.*        slice-to-picture assembly, stats, GOP boundaries
src/summary_writer.*     text and JSON rendering
src/main.cpp             CLI driver, streaming loop, exit codes
tools/fixture_builder.hpp deterministic stream building blocks
tools/generate_fixture.cpp fixture generator (clean and --corrupt streams)
tests/                   Catch2 unit and integration tests
```
