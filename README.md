# transition_render

Headless, bit-reproducible cutscene transition renderer and pixel regression
tool. Pure C++17, standard library only; frames are binary PPM (P6), no
graphics device or third-party image library is used.

## Build

```sh
brew install cmake
cmake -S . -B build
cmake --build build
```

## Run

```sh
# render the built-in samples (four transition types, 30 frames each)
./build/transition_render demo --width 160 --height 90 --fps 30 \
    --out /tmp/transition-demo

# re-render and compare every frame byte-for-byte against the stored PPMs
./build/transition_render verify --out /tmp/transition-demo

# or the full end-to-end script
./scripts/demo.sh
```

`--fps` and `--sample` accept rationals, e.g. `--fps 24000/1001`;
`--sample speed` enables variable-speed segments that cross the transition
boundary (progress is always taken from global time).

## Output

For each transition type: `type`, `frames`, `first_hash`, `last_hash`,
`mid_diff_pixels`, `mid_mean_abs_diff`, plus a `reason` field that explains
zeroed statistics (`ok`, `identical_mid_frames`, `insufficient_samples`).
Hashes are fixed-length 64-bit FNV-1a hex; mean difference is 4-decimal
fixed point computed with integer arithmetic.

## Guarantees and rules

- Half-open `[first, last)` frame intervals: clips and segments tile without
  repeated or skipped frames.
- Transition weights/masks come from a single shared kernel module used by
  both the renderer and the self-test; all math is integer-only.
- Timeline validation (sizes, zero duration, negative time, non-positive
  speed, overlaps, unrepresentable time) finishes before any file is written.
- PPM writes go to a temp file followed by atomic rename; a failed frame is
  cleaned up while earlier frames remain.
- Output directories may contain only `*.ppm` and `render_manifest.txt`.
- Frames are capped at 4096x4096 with overflow-checked allocation; the
  renderer holds at most two live frames (<= 96 MiB).

## Exit codes

| Code | Meaning |
| ---- | ------- |
| 0    | success |
| 2    | bad parameters or invalid timeline |
| 3    | rendering or file write failure |
| 4    | pixel verification failure |

## Layout

| File | Role |
| ---- | ---- |
| `src/timeline.*` | rational time, clips, speed segments, validation |
| `src/transition.*` | shared linear/wipe/circle/checker kernels |
| `src/frame_buffer.*` | RGB buffer, P6 PPM IO, atomic writes |
| `src/renderer.*` | frame-to-clip mapping, materials, compositing |
| `src/hash.*` | stable hashes and difference statistics |
| `src/sample.*` / `src/manifest.*` | built-in scenes and verify metadata |
| `src/main.cpp` | CLI (`demo` / `verify`) |
| `tests/self_test.cpp` | boundary and pixel regression tests |
