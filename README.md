# tsaudit

A local, dependency-light command-line auditor for MPEG-TS (188-byte) transport
streams. It rebuilds the program tables from raw packets, tracks PAT/PMT
version changes and per-PID continuity counters, records evidence for damaged
sections, and can slice one program back out into a new TS file.

No PES, codec, or encrypted-content parsing is performed. Only common PAT and
PMT tables are interpreted.

## Build

```sh
go mod download
go build ./...
```

## Generate deterministic fixtures

All sample data is produced locally by the Go generator; no external media is
needed.

```sh
go run ./testdata --scenario normal       --out /tmp/sample.ts
go run ./testdata --scenario bad-sections --out /tmp/bad-sections.ts
go run ./testdata --scenario bad-cc       --out /tmp/bad-cc.ts
```

The `normal` fixture is exactly 120 packets with two complete PMT versions:
v0 maps video to PID 256 and audio to PID 257; v1 remaps video to PID 258
while keeping audio on 257. A secondary stream on PID 300 spans both epochs.

## Audit

```sh
# JSON summary (stable field names and array ordering)
go run ./cmd/tsaudit --input /tmp/sample.ts --format json

# Greppable key=value text summary
go run ./cmd/tsaudit --input /tmp/sample.ts

# Read from standard input
cat /tmp/sample.ts | go run ./cmd/tsaudit --input -
```

## Slice one program

```sh
go run ./cmd/tsaudit --input /tmp/sample.ts --program 1 --slice /tmp/program1.ts
go run ./cmd/tsaudit --input /tmp/program1.ts --format json
```

The slice keeps the program's PAT and PMT packets and only the elementary PIDs
mapped by a fully validated table. Original packet order is preserved, and a
PID reused across a PMT change is reported as separate mapping intervals
rather than silently merged.

## Exit codes

| Code | Meaning                                                            |
|------|-------------------------------------------------------------------|
| 0    | Clean audit (a complete PAT and every referenced PMT published).  |
| 2    | Input or structural damage: unreadable file, sync/truncation error, corrupt section, missing PAT, or referenced PMT never published. |
| 3    | `--strict-continuity` found one or more elementary-stream gaps.   |

## Architecture

```
TS file / stdin
   -> internal/tsreader   framing, sync/TEI/truncation, transport header
   -> internal/psi        section reassembly, CRC-32/MPEG-2, PAT/PMT versions
   -> internal/program    program -> PMT PID -> elementary PID map + intervals
   -> internal/continuity per-PID 4-bit counter, duplicates, gaps, errors
   -> internal/analyze    coordinates the pipeline and builds the summary
   -> internal/slicer     reuses the same PSI logic to extract one program
   -> internal/report     deterministic JSON and text output
```

`github.com/asticode/go-astits` supplies the transport-header type and packet
constants; section assembly, version management, CRC validation and program
reconstruction are implemented in this module.

## Tests and acceptance

```sh
go test ./...
./scripts/verify.sh
```

`verify.sh` regenerates the fixtures and asserts the concrete summary fields,
slice round-trip counts, damaged-table non-publication, the 5 -> 8 two-packet
continuity gap, and exit codes 0/2/3.
