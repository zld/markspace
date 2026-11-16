# Transition Lab

Offline cutscene timeline verification tool for an indie game studio.
Pure C++17, no third-party dependencies, no GUI — frames are generated in
memory, composited through fixed-point transition kernels, written as
binary P6 PPM files, and verified by replaying the whole pipeline and
comparing 64-bit frame hashes.

## Build

```sh
c++ -std=c++17 -O2 -Wall -Wextra -pedantic src/*.cpp -o /tmp/transition_lab
```

Requires Apple Clang (Xcode Command Line Tools). If missing:
`xcode-select --install`.

## Demo

```sh
/tmp/transition_lab demo --out /tmp/transition_demo --fps 24 --frames 48 \
    --speed 0=1/2,24=2/1,40=1/1 --seed 17 --verify
```

This renders three transitions (dissolve, directional soft-edge wipe,
circle reveal), 48 frames each — 144 P6 PPM files total — and prints one
summary line per transition:

```
type=dissolve frames=48 first_hash=... last_hash=... \
    mid_changed_pixels=6144 mid_mae=43.67 mismatch_frames=0
```

| Field | Meaning |
|---|---|
| `type` | transition kernel used |
| `frames` | frames rendered for this transition |
| `first_hash` / `last_hash` | FNV-1a 64-bit hash, 16 hex digits |
| `mid_changed_pixels` | pixels differing from the front clip at the middle frame (0..6144) |
| `mid_mae` | mean absolute per-channel difference, 0..255 |
| `mismatch_frames` | hash mismatches found during the replay pass |

## Options

| Flag | Default | Notes |
|---|---|---|
| `--out DIR` | required | existing output directory, no `..` components |
| `--fps N` | 24 | 1..240 |
| `--frames N` | 48 | total output frames, capped at 4096 |
| `--width` / `--height` | 96 / 64 | capped at 4096 and 8M pixels |
| `--seed N` | 17 | deterministic source generator seed |
| `--speed f=a/b,...` | 1/1 | rate nodes; rates limited to 1/8..8 |
| `--wipe ltr\|rtl\|ttb\|btt` | ltr | wipe direction |
| `--verify` | off | exit 3 on any replay hash mismatch |

Speed nodes are keyed by output frame with rational rates. Source
positions are obtained by integrating the piecewise-constant rate, so
variable-speed sections map deterministically. Node frames must start at
0, stay strictly increasing, and lie inside the output range; cumulative
positions are checked for integer overflow.

## Exit codes

| Code | Meaning |
|---|---|
| 0 | success |
| 2 | parameter error (bad flags, rates, dimensions, speed nodes) |
| 3 | consistency failure (replay hash mismatch with `--verify`) |
| 4 | file error (invalid path, open/write/flush failure) |

PPM writes go to a temporary sibling file and are renamed only after a
successful flush; failed writes remove the temporary file.

## Tests

```sh
sh tests/smoke.sh /tmp/transition_lab
```

Covers the happy path, P6 headers, cross-process reproducibility,
endpoint exactness, and every exit code.
