# abr-engine

A deterministic adaptive-bitrate (ABR) decision engine that plays a synthetic
network trace through a bandwidth estimator, a buffer model, and a
hysteresis-aware selector. It exists to make switching-policy problems
measurable: every segment reports the estimate it saw, the bitrate it chose,
the time the download took, the buffer left behind, and any stall.

No network access, media decoding, or player is involved. Traces are generated
locally from a seed.

## Requirements

- macOS with Python 3.11
- NumPy 2.1.x (installed into the local `.venv`)

## Setup

```bash
python3.11 -m venv .venv
.venv/bin/python -m pip install -e '.[test]'
```

## Run

```bash
.venv/bin/python -m abr_engine simulate \
  --segments 120 --seed 41 --segment-seconds 4 \
  --bitrates 300,750,1200,1850,2600 \
  --initial-buffer 12 --buffer-capacity 30 \
  --output json
```

`--output text` (the default) prints `key=value` summary lines followed by one
`segment=...` line per segment; `--output json` emits a `summary` object plus a
`segments` array. Diagnostics go to stderr.

Exit codes: `0` success, `2` invalid arguments or configuration, `1` a trace or
simulation invariant failed at runtime.

## Policy

Each segment is decided from the estimator's *previous* observations:

1. **Buffer danger** — below `--danger-buffer` seconds the policy downgrades,
   even when the estimate would still support the current tier.
2. **Bandwidth drop** — if the current bitrate exceeds the available estimate,
   the policy drops straight to the affordable tier. Downgrades are never
   delayed by up-switch hysteresis.
3. **Up-switch** — needs the safety margin
   (`next_bitrate * --up-safety-margin <= available_kbps`), at least
   `--up-min-buffer` seconds of buffer, `--min-dwell` segments at the current
   tier, and `--up-streak` consecutive qualifying segments. One tier at a time.
4. **Hold** otherwise.

Threshold comparisons are deterministic: "meets" is inclusive (`>=`) and
"dangerously low" is strict (`<`).

## Layout

| Path | Purpose |
| --- | --- |
| `src/abr_engine/trace.py` | Seeded bandwidth, RTT, and burst-loss trace generation |
| `src/abr_engine/estimator.py` | Forget-weighted throughput estimate with safety factor |
| `src/abr_engine/ladder.py` | Strictly increasing bitrate ladder and affordable-tier lookup |
| `src/abr_engine/buffer.py` | Buffer occupancy and accumulated stall |
| `src/abr_engine/policy.py` | Buffer-aware selection with switching hysteresis |
| `src/abr_engine/simulate.py` | Download/playback state machine |
| `src/abr_engine/cli.py` | `argparse` front end, text/JSON rendering, exit codes |
| `tests/test_engine.py` | Boundary and state-transition tests |
| `scripts/demo.sh` | End-to-end demo |

## Tests

```bash
.venv/bin/python -m unittest discover -s tests
```
