# mediacore

Deterministic CMAF/fMP4 processing kernel for low-latency interactive media.
It parses fragmented MP4 sample data with exact byte offsets, maps several
tracks onto one integer timeline, repairs sequence defects under a fixed
policy, exports a reproducible offscreen frame sequence, persists resumable
checkpoints and independently verifies the result.

Everything runs locally. There is no network access, no capture device and no
graphics stack; sample data is generated from a seed.

## Build and test

```sh
brew install go@1.23
go mod tidy
go build ./...
go test ./...
```

Dependencies are pinned in `go.mod`: `github.com/abema/go-mp4` for ISO BMFF
box parsing and `github.com/pion/rtp` for RTP header and sequence handling.
The requested `go-mp4 v1.30.0` does not exist upstream; `v1.7.4` is the newest
release and is what is pinned.

Synthetic sample payloads start with a real RTP v2 header, so RTP sequence
continuity is checked with `pion/rtp` alongside container-level continuity.
The `rtp-sequence-gap` fault exercises that path: it reports a loss at the
sample byte that carries the packet, using the same error model as box faults.

## Demo

```sh
go run ./cmd/mediacore demo \
  --seed 41 --tracks 2 --fragments 24 \
  --inject truncation,tfdt-regression,index-gap \
  --checkpoint ./tmp/checkpoint.json --resume

go run ./cmd/mediacore verify \
  --manifest ./tmp/manifest.json --frames ./tmp/frames.jsonl
```

`demo` prints `fragments`, `tracks`, `faults`, `first_error_offset`,
`offset_error_bytes`, `faults_reached`, `recovered`, `resume_ms`, `frames`
and `sha256`. Repeating the same invocation reproduces the same `sha256`, and
the second run resumes from the checkpoint.

`faults` is how many faults were injected; `faults_reached` is how many were
matched byte-exactly by a reported error. A structural fault (an impossible
box size or sample range) stops parsing at its own byte by design, so faults
placed after it are unreachable. `offset_error_bytes` is the distance between
the first reported error and the nearest injected byte, and is `0` whenever
the first error is one of the damaged positions.

`scripts/acceptance.sh` runs both commands and asserts the acceptance
properties.

### Exit codes

| code | meaning |
| ---- | ------- |
| 0 | complete, no input damage |
| 2 | input damaged but processed under the documented policy, with faults recorded |
| 1 | unrecoverable, or verification failed |

## Layout

| path | role |
| ---- | ---- |
| `internal/clock` | exact integer rational timebase conversion |
| `internal/fault` | error codes with file, fragment, box and raw offset |
| `internal/fixture` | seeded sample streams and fault injection |
| `internal/ingest` | boundary-safe box, init, fragment and index parsing, plus RTP sequence continuity |
| `internal/timeline` | per-track mapping and multi-track merge |
| `internal/transform` | duplicate/gap/regression repair and frame export |
| `internal/checkpoint` | atomic checkpoint store and resume validation |
| `internal/verify` | cross-stage invariants and provenance manifest |
| `internal/pipeline` | stage wiring |
| `cmd/mediacore` | terminal entry point |

## Policy

* **Ordering.** Input order is authoritative. Fragments are never reordered.
* **Duplicates.** A fragment that exactly repeats its predecessor is dropped;
  the first appearance is kept and the dropped bytes remain recorded.
* **mfhd gaps.** Recorded at the tfdt that reveals the skipped decode time.
  Decode time is not invented, because tfdt remains authoritative.
* **tfdt regression.** The fragment continues at the previous fragment's end;
  every sample keeps its original source time.
* **Unresolvable contradictions.** A repeat with the same or earlier sequence
  but a different declaration marks the track unmergeable with the offending
  offset retained.
* **Corrupt data.** A malformed `mdat` is never scanned for box magic. The scan
  stops at the declaring box and returns a resume offset.

## Limits

`ingest.Limits` bounds file size, box count, scan box count and per-fragment
duration. The CLI additionally bounds track count, fragment count and review
frame dimensions. Performance targets apply to local synthetic samples only.
