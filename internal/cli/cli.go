// Package cli parses command-line arguments and routes commands.
package cli

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"mediaseal/internal/media"
	"mediaseal/internal/pipeline"
	"mediaseal/internal/report"
)

// Exit codes: 0 report generated (even when it lists expected validation
// failures), 2 argument or file error, 3 internal invariant failure.
const (
	ExitOK        = 0
	ExitUsage     = 2
	ExitInvariant = 3
)

// Run executes the command line and returns the process exit code.
func Run(args []string, stdout, stderr io.Writer) int {
	if len(args) < 2 {
		usage(stderr)
		return ExitUsage
	}
	switch args[1] {
	case "demo":
		return runDemo(args[2:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "unknown command %q\n", args[1])
		usage(stderr)
		return ExitUsage
	}
}

func usage(w io.Writer) {
	fmt.Fprintln(w, "usage: mediaseal demo --out DIR --segments N --rotate-every N [--inject kind:idx,...] [--format json]")
}

func runDemo(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	fs.SetOutput(stderr)
	out := fs.String("out", "", "output directory for the ciphertext archive and report")
	segments := fs.Int("segments", 0, "number of segments to generate")
	rotateEvery := fs.Int("rotate-every", 0, "segments per data key before rotation")
	inject := fs.String("inject", "", "comma separated fault injections, e.g. tamper:2,replay:4")
	format := fs.String("format", "json", "report format")
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	if *out == "" || *segments < 1 || *rotateEvery < 1 {
		fmt.Fprintln(stderr, "error: --out, --segments >= 1 and --rotate-every >= 1 are required")
		return ExitUsage
	}
	if *format != "json" {
		fmt.Fprintf(stderr, "error: unsupported format %q\n", *format)
		return ExitUsage
	}
	inj, err := parseInject(*inject, *segments)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return ExitUsage
	}

	// A manual clock pinned at a fixed instant keeps every expiry scenario
	// deterministic; no real time ever needs to pass.
	clock := media.NewManualClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	res, err := pipeline.Run(pipeline.Config{
		AssetID:     "demo-asset",
		Segments:    *segments,
		RotateEvery: *rotateEvery,
		Inject:      inj,
		Clock:       clock,
	})
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return ExitInvariant
	}

	if err := os.MkdirAll(*out, 0o755); err != nil {
		fmt.Fprintf(stderr, "error: create output dir: %v\n", err)
		return ExitUsage
	}
	// Archive ciphertext only; plaintext, key material, private keys and
	// license nonces never leave the process.
	for id, blob := range res.Archive {
		path := filepath.Join(*out, id+".cipher")
		if err := os.WriteFile(path, blob, 0o644); err != nil {
			fmt.Fprintf(stderr, "error: write archive: %v\n", err)
			return ExitUsage
		}
	}
	summary := report.Build(res)
	if err := writeReport(filepath.Join(*out, "report.json"), summary); err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return ExitUsage
	}
	if err := report.WriteJSON(stdout, summary); err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return ExitUsage
	}
	return ExitOK
}

func writeReport(path string, summary report.Summary) error {
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create report: %w", err)
	}
	if err := report.WriteJSON(f, summary); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("write report: %w", err)
	}
	return nil
}

// parseInject parses "tamper:2,replay:4,expired-key:7,expired-license:10"
// style specs, validating that every index addresses an existing segment.
func parseInject(spec string, segments int) (pipeline.Injection, error) {
	inj := pipeline.Injection{
		Tamper:         map[int]bool{},
		Replay:         map[int]bool{},
		ExpiredKey:     map[int]bool{},
		ExpiredLicense: map[int]bool{},
	}
	if spec == "" {
		return inj, nil
	}
	for _, part := range strings.Split(spec, ",") {
		kind, value, ok := strings.Cut(part, ":")
		if !ok {
			return inj, fmt.Errorf("invalid injection %q, want kind:index", part)
		}
		idx, err := strconv.Atoi(value)
		if err != nil || idx < 0 || idx >= segments {
			return inj, fmt.Errorf("invalid injection index %q (segments: %d)", value, segments)
		}
		switch kind {
		case "tamper":
			inj.Tamper[idx] = true
		case "replay":
			inj.Replay[idx] = true
		case "expired-key":
			inj.ExpiredKey[idx] = true
		case "expired-license":
			inj.ExpiredLicense[idx] = true
		default:
			return inj, fmt.Errorf("unknown injection kind %q", kind)
		}
	}
	return inj, nil
}
