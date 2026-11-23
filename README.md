# mediad — local Matroska repair and RTP aggregation kernel

A self-contained Go library and CLI for repairing Matroska/WebM recordings
that were captured by several devices with independent clocks and then moved
across a lossy network. It unwraps 32-bit millisecond timestamps, corrects
per-device clock drift, rebuilds a random-access index from video keyframes,
fans the repaired stream out to three bitrate representations, recovers each
representation's RTP stream independently and merges them back onto one
shared clock while cross-checking every representation switch.

Nothing here decodes or re-encodes media payloads, and nothing touches the
network: every fixture is generated in memory from a seed.

## Requirements

- Go 1.23 (`brew install go@1.23`)
- No services, no external data, no network access

```sh
go mod tidy
go build ./...
go test ./...
```

## Commands

```sh
# Normal path: three representations, a slow editor, 1-in-17 packet loss.
go run ./cmd/mediad demo --seed 42 --slow-consumer-ms 120 --loss-every 17

# Damaged input: a cluster truncated mid-block.
go run ./cmd/mediad demo --seed 42 --corrupt cluster-truncate
# or: go run ./cmd/mediad corrupt --seed 42 --fault cluster-truncate

# Reproducibility: run everything twice and compare hashes.
go run ./cmd/mediad verify --repeat 2
```

Exit codes: `0` success, `2` damaged input with a recovery summary, `1`
execution failure.

Note: `go run` collapses any non-zero child exit to `1` (a Go toolchain
behaviour, not the program's). Build the binary to see the real code:

```sh
go build -o mediad ./cmd/mediad
./mediad demo --seed 42 --corrupt cluster-truncate > summary.json
echo $?   # 2
```

Available faults: `cluster-truncate`, `timestamp-wrap`, `unknown-size-nest`,
`rtp-loss`, `slow-consumer-cancel`.

## Output

The normal path prints a JSON summary. The acceptance fields are:

| Field | Meaning | Target |
| --- | --- | --- |
| `sync_error_ms` | worst audio/video deviation after repair | ≤ 20 |
| `packet_recovery_rate` | delivered / (delivered + lost) across representations | ≥ 0.90 |
| `p95_latency_ms` | 95th percentile publish-to-consume latency | ≤ 250 |
| `index_entries` | keyframe index entries | ≥ 10 |
| `boundary_mismatch` | rejected representation switches | 0 |

Additional fields expose the cross-stage invariants (`timestamps_monotonic`,
`index_keyframes_only`, `layers_consistent`), the recovery cursor
(`recoverable_prefix_bytes`, `recovery_cursor_entries`), the MP4 cross-check
(`mp4_crosscheck_entries`) and the final `summary_hash`.

The damaged path adds `error_code` (for example `truncated_cluster`) and a
non-zero `recoverable_prefix_bytes`. `verify` prints `summary_hash_equal` and
`index_digest_equal`; both must be `true`.

## Architecture

```
internal/merr      unified error model with byte offsets and exit-code mapping
internal/media     payload-neutral block, track and index types
internal/clock     shared nanosecond clock: wrap unwrapping and drift
internal/ebml      bounded streaming EBML walker + Matroska demuxer
internal/repair    timestamp repair decisions, index rebuild, recovery cursor
internal/mp4x      independent index cross-check via go-mp4
internal/rtp       RTP reorder recovery with a 64-packet window (pion/rtp)
internal/fanout    three-representation ladder, 4 MiB per-layer budgets
internal/fanin     shared-clock merge with boundary cross-checks
internal/report    terminal summary and stable summary hash
internal/testkit   deterministic in-memory fixtures and fault injection
internal/pipeline  the single composition the CLI runs
cmd/mediad         terminal entry point
```

### Design notes

**Bounded parsing.** The EBML walker rejects unknown-size elements except
`Segment`, `Cluster` and `BlockGroup`, caps any buffered payload at 8 MiB and
reports every failure with the byte offset where it was detected. It never
guesses structure: a child that overruns its parent, a truncated payload and
an invalid VINT are all hard errors. Blocks already confirmed are delivered
before the error, which is what makes a recovery prefix possible.

**Clock repair.** Timestamps are unwrapped across the 32-bit counter wrap,
a per-track rate is fitted against the container's nominal cadence
(least squares, clamped to ±5000 ppm), and every track is mapped onto the
reference track's clock. Small backwards steps are treated as jitter and
clamped so output stays strictly increasing per track. Every change is
recorded as a `Decision` (`unwrap`, `drift`, `clamp`, `keep`) and enters the
decision digest, so a repaired run can be audited.

**Deterministic index.** The index is a pure function of the repaired block
list: sort by track then time, hash `(track, time, offset)` triples. Repeated
runs produce identical digests. Each entry is validated to point at a video
keyframe, both in-memory and against the re-parsed MP4 sample table.

**Independent MP4 cross-check.** `internal/mp4x` writes the index as a real
ISO-BMFF sample table (`stts`/`stss`/`stsz`/`stco`) with go-mp4, re-reads it
with go-mp4's box parser and compares entry counts and keyframe sample
numbers. A bug corrupting both the Matroska walker and the in-memory index
would still be caught.

**Backpressure, not dropping.** Each representation owns a 4 MiB byte budget
and its own 64-packet reorder window. A slow consumer stalls the publisher
through the byte budget; nothing is silently dropped to catch up. Canceling
one layer discards only that layer's state and leaves the other two
unaffected.

**Boundary cross-check.** A planned representation switch is validated by
comparing the nearest blocks on both sides. A deviation over 20 ms rejects the
switch, keeps the outgoing representation and reports the track, both
representations and the time point.

## Constraints honoured

- No payload decode or re-encode.
- At most 4 MiB buffered per track/representation.
- RTP reorder window at most 64 packets.
- Unknown-size elements limited to `Segment`, `Cluster`, `BlockGroup`.
- Inputs and fixtures generated in memory; no network or external files.
