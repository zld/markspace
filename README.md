# bbdiff

`bbdiff` compares the basic blocks of two builds of the same x86-64 ELF firmware
image and reports blocks that were added, removed, changed, or that cannot be
confidently classified. It is built for patch-risk review: a block that moved
because the linker shifted the whole image must not be reported as a change,
while an edited immediate or a flipped branch must be.

The tool is local-only. It reads two files, writes a report to the terminal, and
never opens a socket.

## What it does

1. Validates both ELF64 little-endian x86-64 images (byte order, machine type,
   section table, file truncation, table indexes). A structurally broken input
   produces a clear error and a distinct exit code.
2. Normalises PC-relative relocations into symbolic references
   (`[pc32 msg_err+0]`, `[plt verify+0]`) so relocated addresses do not read as
   code changes. Unknown relocation types and missing symbols keep their raw
   bytes, are reported, and lower the confidence of affected blocks.
3. Aligns functions between the two builds by symbol name and alias set,
   disambiguating duplicate names with section, size, and call-target evidence.
   Cases that cannot be resolved uniquely become `uncertain` instead of guessed.
4. Disassembles each aligned function with Capstone 5, splits it into basic
   blocks at branches, calls, returns, and function ends, and resynchronises
   after undecodable bytes.
5. Canonicalises each instruction — address fields are blanked and replaced by
   symbolic tokens, everything else (opcode, registers, non-address immediates)
   is kept — and hashes the canonical instruction stream with SHA-256.
6. Matches blocks by exact canonical hash, then by canonical hash ignoring
   unresolved addresses, then structurally using instruction shape, size,
   terminator kind, and control-flow adjacency. Every report entry carries the
   block address, instruction count, SHA-256, owning function, and change kind.

Functions are analysed concurrently with a bounded worker pool. Each worker
owns one Capstone engine and closes it when it stops, including on cancellation
and after a failed function; a failure never discards the results of the other
functions.

## Requirements

- Go 1.23 or newer (the repository was developed against Go 1.27).
- Capstone 5.x installed as a native library with its headers.
  - macOS: `brew install capstone` (installed under `/opt/homebrew`).
  - Debian/Ubuntu: `apt-get install libcapstone-dev`.
- Linux amd64 (or macOS arm64/amd64) with cgo enabled.

The Go module has no third-party dependencies; `go mod tidy` is a no-op beyond
recording the Go version. Capstone is linked through a small in-tree cgo binding
in `internal/disasm`, which is why no external Go binding is vendored: the
published bindings target Capstone 3/4 headers and do not compile against
Capstone 5.

If Capstone lives somewhere other than `/opt/homebrew` or `/usr/local`, set:

```sh
export CGO_CFLAGS="-I/path/to/capstone/include"
export CGO_LDFLAGS="-L/path/to/capstone/lib"
```

## Build and test

```sh
go mod tidy
go build ./...
go test ./...
```

Run the test suite with the race detector when changing the concurrency code:

```sh
go test -race ./...
```

## Usage

```sh
# Generate two deterministic demo samples and diff them as JSON.
go run ./cmd/bbdiff --generate-fixtures \
    --old ./.work/old.elf --new ./.work/new.elf --json

# Human-readable report of two existing samples.
go run ./cmd/bbdiff --old ./.work/old.elf --new ./.work/new.elf

# Demonstrate the degraded path: one relocation cannot be normalised, so the
# affected block is reported as uncertain instead of changed.
go run ./cmd/bbdiff --generate-fixtures --new-variant corrupt \
    --old ./.work/old.elf --new ./.work/new.elf

# Remove generated samples after the run.
go run ./cmd/bbdiff --generate-fixtures --clean \
    --old ./.work/old.elf --new ./.work/new.elf --json
```

### Output

JSON output has a stable shape:

```json
{
  "version": 1,
  "summary": {"added": 1, "removed": 1, "changed": 2, "uncertain": 0},
  "blocks": [
    {
      "address": "0x401020",
      "instruction_count": 5,
      "sha256": "<64 hex characters>",
      "function": "audit_gate",
      "kind": "added",
      "new_address": "0x401020"
    }
  ],
  "old": "./.work/old.elf",
  "new": "./.work/new.elf"
}
```

`blocks` always contains only the four diff kinds; matched-equal blocks are
counted in `functions[].matched` but not listed. Each entry has at least
`address`, `instruction_count`, `sha256`, `function`, and `kind`. Matched
entries also carry `old_address`, `new_address`, and `old_sha256`; changed
entries carry `changed_instructions`; uncertain entries carry `confidence` and
`reasons`.

For `removed` blocks, `address` is the old-build address (there is no new
address). For every other kind, `address` is the new-build address.

### Flags

| Flag | Meaning |
| --- | --- |
| `--old PATH`, `--new PATH` | Input samples; required. |
| `--json` | Emit JSON on stdout instead of the text report. |
| `--quiet` | Suppress the text report. |
| `--generate-fixtures` | Write deterministic samples to `--old` / `--new` first. |
| `--new-variant new\|corrupt` | Variant for the generated new sample. |
| `--clean` | Delete the generated samples after the run. |
| `--workers N` | Analysis workers (0 = auto, capped at 8). |
| `--timeout D` | Abort after duration `D` (Go duration syntax); partial results are still returned. |
| `--max-file-size N` | Reject input files larger than `N` bytes (default 512 MiB). |
| `--max-blocks N` | Retain at most `N` blocks per function (default 20000). |
| `--diagnostics`, `--functions` | Toggle the text report sections. |
| `--version` | Print the linked Capstone version. |

### Exit codes

| Code | Meaning |
| --- | --- |
| 0 | Report produced (differences are a normal outcome, not an error). |
| 2 | Usage error (bad flags, missing paths, path traversal in an output path). |
| 3 | An input is not a usable ELF64 x86-64 sample. |
| 4 | Analysis failed, or some functions could not be analysed. |
| 5 | The run was cancelled (signal or `--timeout`); output is partial. |

## Safety and limits

- Only files named on the command line are read. No network access.
- The two input files must be different paths; generated output paths must not
  contain `..`, so a fixture run cannot be tricked into writing outside the
  working tree.
- Fixtures are written through a sibling temporary file and renamed into place,
  so an interrupted run never leaves a partial ELF. `--clean` removes the
  generated samples.
- File size, section size, symbol count, relocation count, and per-function
  instruction/block counts are all bounded. Decoding resynchronises after bad
  bytes, and a per-function failure is isolated.
- Unsupported inputs (ELF32, big-endian, non-x86-64, unsupported object types)
  are rejected with an explicit message rather than being analysed on a
  best-effort basis.

## Repository layout

```
cmd/bbdiff/main.go        flag parsing, run orchestration, exit codes
internal/elfscan/         ELF sections, symbols, relocations, bounds checks
internal/reloc/           relocation normalisation and unresolved reporting
internal/align/           symbol/alias based function alignment
internal/disasm/          Capstone 5 cgo binding (engine lifecycle, decode)
internal/cfg/             basic-block construction and edges
internal/compare/         canonicalisation, SHA-256 hashing, block matching
internal/analyze/         bounded concurrent pipeline and report assembly
internal/report/          text and JSON rendering
internal/fixture/         deterministic ELF sample generator
scripts/verify.sh         build + test + demo reproduction check
```
