# pw_sync — audio/video sync diagnostics kernel

A terminal-only, fully local engine that decodes an MP4/MOV file and simulates
two output paths sharing one master clock. It reports clock drift, audio/video
offset distributions and drop/repeat frame counts for offline performance
regression.

## Requirements

- macOS (arm64 or x86_64), C++17, CMake 3.27+
- FFmpeg 7.x or newer (`libavcodec`, `libavformat`, `libavutil`,
  `libswresample`). The code only uses the modern API surface, so it also
  builds against FFmpeg 8/9 (e.g. Homebrew's current `ffmpeg`).

```sh
brew install cmake ffmpeg
```

## Build

```sh
cmake -S . -B build -DCMAKE_BUILD_TYPE=Release
cmake --build build --parallel
```

## Usage

Generate the deterministic, in-process test fixture (no external media):

```sh
./build/pw_sync --generate-fixture ./build/sync_fixture.mp4
```

Run the diagnostics:

```sh
./build/pw_sync --input ./build/sync_fixture.mp4 \
    --clock audio --late-ms 35 \
    --path-a-delay-ms 4 --path-b-delay-ms 17 --json
```

### Options

| Option | Meaning | Default |
| --- | --- | --- |
| `--input FILE` | local MP4/MOV to analyze | required (or use `--generate-fixture`) |
| `--generate-fixture FILE` | write the deterministic fixture and exit | — |
| `--json` | stable JSON output instead of text summary | off |
| `--output FILE` | report destination (`-` or omitted = stdout) | stdout |
| `--clock audio\|monotonic` | audio-master or monotonic simulated clock | audio |
| `--late-ms MS` | late-frame threshold | 35 |
| `--path-a-delay-ms MS` | path A processing delay | 4 |
| `--path-b-delay-ms MS` | path B processing delay | 17 |
| `--discontinuity-tolerance-ms MS` | rollback/jump tolerance | 250 |
| `--late-policy drop\|repeat` | action for late video frames | drop |
| `--max-duration SECONDS` / `--max-frames N` | safety limits | 3600 / 200000 |

### Exit codes

| Code | Meaning |
| --- | --- |
| 0 | success |
| 2 | bad arguments or input path error |
| 3 | demux/decode/structural media failure (missing stream, corrupt data, limit) |
| 4 | report generation/write failure |

## JSON contract

`master_drift_ms` holds cumulative (signed), mean and maximum absolute drift
and the reset count. Each `path_X_av_offset_ms` block contains P50/P95/max
plus a histogram with **fixed edges**
`[-80,-40,-20,-10,0,10,20,40,80]` ms and 10 half-open bins. Drop/repeat
counters, `timestamp_repairs` and `discontinuities` are non-negative integers.

## Tests

```sh
ctest --test-dir build --output-on-failure
```

- `core_unit_tests`: timestamp normalization, clock drift/reset, synchronizer
  decisions and histogram edges (pure C++, no FFmpeg).
- `acceptance`: builds, generates the fixture, runs the CLI twice and diffs
  frame counts/statistics for determinism, validates required JSON fields,
  and checks every exit code.

## Architecture

```
media file
  -> demux_decode (FFmpeg; per-stream TimestampNormalizer, decoder flush)
  -> path_simulator (shared MasterClock + two Synchronizer instances)
  -> report (fixed-edge JSON / text summary)
```

Timestamps are normalized to double-precision seconds while the original time
base and raw ticks are retained on each packet. The fixture generator
(`fixture_generator`) embeds nonzero start times, unequal audio/video
duration, VFR, forward timestamp jumps, decoder delay and deterministic audio
PTS jitter.
