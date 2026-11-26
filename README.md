# continuity

Rebuild auditable continuity state from local HLS/DASH manifests, fMP4
segments and RTP event records after a primary/backup source switch.

Pure local, terminal-only. No transcoding, no network access.

## Layout

- `cmd/continuity` — CLI entry (`demo`, `run`)
- `internal/manifest` — HLS (m3u8) and static DASH (MPD/SegmentList) parsers;
  keep manifest line numbers, reject path escapes and remote URIs
- `internal/adapter` — versioned `ParserV1` registry; go-mp4 segment parser
  (moof offset, tfdt, trun sample durations, mdat fingerprint) and pion/rtp
  event parser
- `internal/timebase` — integer 90 kHz timebase: anchor-based offset/drift
  fit (exact rational least squares) and PTS/RTP wraparound unwrapping
- `internal/core` — shared error model (stage, code, source, line, byte
  offset) and the continuity kernel
- `internal/metrics` — per-record mapping rows, summary, SHA-256 digest
- `internal/fixture` — deterministic sample generator (seed-parameterized)

## Pipeline

manifest -> parser adapters -> timebase (unwrap + clock fit) -> kernel
(dedup, gap/late accounting) -> mapping + summary.

- PTS (33-bit) and RTP (32-bit) are unwrapped by modulus; a jump of exactly
  half the modulus is reported as `ambiguous_jump`.
- Clock offset/drift are estimated from RTP sync anchors; VFR intervals are
  kept as actual sample durations, never re-quantized to a nominal rate.
- Duplicates are detected by source identity + track + normalized time +
  content fingerprint; the earliest arrival is kept and the rationale is
  recorded on the row.
- Sequence gaps count as lost; the unrecovered interval is marked and
  processing resumes from the next valid anchor. Corrupt segments abort
  with a parse error (exit 2) — the timeline is never silently shortened.
- Same inputs and seed always produce the same 64-hex digest.

## Commands

```
continuity demo --case normal|damaged|recover --seed N
continuity run --manifest <m3u8|mpd> --events <file> --arrivals <file>
```

Exit codes: 0 ok; 1 usage error; 2 damaged media (parse stage);
3 manifest/input error.

Note: `go run` masks child exit codes (it prints `exit status 2` but itself
exits 1). Build the binary (`go build ./cmd/continuity`) to observe exit
code 2 directly.

## Acceptance

```
brew install go@1.23   # any Go >= 1.23 toolchain works
go mod tidy && go build ./... && go test ./...
go run ./cmd/continuity demo --case normal --seed 17
go run ./cmd/continuity demo --case damaged --seed 17
go run ./cmd/continuity demo --case recover --seed 17   # run twice
./scripts/acceptance.sh                                  # asserts all of the above
```
