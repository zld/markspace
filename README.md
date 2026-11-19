# h264scan

A local, dependency-free analyzer for raw H.264 (Annex B) byte streams.
It reads SPS image parameters, classifies I/P/B pictures, merges multi-slice
and interlaced field pictures, and reports the byte offset of every picture
group. No FFmpeg, OpenH264, or other H.264 parsing library is used: the bit
reader, Annex B scanner, and syntax parser are all implemented in this repo.

## Scope and limitations

The tool parses only the fields needed for SPS/PPS identification, slice
header picture identity, and access-unit boundaries. It does **not** perform
entropy decoding, pixel reconstruction, or claim support for every H.264
extension (for example scaling lists and `pic_order_cnt_type == 1` are
rejected with a diagnostic rather than guessed).

## Build

Requires CMake >= 3.25 and LLVM/Clang >= 16.

```sh
brew install cmake llvm
cmake -S . -B build \
    -DCMAKE_CXX_COMPILER="$(brew --prefix llvm)/bin/clang++" \
    -DCMAKE_CXX_STANDARD=17
cmake --build build --parallel
```

## Usage

```sh
# Generate the deterministic demo stream (no external files needed).
./build/h264scan make-fixture --output /tmp/h264scan-demo.264

# Human-readable report.
./build/h264scan analyze /tmp/h264scan-demo.264

# Machine-readable report; top-level keys: sps, images, groups, diagnostics.
./build/h264scan analyze /tmp/h264scan-demo.264 --json

# Treat any diagnostic as a build failure.
./build/h264scan analyze /tmp/h264scan-demo.264 --strict
```

### Exit codes

| Code | Meaning                                                        |
|------|----------------------------------------------------------------|
| 0    | Analysis completed without diagnostics                         |
| 2    | `--strict` mode and at least one diagnostic was produced       |
| 64   | Usage error (unknown command or missing argument)              |
| 66   | Input/output error (unreadable, empty, no NAL, > 512 MiB)      |

## Robustness

* All offsets are 64-bit byte offsets into the original input file.
* Reads are bounds-checked; truncated Exp-Golomb codes and reads past the
  buffer end stop parsing and emit a structured diagnostic.
* Input size is capped at 512 MiB; NAL unit and diagnostic counts are capped
  so malformed streams cannot trigger unbounded allocation.
* The scanner handles 3- and 4-byte start codes, leading zero runs,
  consecutive start codes (empty NALs), a final NAL with no trailing zero,
  and a truncated start code at end of file.

## Testing

```sh
./tests/check_cli.sh ./build/h264scan /tmp/h264scan-demo.264
```

The deterministic fixture contains 12 frame numbers across two GOPs, I/P/B
slice types, one multi-slice frame picture, and one interlaced field pair,
mixing three- and four-byte start codes.
