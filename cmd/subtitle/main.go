// Command subtitle converts SRT, WebVTT and ASS subtitles onto an integer
// frame timeline, resolves overlaps and verifies every conversion by parsing
// its own output back. It is a pure terminal program with no GUI dependencies.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"subtitle/internal/cli"
	"subtitle/internal/fixture"
	"subtitle/internal/pipeline"
	"subtitle/internal/report"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Fprint(stdout, cli.HelpText)
		return cli.ExitOK
	}
	switch args[0] {
	case "fixture":
		return runFixture(args[1:], stdout, stderr)
	case "verify":
		return runVerify(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "unknown subcommand %q\n\n%s", args[0], cli.HelpText)
		return cli.ExitUsageOrIOError
	}
}

func runFixture(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("fixture", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	dir := fs.String("dir", "", "directory to write sample subtitle files into")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintf(stderr, "fixture: %v\n", err)
		return cli.ExitUsageOrIOError
	}
	if *dir == "" {
		fmt.Fprintln(stderr, "fixture: --dir is required")
		return cli.ExitUsageOrIOError
	}
	if err := os.MkdirAll(*dir, 0o755); err != nil {
		fmt.Fprintf(stderr, "fixture: %v\n", err)
		return cli.ExitUsageOrIOError
	}
	paths, err := fixture.Write(*dir)
	if err != nil {
		fmt.Fprintf(stderr, "fixture: %v\n", err)
		return cli.ExitUsageOrIOError
	}
	for _, p := range paths {
		fmt.Fprintf(stdout, "wrote %s\n", p)
	}
	return cli.ExitOK
}

func runVerify(args []string, stdout, stderr io.Writer) int {
	va, err := cli.ParseVerify(args)
	if err != nil {
		fmt.Fprintf(stderr, "verify: %v\n\n%s", err, cli.HelpText)
		return cli.ExitUsageOrIOError
	}
	info, err := os.Stat(va.Input)
	if err != nil {
		fmt.Fprintf(stderr, "verify: %v\n", err)
		return cli.ExitUsageOrIOError
	}
	if !info.IsDir() {
		if va.Output == "" {
			fmt.Fprintln(stderr, "verify: --output is required for a single input file")
			return cli.ExitUsageOrIOError
		}
		return runSingle(va, stdout, stderr)
	}
	return runDirectory(va, stdout, stderr)
}

func runSingle(va cli.VerifyArgs, stdout, stderr io.Writer) int {
	opts := va.AsPipelineOptions(va.Input, va.Output)
	res := pipeline.Run(opts)
	report.File(stdout, res)
	if res.Error != "" {
		// Encoding, file and content parse problems surface here; a tolerance
		// breach is reported the same way and treated as a verification failure.
		if isVerifyError(res.Error) {
			return cli.ExitVerifyFailure
		}
		return cli.ExitUsageOrIOError
	}
	return cli.ExitOK
}

func runDirectory(va cli.VerifyArgs, stdout, stderr io.Writer) int {
	paths, err := pipeline.DiscoverInputs(va.Input)
	if err != nil {
		fmt.Fprintf(stderr, "verify: %v\n", err)
		return cli.ExitUsageOrIOError
	}
	if len(paths) == 0 {
		fmt.Fprintf(stderr, "verify: no supported subtitle files under %s\n", va.Input)
		return cli.ExitUsageOrIOError
	}
	options := make([]pipeline.Options, len(paths))
	for i, p := range paths {
		rel, _ := filepath.Rel(va.Input, p)
		outName := strings.TrimSuffix(rel, filepath.Ext(rel)) + "." + string(va.To)
		options[i] = va.AsPipelineOptions(p, filepath.Join(va.Input, "converted", outName))
	}
	results := pipeline.Batch(options, va.Jobs)
	summary := report.Batch(stdout, results)
	if summary.Failed > 0 {
		return cli.ExitPartialBatch
	}
	return cli.ExitOK
}

// isVerifyError distinguishes round-trip/content failures (exit 1) from file,
// encoding and usage failures (exit 2).
func isVerifyError(msg string) bool {
	for _, marker := range []string{"round-trip", "non-positive cue", "shorter than one frame", "frame number overflow"} {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
}
