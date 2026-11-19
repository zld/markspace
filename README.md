# avsync — headless A/V sync kernel

A fully local, reproducible offline cutscene playback kernel. It generates a
synthetic media sample entirely in code, decodes it through FFmpeg, runs the
audio and video paths on separate threads behind a single master clock, and
reports synchronization behavior as JSON in the terminal. No GUI, no browser,
no camera/microphone, no network, no external media files.

## Requirements (macOS)

```sh
brew install cmake ffmpeg
```

Targets C++17 and CMake 3.25+, links FFmpeg's libavformat, libavcodec,
libavutil and libswresample. Builds on Apple Silicon and Intel.

## Build and run

```sh
cmake -S . -B build -DCMAKE_BUILD_TYPE=Release
cmake --build build --parallel

./build/avsync --duration 12 --fps 30000/1001 --sample-rate 48000 \
  --audio-underrun-ms 180 --video-delay-ms 90 \
  --duplicate-every 47 --missing-pts-every 113 --seed 42
```

`--fps` accepts `NUM/DEN` (e.g. `30000/1001`) or an integer. The only file
the program writes is a temporary matroska sample in the system temp
directory; it is removed before exit. Identical seeds produce byte-identical
samples (the muxer runs in bit-exact mode).

## Acceptance

```sh
./scripts/verify.sh           # configure, build, ctest, run + validate JSON
ctest --test-dir build --output-on-failure
```

## Exit codes

| Code | Meaning                                            |
|------|----------------------------------------------------|
| 0    | Processing and statistics completed                |
| 2    | Invalid command-line arguments                     |
| 3    | Decode/sync failure (resources cleaned up first)   |
| 4    | Test failure (`sync_scenarios`)                    |

## Output

Terminal JSON includes, at minimum:

- `master_drift_ms` and `av_offset_ms`, each with `min/max/mean/p50/p95/p99`
  in milliseconds. An empty sample set renders explicit `null`s, never zeros.
- `dropped_frames`, `repeated_frames`, `audio_underruns`, `missing_pts` as
  non-negative integers, alongside the injected anomaly counts.
- Decoded totals: a 12 s sample yields roughly 360 video frames and
  576000 audio samples.

## Architecture

```
 synthetic generator ──► matroska (mpeg4 B-frame video + pcm_s16le audio)
                                  │
                          FFmpeg demux + decode
                  ┌───────────────┴───────────────┐
            bounded audio q                 bounded video q
                  │                               │
          AudioOutput thread             VideoOutput thread
          (consumed samples ──► MasterClock ◄── wait/show/drop/repeat
           drive clock; underrun             via SyncController)
           freezes it)                  │
                  └──────────► Metrics ◄──┘
                       (drift + offset percentiles, counters)
```

- **MasterClock** (`master_clock.*`): monotonic integer-microsecond media
  clock with pause/resume/reset. `now_us()` never moves backward even across
  repeated or out-of-order state changes. A wall-paced ideal timeline makes
  drift measurable; an underrun freeze shows up as negative drift.
- **SyncController** (`sync_controller.*`): pure integer-microsecond
  wait/show/drop decisions. Missing, non-monotonic or absurd pts are estimated
  from the last shown frame; suggested waits are capped so a far-future
  timestamp cannot stall the pipeline.
- **AudioOutput** (`audio_output.*`): a headless simulated device that blocks
  for each chunk's sample duration, then advances the clock by consumed
  samples. Underruns pause the clock and count events; recovery re-anchors at
  the real media position without jumping backward.
- **VideoOutput** (`video_output.*`): schedules every frame against the
  shared clock. Early frames wait, late frames are dropped past a threshold
  and the previous frame is repeated to preserve output cadence.
- **BoundedQueue** (`bounded_queue.hpp`): blocking MPMC queue; `close()` wakes
  every waiter and producers can poll a stop flag via `push_for`, so shutdown
  or failure always joins every thread.

All timestamp math stays in integer microseconds; the only floating point is
in generated waveform/pixel content and in the final percentile rendering.

## Fault injection

- `--audio-underrun-ms N`: one N-millisecond audio stall mid-stream.
- `--video-delay-ms N`: decode-latency spike on every 7th video frame.
- `--duplicate-every N`: push every Nth decoded frame twice.
- `--missing-pts-every N`: blank the pts of every Nth frame.
