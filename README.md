# tsinspect

`tsinspect` is a local, terminal-only MPEG-TS inspection, program-table
rebuild and slicing tool written in Go 1.22. It reads transport streams with
[`go-astits`](https://github.com/asticode/go-astits) for packet framing only;
PAT/PMT parsing, CRC-32 validation, section reassembly, version state,
continuity accounting and slicing are implemented in this repository. The
slicer drives the same PSI parser as `inspect`, so a slice can only be taken
from tables the inspector would report as effective.

No network access, external service, media player or graphical toolkit is
involved. Sample streams are synthesized deterministically.

## Build and run

```sh
xcode-select --install            # only if the command line tools are missing
go mod download
go build -o /tmp/tsinspect ./cmd/tsinspect
go test ./...
```

or use the Makefile: `make build`, `make test`, `make demo`, `make inspect`,
`make slice`.

### Demo

```sh
go run ./cmd/tsinspect generate --out /tmp/demo.ts --scenario updates-loss
go run ./cmd/tsinspect inspect  --input /tmp/demo.ts --json
go run ./cmd/tsinspect slice    --input /tmp/demo.ts --program 1 --out /tmp/program-1.ts
```

The JSON summary contains `pat_version_changes`, `pmt_version_changes`,
`program_to_pmt_pid` (at least two programs), and a `streams` array where each
entry has `program_number`, `stream_type`, `pid` and a non-negative
`packet_count`. Lossy fixtures report `continuity_errors > 0`.

## Commands

| Command | Purpose |
| --- | --- |
| `generate --out PATH [--scenario NAME]` | Write a deterministic fixture. |
| `inspect --input PATH [--offset N] [--json]` | Rebuild tables, count packets and print a summary. |
| `slice --input PATH --program N --out PATH [--offset N]` | Extract one program. |

Scenarios: `baseline`, `updates-loss` (default), `corrupt`, `all`.

Exit codes: `0` success, `1` input or parse failure, `2` invalid arguments.
Diagnostics go to stderr.

## Behaviour

- **Framing** — packets are fixed 188 bytes; `--offset` skips leading bytes.
  A non-`0x47` sync byte is a parse failure. A final short fragment is counted
  as one `trailing_packets` and its byte count is reported.
- **PSI reassembly** — sections may span packets, honour `pointer_field`, and
  multiple sections may share one payload. Section length must be between 4
  and 4093 bytes (4096 including the header) and the CRC-32/MPEG-2 must
  validate; bad sections are counted and dropped, never applied.
- **Table state** — a mapping only becomes effective once every section of a
  version (`section_number` 0..`last_section_number`) has arrived, all with
  valid CRCs and `current_next_indicator = 1`. Version numbers wrap at 31.
  Repeats of the active version are counted as duplicates. A PMT only becomes
  effective on the PID the active PAT maps to that program.
- **Damage handling** — on a continuity jump or transport error the affected
  PID's in-progress section is discarded, and pending tables fed by that PID
  are dropped. The last effective tables stay in force; an incomplete new
  version is never used for slicing.
- **Continuity** — a packet with payload must advance mod 16 per PID.
  Adaptation-only packets do not advance it. Duplicates, signalled
  discontinuities and transport errors are classified separately, and a
  per-PID lost-packet estimate is reported.
- **Slicing** — fails if the program is absent from the rebuilt PAT or its PMT
  never completes, and refuses to write when input and output resolve to the
  same file. Output is written to a temporary file beside the destination and
  renamed only on success, so a failed run leaves no output. Selected packets
  keep their PID layout; continuity counters are renumbered per PID so the
  slice is self-consistent, while source gaps are counted and reported.

## Layout

```
cmd/tsinspect/main.go      argument parsing, commands, exit codes
internal/ts/reader.go      streaming packet reader over go-astits
internal/psi/section.go    section reassembly, length/CRC validation
internal/psi/pat.go        PAT parsing
internal/psi/pmt.go        PMT parsing
internal/psi/tables.go     version state and effective mappings
internal/demux/counter.go  per-PID packet and continuity accounting
internal/demux/session.go  reader → counters + PSI → tables pipeline
internal/demux/slicer.go   program slicing on confirmed tables
internal/report/json.go    stable, sorted summary model and JSON
internal/report/text.go    terminal summary
internal/fixture/generate.go deterministic TS fixture scenarios
```

Input is read as a stream; the whole file is never held in memory.
