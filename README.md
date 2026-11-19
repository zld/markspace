# streamcheck

Generate and verify multi-bitrate HLS media playlists from locally transcoded
segments. Pure Go standard library, no network, no encoder, no decoder.

## Build

```sh
go build -o /tmp/streamcheck ./cmd/streamcheck
```

Or via Make: `make build`, `make test`, `make acceptance`.

## Segment naming

Every media file lives directly under `--root` and is named:

```
<slug>_<rate>_<number>_<duration_ms>.ts
```

Example: `seg_1600000_5_1000.ts`. The slug is `[A-Za-z0-9-]+`; rate must be
positive; number must be a non-negative integer; duration must be a positive
integer in milliseconds. A mismatch, illegal character, negative number or
path escape aborts the run with exit code 2 rather than guessing.

Each rate gets one playlist, `<rate>.m3u8`, written next to its segments.

## Commands

```sh
# Generate demo data (deterministic, no external assets).
streamcheck demo --root /tmp/stream-demo \
  --rates 800000,1600000,3200000 --segments 12 \
  --drop 1600000:5 --shuffle 3200000

# Build playlists from whatever is on disk.
streamcheck build --root /tmp/stream-demo [--rates 800000,1600000] [--json]

# Verify playlists against disk.
streamcheck verify --root /tmp/stream-demo [--json] \
  [--rates ...] [--missing-tolerance N] [--deviation-ms MS]
```

`--drop RATE:NUMBER` omits one segment file from that rate; `--shuffle RATE`
writes that rate's playlist entries out of order. Both are demo-only knobs
for producing failing fixtures.

## Exit codes

| code | meaning |
|------|---------|
| 0 | success / verification passed |
| 1 | verification found problems |
| 2 | invalid arguments or file operation failure |

A missing segment is tolerated when `--missing-tolerance` is raised, but any
other finding (out of order, duplicate, checksum, cross-rate drift, manifest
disagreement) still fails the run.

## What verification checks

- **Correspondence** — every manifest entry names a file on disk and every
  file on disk is declared. Both directions are reported.
- **Numbering** — duplicates, descending order and holes between the lowest
  and highest number, reported with rate and number.
- **Durations** — manifest EXTINF versus the duration encoded in the file
  name, at millisecond precision.
- **Cross-rate** — segment counts, cumulative duration and the largest
  boundary deviation against the lowest rate. The deviation is reported per
  rate and fails when it exceeds `--deviation-ms`.
- **Content** — SHA-256 streamed per file. Repeated content *within* a rate is
  reported; identical bytes across rates are allowed.
- **Manifest structure** — header, target duration, media sequence, ENDLIST,
  millisecond durations and plain file-name URIs.

The generator and the verifier share `internal/manifest` for duration
formatting, URI normalization and parsing, so the two cannot drift apart.

## Safety and scale

- Files are hashed in 64 KiB chunks; nothing larger than one chunk is held in
  memory per file.
- Scanning reads directory entries in batches and honours context
  cancellation (Ctrl-C).
- Playlists are staged as temporary siblings and renamed only after every
  file in the build is written and synced, so an interrupted or failed build
  never leaves a complete-looking playlist. A cancelled multi-rate build
  leaves previously existing playlists untouched.
- Symlinks resolving outside `--root` are rejected.
- The per-rate segment limit is 100,000.
