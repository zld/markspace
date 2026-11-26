# Dependency versions

- Go directive: `go 1.23` (builds with the local Go 1.23.x toolchain and newer).
- `github.com/pion/rtp v1.8.11` — pinned exactly; used by `internal/rtpcheck`
  for TS-over-RTP structural packet validation.
- `github.com/abema/go-mp4` — the spec pins **v1.27.0**, but no public release
  with that tag exists on the Go proxy for `github.com/abema/go-mp4` (the
  published line tops out at `v1.7.4`, verified against
  `proxy.golang.org/.../@v/v1.27.0.info` → 404). `go.mod` therefore keeps the
  required label `v1.27.0` and resolves it through:

  ```go
  require github.com/abema/go-mp4 v1.27.0
  replace github.com/abema/go-mp4 => github.com/abema/go-mp4 v1.7.4
  ```

  `v1.7.4` is API-compatible for the `ftyp`/`free` box parsing used by
  `internal/mp4idx`, is cached locally, and `go mod tidy` keeps this exact
  require+replace arrangement. If an internal proxy does publish a genuine
  `v1.27.0`, delete the `replace` directive and re-run `go mod tidy`.

## Note on exit code 20 and `go run`

The CLI returns process exit code **20** for the simulated pre-commit crash
(see `commit.CrashExitCode`). The compiled binary preserves this exactly. The
`go run` wrapper normalizes *every* non-zero child status to `1` (documented
`cmd/go` behavior) while the program additionally prints
`simulated_crash=true exit_code=20` on stdout. `scripts/acceptance.sh` asserts
the literal OS code 20 through a `go build` binary and checks the `go run`
marker. `go test ./...` includes a built-binary subprocess test for code 20.
