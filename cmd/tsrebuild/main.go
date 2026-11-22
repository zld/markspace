// Command tsrebuild rebuilds program and elementary-stream mappings from an
// MPEG-TS packet stream and reports per-PID statistics.
//
// Exit codes: 0 success; 1 unreadable/unparseable input or runtime IO error;
// 2 invalid command-line arguments.
package main

import (
	"flag"
	"fmt"
	"os"

	"tsrebuild/internal/cli"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 {
		usage()
		return 2
	}
	cmd := args[0]
	rest := args[1:]

	var err error
	switch cmd {
	case "generate":
		err = runGenerate(rest)
	case "inspect":
		err = runInspect(rest)
	case "segment":
		err = runSegment(rest)
	case "-h", "--help", "help":
		usage()
		return 0
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand %q\n\n", cmd)
		usage()
		return 2
	}

	if err != nil {
		if cli.AsUsageError(err) {
			fmt.Fprintf(os.Stderr, "%s\n", err)
			return 2
		}
		fmt.Fprintf(os.Stderr, "error: %s\n", err)
		return 1
	}
	return 0
}

func runGenerate(args []string) error {
	fs := flag.NewFlagSet("generate", flag.ContinueOnError)
	out := fs.String("out", "", "output TS file (required)")
	scenario := fs.String("scenario", "remap-loss", "scenario: basic|remap-loss|bad-crc|fragmented-psi")
	if err := fs.Parse(args); err != nil {
		return &cli.UsageError{Msg: err.Error()}
	}
	return cli.Generate(*out, *scenario)
}

func runInspect(args []string) error {
	fs := flag.NewFlagSet("inspect", flag.ContinueOnError)
	input := fs.String("input", "-", "input TS file, or - for stdin")
	program := fs.Int("program", 1, "program number to analyze")
	asJSON := fs.Bool("json", false, "emit a JSON report")
	outFile := fs.String("report", "", "optional file to write the report to (default stdout)")
	if err := fs.Parse(args); err != nil {
		return &cli.UsageError{Msg: err.Error()}
	}
	w, closeFn, err := reportSink(*outFile)
	if err != nil {
		return err
	}
	defer closeFn()
	return cli.Inspect(*input, *program, *asJSON, w)
}

func runSegment(args []string) error {
	fs := flag.NewFlagSet("segment", flag.ContinueOnError)
	input := fs.String("input", "-", "input TS file, or - for stdin")
	program := fs.Int("program", 1, "program number to extract")
	out := fs.String("out", "", "output TS file for selected packets (required)")
	asJSON := fs.Bool("json", false, "emit a JSON segment report")
	reportFile := fs.String("report", "", "optional file for the segment report (default stdout)")
	if err := fs.Parse(args); err != nil {
		return &cli.UsageError{Msg: err.Error()}
	}
	w, closeFn, err := reportSink(*reportFile)
	if err != nil {
		return err
	}
	defer closeFn()
	return cli.Segment(*input, *program, *out, *asJSON, w)
}

// reportSink opens the report destination: stdout unless a file is named.
func reportSink(path string) (w *os.File, closeFn func(), err error) {
	if path == "" || path == "-" {
		return os.Stdout, func() {}, nil
	}
	f, ferr := os.Create(path)
	if ferr != nil {
		return nil, nil, ferr
	}
	return f, func() { f.Close() }, nil
}

func usage() {
	fmt.Fprintln(os.Stderr, `tsrebuild - rebuild MPEG-TS program mappings and per-PID stats

usage:
  tsrebuild generate --out FILE [--scenario remap-loss|basic|bad-crc|fragmented-psi]
  tsrebuild inspect  --input FILE|- [--program N] [--json] [--report FILE]
  tsrebuild segment  --input FILE|- --program N --out OUT.ts [--json] [--report FILE]

exit codes: 0 ok, 1 read/parse failure, 2 bad arguments`)
}
