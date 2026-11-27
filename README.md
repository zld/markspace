# mediapipeline

A reusable, fully local media-processing library for regional live-distribution
cross-node transport. It maps and reconciles multiple versions of H.264 / H.265
/ AV1 elementary streams and their key-frame indices, tolerating duplicate,
reordered and burst-lost RTP packets over a bandwidth-varying link, and reports
every missing item, conflict and rollback deterministically.

All sample media is generated in-process from a seed — there are no external
media files and no network access.

## Requirements

- Go 1.23+ (built and verified with the local Go toolchain on macOS)
- Dependencies are pinned in `go.mod`:
  - `github.com/abema/go-mp4 v1.4.1` — index container parsing/writing
  - `github.com/pion/rtp v1.8.10` — RTP header parsing

## Build and test

```sh
go mod tidy
go build ./...
go test ./...
```

## Architecture and data flow

```
fixture ─▶ ingest (stream scan + index parse)
        ─▶ transport (RTP dedup / reorder / wrap / gaps)
                 │  timeline with gaps
                 ▼
        budget (window, concurrency, byte budget)
                 │  admitted tasks only
                 ▼
        worker (bounded pool + checkpoint, idempotent, resumable)
                 ▼
        merge (multi-version reconcile → staged manifest)
                 ▼
        check (independent re-scan / re-hash validator)
                 ▼
        commit on pass, rollback (with reason) on failure
```

`internal/clock` (rational time base) and `internal/fault` (structured error
model with byte offset + time range + stable code) are shared by every stage.

| Package | Responsibility |
| --- | --- |
| `internal/clock` | Exact rational timestamps; non-unique conversion is a conflict, never a silent shift. |
| `internal/fault` | Shared fault codes, offsets, time ranges and an accumulating report. |
| `internal/ingest` | Length-prefixed NAL (H.264/H.265) and OBU/leb128 (AV1) scanning; key-frame index box parse; resumes after each fault. |
| `internal/transport` | Per-SSRC extended-sequence dedup, bounded reorder, wrap-around expansion, continuous gap records. |
| `internal/budget` | Bandwidth/gap/work-aware window, concurrency and byte budget; rejects over-limit plans with a reason. |
| `internal/worker` | Bounded concurrency pool; only budget-admitted tasks; checkpointed, idempotent, resumable units. |
| `internal/merge` | Multi-version join on asset key + time range; missing/index/byte conflicts; stage → commit/rollback. |
| `internal/check` | Independent validator: re-scans raw bytes and re-hashes; never trusts worker success markers. |
| `internal/checkpoint` | Crash-safe atomic checkpoint with whole-file CRC, idempotent commits and rollback. |
| `internal/fixture` | Deterministic streams, indices, RTP feeds, versions and all fault injections. |
| `internal/demo` | End-to-end orchestration and stable terminal output (testable without a process). |
| `cmd/mediapipeline` | The `demo` terminal entry point. |

## Acceptance commands

### 1. Clean sample — exit code 0

```sh
go run ./cmd/mediapipeline demo --codec av1 --seed 17 --bandwidth 8000000
```

Expected line:

```
assets=3 missing=0 conflicts=0 validator=pass sha256=<64 hex chars>
```

### 2. Damaged and boundary sample

```sh
go run ./cmd/mediapipeline demo --codec h265 --seed 17 \
  --inject truncate,index-oob,duplicate,reorder,burst-loss,bandwidth-drop
```

This self-generates a truncated stream, an out-of-bounds index, duplicate and
reordered packets, a burst loss and a bandwidth drop, then prints the damage
offsets, gap count, budget rejection count and validation failure reasons.
Each located byte offset is within one byte of the injection position.

### 3. Interrupt and resume — digest must equal a full run

```sh
go run ./cmd/mediapipeline demo --codec h264 --seed 17 --interrupt-after 2 --resume
```

Expected line:

```
resumed_units=... elapsed_ms=... sha256=<same 64 hex chars as a full run>
```

`elapsed_ms` is non-negative; the resumed SHA-256 matches the same-parameter
full run byte-for-byte (asserted in `internal/demo/demo_test.go`).

## Exit-code contract and a `go run` caveat

The program defines:

- **0** — validation passed;
- **2** — input was damaged but every problem was fully reported;
- **1** — budget rejection on otherwise-clean input, or output validation failure.

The Go toolchain's `go run` does **not** propagate a compiled binary's exit code.
This is explicit in the Go source (`cmd/go/internal/run/run.go`:
*"The exit status of Run is not the exit status of the compiled binary"* — the
run action ignores the child status and the go command collapses any non-zero
exit to `1`, printing `exit status 2`). It behaves this way on every local
toolchain, including a pinned `go1.23.0`.

To observe the real **0 / 1 / 2** contract, build and run the binary directly:

```sh
./build.sh                       # produces bin/mediapipeline
./bin/mediapipeline demo --codec h265 --seed 17 \
  --inject truncate,index-oob,duplicate,reorder,burst-loss,bandwidth-drop
echo $?                          # 2 for damaged input
```

The program's own exit code is also covered deterministically by
`Result.ExitCode()` and its tests, independent of how the process is launched.

## Safety limits

Single-task bytes, concurrency, stream size, index range and window size are all
capped (`budget.DefaultLimits`, `ingest.DefaultLimits`); over-limit input is
rejected with a `LIMIT_EXCEEDED` / `BUDGET_EXCEEDED` fault carrying the position.
