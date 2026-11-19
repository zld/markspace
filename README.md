# h264-inspect

A from-scratch structural analyzer for H.264/AVC Annex B elementary streams.
It reports sequence parameter sets, frame and field composition, access-unit
boundaries, and image-group start offsets, with every finding traceable to a
byte offset in the input file. It does not decode pixels and does not handle
containers.

## Build

```sh
brew install cmake          # if cmake is not already present
cmake -S . -B build -DCMAKE_BUILD_TYPE=Release
cmake --build build --parallel
ctest --test-dir build --output-on-failure
```

Requirements: C++17 compiler (Clang 15+), CMake 3.25+.

## Usage

```sh
# Generate the bundled reference sample, then analyze it.
./build/h264-inspect --generate-sample /tmp/sample.h264
./build/h264-inspect analyze /tmp/sample.h264 --format json

# Interlaced sample exercising field pairing.
./build/h264-inspect --generate-interlaced-sample /tmp/interlaced.h264
./build/h264-inspect analyze /tmp/interlaced.h264

# Text report for any Annex B stream.
./build/h264-inspect analyze some_stream.h264 --format text
```

Exit codes:

| Code | Meaning                                              |
|------|------------------------------------------------------|
| 0    | Analysis completed without recoverable stream errors |
| 1    | Bad arguments or unreadable input file               |
| 2    | Analysis completed, but recoverable errors were found |

Recoverable errors never abort the run: the analyzer keeps any successfully
parsed state, attaches the byte offset and error category, and continues with
the next NAL unit. An unreadable file terminates with exit code 1.

## Reference sample

`--generate-sample` writes a deterministic 320x192 high-profile stream:

| Property            | Value                     |
|---------------------|---------------------------|
| Image groups        | 1                         |
| Frames (I / P / B)  | 1 / 1 / 1                 |
| Slices              | 3                         |
| Group start offset  | 0                         |
| Interlaced flag     | `frame_mbs_only_flag: true` (progressive) |

The generated stream is fully self-contained; no external media is required.

## JSON schema

The JSON report contains these top-level sections:

- `sps` — one entry per parsed SPS occurrence: `id`, `profile_idc`,
  `level_idc`, `chroma_format_idc`, bit depths, cropped and coded dimensions,
  `frame_mbs_only_flag`, `interlaced`, and the carrying NAL's byte offset.
- `pps` — PPS id, referenced SPS id, entropy mode, and byte offset.
- `frames` — totals split by picture type (`i`/`p`/`b`/`unknown`), a separate
  `fields` block with `pairs` and `isolated`, plus per-frame items carrying
  `type`, `idr`, `reference`, field flags, `frame_num`, the macroblock span
  (`first_mb_min`/`first_mb_max`), slice count, byte offset, an error flag, and
  an explicit `uncertainty` note where a boundary could not be decided
  confidently.
- `groups` — image groups with `start_nal_offset`, `first_frame_type`,
  `boundary` basis (`first_picture`, `idr_picture`, `parameter_set_change`),
  frame/field/slice counts, and `complete`/`uncertain`.
- `exp_golomb` — `ue_calls`, `se_calls`, their failure counts, maximum codeword
  widths, maximum leading-zero run, and maximum decoded values.
- `nal_scan` — `nal_units` count, total bytes, leading garbage/zero bytes,
  consecutive start codes, empty NAL units, and inter-NAL/trailing zero padding.
- `errors` — ordered list of `{kind, offset, message}` diagnostics.

## Architecture

| Path | Responsibility |
|------|----------------|
| `include/byte_source.hpp`, `src/byte_source.cpp` | Bounded whole-file read with absolute offsets |
| `include/bit_reader.hpp`, `src/bit_reader.cpp` | RBSP emulation-byte removal, bounded bit reads, trailing-bit validation |
| `include/exp_golomb.hpp`, `src/exp_golomb.cpp` | `ue(v)`/`se(v)` with shared call/failure/width statistics |
| `include/nal_parser.hpp`, `src/nal_parser.cpp`, `src/nal_types.cpp` | Annex B start-code scanning, NAL boundaries and type names |
| `include/sps_types.hpp`, `include/sps_dimensions.hpp`, `src/sps_dimensions.cpp` | SPS field model, cropped dimensions, overflow and crop checks |
| `include/sps_parser.hpp`, `src/sps_parser.cpp` | SPS syntax decoding |
| `include/pps_parser.hpp`, `src/pps_parser.cpp` | PPS fields needed by slice headers |
| `include/slice_types.hpp`, `include/slice_parser.hpp`, `include/slice_header_lists.hpp` | Slice type names, slice header decoding, optional header lists |
| `include/param_set_store.hpp`, `src/param_set_store.cpp` | Active SPS/PPS definitions with content revisions |
| `include/boundary_rules.hpp`, `src/boundary_rules.cpp` | H.264 7.4.1.2.4 picture-boundary decisions |
| `include/frame_grouping.hpp`, `src/frame_grouping.cpp` | Picture-to-frame folding and image-group splitting |
| `include/access_unit.hpp`, `src/access_unit.cpp` | Stream driver tying scanning, parsing and grouping together |
| `include/report*.hpp`, `src/report*.cpp` | Summary counts plus text and JSON rendering |
| `include/sample_syntax.hpp`, `include/sample_generator.hpp` | Deterministic sample streams |
| `src/main.cpp` | CLI entry point and exit codes |
| `tests/test_*.cpp` | Self-generated fixture suites driven by `tests/runner.cpp` |

## Limits

- Single NAL payload: 16 MiB (larger units are reported and skipped).
- Picture dimensions: 16384x16384 (coded and cropped).
- Input file: 256 MiB.
- Exp-Golomb codewords: at most 32 leading zeros / 65 bits.
- Supported slice layouts: types 1, 2 and 5. Types 3, 4, 19, 20, 21 and SPS
  extensions are reported as unsupported rather than guessed at.
- Only Annex B byte streams are accepted; length-prefixed (AVCC) input is out
  of scope.
