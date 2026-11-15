# h264_inspect

A fully local, repeatable H.264 bitstream checker for validating the
encoding parameters, frame-type distribution and group-of-pictures
boundaries of delivered cutscene video. No network access, external
decoders or out-of-tree samples are used; every sample stream is
generated from code.

## Build

Requires CMake >= 3.20 and a C++17 compiler (Linux, macOS).

```sh
cmake -S . -B build
cmake --build build -j
ctest --test-dir build --output-on-failure
```

The test framework (Catch2 3.5.4) is vendored under
`tests/third_party/catch2`, so the build is offline.

## Demo

```sh
# Generate the synthetic sample (SPS/PPS, three-slice IDR, P/B/P frames,
# an interlaced field pair and a second IDR), then inspect it.
./build/h264_inspect --generate-sample --input ./build/sample.264
./build/h264_inspect --input ./build/sample.264 --format auto --json
```

Expected summary: `profile_idc=66`, `level_idc=30`, `width=640`,
`height=360`, `interlaced=true`, `frames={I:2,P:3,B:1,unknown:0}`,
`groups=2`, `errors=0`, exit code 0.

## Options

| Option | Meaning |
| --- | --- |
| `--input PATH` | bitstream file (required unless `--generate-sample`) |
| `--format auto\|annexb\|avcc` | framing detection, default `auto` |
| `--json` | stable-field-order JSON instead of text |
| `--strict` | stop at the first truncation / illegal Exp-Golomb |
| `--generate-sample` | emit the code-generated sample to `--input` |
| `--help` | usage |

## Exit codes

| Code | Meaning |
| --- | --- |
| 0 | completed without error |
| 1 | argument error |
| 2 | strict parse failure, or fatal format/data error |
| 3 | file or resource-limit error |

## Modules and data flow

```
input bytes
  -> NAL scan (Annex B start codes / 4-byte length prefix)
  -> RBSP bit reader (bounded, emulation-prevention removal)
  -> Exp-Golomb (ue/se, 31 leading-zero cap)
  -> H.264 syntax parser (SPS, PPS, slice headers, field markers)
  -> access-unit / GOP aggregation
  -> CLI text or JSON formatting and exit code
```

- `include/bit_reader.hpp`, `src/bit_reader.cpp` — bounded MSB-first bit
  reader, RBSP unescape, truncation detection.
- `include/exp_golomb.hpp`, `src/exp_golomb.cpp` — unsigned/signed
  Exp-Golomb with a leading-zero limit.
- `include/nal_scanner.hpp`, `src/nal_scanner.cpp` — format detection and
  NAL unit scanning with raw byte offsets.
- `include/h264_parser.hpp`, `src/h264_parser.cpp` — parameter sets,
  slice types, reference/field info, parameter-set history events.
- `include/group_index.hpp`, `src/group_index.cpp` — access-unit
  aggregation and GOP start offsets/frame counts.
- `include/fixture_writer.hpp`, `src/fixture_writer.cpp` — code-generated
  Annex B / AVCC samples and corrupted variants.
- `src/main.cpp` — argument parsing, orchestration, reporting.

## Limits

- single file: 512 MiB
- single NAL unit: 64 MiB
- Exp-Golomb leading zeros: 31
- all offsets are byte offsets into the input; errors carry a stage,
  reason and offset.
