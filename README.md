# tsinspect

A small, dependency-light MPEG-TS inspection and program-slicing tool for
media pipelines. It reads 188-byte transport streams, rebuilds PAT and PMT
program tables itself, reports continuity anomalies, and can write a
single-program slice.

- Pure terminal; no GUI, browser, camera or microphone access.
- PSI section reassembly, CRC validation, PAT and PMT parsing are implemented
  in this repository. `go-astits` is used only for TS packet framing and
  header demultiplexing.
- Runs on macOS arm64 and x86_64 with Go 1.22+.

## Quick start

```sh
go mod download
go build -o /tmp/tsinspect ./cmd/tsinspect
go test ./...
go run ./testdata/generate.go -out /tmp/sample.ts
/tmp/tsinspect -json -program 101 -out /tmp/program-101.ts /tmp/sample.ts
```

Run the full acceptance suite (build, tests, fixtures, assertions):

```sh
scripts/acceptance.sh
```

## Usage

```
tsinspect [flags] [input.ts|-]
```

| Flag | Meaning |
| --- | --- |
| `-json` | Write a stable JSON summary to stdout. |
| `-program N` | Program number to slice. Omit or use `0` to disable slicing. |
| `-out PATH` | Slice destination. Written via a temporary file and atomically renamed; a failed run leaves any previous file untouched. |
| `-max-buffer-packets N` | Packets cached while waiting for a usable program table (default 4096). |

Input comes from the named file, `-`, or stdin. Only 188-byte packets are
accepted; 192/204-byte encapsulation is intentionally not auto-detected.

### Exit codes

| Code | Meaning |
| --- | --- |
| `0` | Completed without input problems. |
| `2` | Input error (unreadable input, non-188 layout, partial trailing packet), PSI CRC/length/table-integrity error, or bad arguments. |
| `3` | Slice output could not be written. |

A PSI integrity error still produces a complete report and a valid slice; the
exit code only signals that the input contained damaged tables.

## What the report contains

`pat_version_changes` and `pmt_version_changes` count only versions that
actually took effect — retransmissions of the current version are not
counted. `pid_mappings` and `elementary_streams` list each effective
elementary PID with `program_number`, `pid`, `stream_type`, `packet_count`
and `discontinuity_seen`. `table_events` explains every lifecycle transition:
applied versions (including 5-bit wrap), ignored `current_next=0` tables,
stale versions, same-version content conflicts, orphan PMTs, PMT PID changes
and programs removed by a newer PAT.

`anomalies` distinguishes:

- `duplicates` — repeated continuity counter with payload.
- `counter_jumps` — lost or forwarded packets.
- `transport_errors` — packets with `transport_error_indicator` set.
- `af_only_packets` — adaptation-field-only packets, which must not advance
  the counter.
- `unjudgeable` — the first packet after damage or an announced
  discontinuity, where a gap cannot be measured.
- `crc_errors`, `section_errors`, `orphan_pmts`.

When `-program` is given, `slice` reports what was written, how many packets
were flushed from the pre-table cache, and the version boundary at each output
position where a new PMT version took effect. Damaged (transport-error) and
duplicate packets are never written to the slice.

## Layout

```
cmd/tsinspect/main.go      CLI, flags, exit codes, atomic output
internal/transport         188-byte packet framing over go-astits
internal/psi               section reassembly, CRC, PAT/PMT parsing
internal/program           version/pending/conflict/removal state
internal/demux             one-pass pipeline and program slicer
internal/metrics           per-PID continuity classification
internal/report            stable, sorted JSON summary
testdata/generate.go       deterministic local fixtures and variants
scripts/acceptance.sh      build + test + quantitative assertions
```

## Design notes

- A section is only reassembled from payloads whose continuity is intact. A
  duplicate, jump, transport error or discontinuity discards the partial
  section, so two unrelated halves can never be spliced into a "valid" table.
- Only complete, CRC-valid, internally consistent multi-section tables become
  effective. A conflicting or stale version leaves the effective table alone.
- Table sizes are bounded (`MaxSectionLength`, `MaxPrograms`, `MaxUnboundPMTs`,
  `MaxPSIPIDs`) and the slicer's pre-table cache is hard-capped, so hostile
  input cannot grow memory without limit.
- All report lists are sorted and the JSON field order is fixed, so identical
  input and flags produce byte-identical output.
