// Command archivecheck analyses local archive sources and produces a verified,
// resumable manifest. Run "archivecheck demo" to exercise every subsystem
// against generated samples.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// Exit codes: 0 means every assertion passed, 1 means an assertion or analysis
// step failed, 2 means the command line was wrong.
const (
	exitOK    = 0
	exitFail  = 1
	exitUsage = 2
)

func main() {
	os.Exit(run(context.Background(), os.Args[1:], os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: archivecheck <demo|verify> [flags]")
		return exitUsage
	}
	switch args[0] {
	case "demo":
		return runDemoCommand(ctx, args[1:], stdout, stderr)
	case "verify":
		return runVerifyCommand(args[1:], stdout, stderr)
	case "help", "-h", "--help":
		fmt.Fprintln(stdout, usageText)
		return exitOK
	default:
		fmt.Fprintf(stderr, "archivecheck: unknown command %q\n%s\n", args[0], usageText)
		return exitUsage
	}
}

const usageText = `usage:
  archivecheck demo   --out DIR [--chunk BYTES] [--queue N]
  archivecheck verify --out DIR

demo generates local samples, runs bounded pipeline analysis with a resumable
checkpoint, reports damage and keyframes, and writes a verified manifest.
verify re-checks an existing manifest and its derivative store.`

func runDemoCommand(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	fs.SetOutput(stderr)
	out := fs.String("out", "", "output directory for samples, state and the manifest")
	chunk := fs.Int("chunk", 4096, "pipeline chunk size in bytes")
	queue := fs.Int("queue", 2, "pipeline queue depth in chunks")
	limit := fs.Int64("limit", 64<<20, "maximum bytes read from one source")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if *out == "" {
		fmt.Fprintln(stderr, "archivecheck demo: --out is required")
		return exitUsage
	}
	if fs.NArg() != 0 {
		fmt.Fprintf(stderr, "archivecheck demo: unexpected arguments %v\n", fs.Args())
		return exitUsage
	}
	if err := runDemo(ctx, stdout, demoOptions{Out: *out, Chunk: *chunk, Queue: *queue, Limit: *limit}); err != nil {
		fmt.Fprintf(stderr, "\nFAILED: %v\n", err)
		return exitFail
	}
	return exitOK
}

func runVerifyCommand(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	fs.SetOutput(stderr)
	out := fs.String("out", "", "archive directory holding manifest.json")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if *out == "" {
		fmt.Fprintln(stderr, "archivecheck verify: --out is required")
		return exitUsage
	}
	return verifyArchive(*out, stdout, stderr)
}

// verifyArchive re-checks the manifest and every derivative file.
func verifyArchive(dir string, stdout, stderr io.Writer) int {
	m, err := verifyManifest(dir)
	if err != nil {
		fmt.Fprintf(stderr, "verify: %v\n", err)
		return exitFail
	}
	sum, err := m.Checksum()
	if err != nil {
		fmt.Fprintf(stderr, "verify: %v\n", err)
		return exitFail
	}
	abs, _ := filepath.Abs(dir)
	fmt.Fprintf(stdout, "verify: %s\n", abs)
	fmt.Fprintf(stdout, "sources=%d tracks=%d damage=%d ranges=%d derivatives=%d\n",
		len(m.Sources), len(m.Tracks), len(m.Damage), len(m.TimeRanges), len(m.Derivatives))
	fmt.Fprintf(stdout, "counts: added=%d reused=%d changed=%d invalidated=%d\n",
		m.Counts.Added, m.Counts.Reused, m.Counts.Changed, m.Counts.Invalidated)
	fmt.Fprintf(stdout, "manifest checksum=%s\n", sum)
	fmt.Fprintln(stdout, "result: OK")
	return exitOK
}
