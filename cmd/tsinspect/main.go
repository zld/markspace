// Command tsinspect generates, inspects and slices MPEG-TS files.
//
// Exit codes:
//
//	0  success
//	1  input or parse failure
//	2  invalid arguments
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"tsinspect/internal/demux"
	"tsinspect/internal/fixture"
	"tsinspect/internal/report"
)

const (
	exitOK      = 0
	exitFailure = 1
	exitUsage   = 2
)

// usageError marks argument problems that must exit with code 2.
type usageError struct{ err error }

func (e *usageError) Error() string { return e.err.Error() }
func (e *usageError) Unwrap() error { return e.err }

func usagef(format string, args ...any) error {
	return &usageError{err: fmt.Errorf(format, args...)}
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printUsage(stderr)
		return exitUsage
	}
	var err error
	switch args[0] {
	case "generate":
		err = runGenerate(args[1:], stdout)
	case "inspect":
		err = runInspect(args[1:], stdout)
	case "slice":
		err = runSlice(args[1:], stdout)
	case "-h", "--help", "help":
		printUsage(stdout)
		return exitOK
	default:
		fmt.Fprintf(stderr, "tsinspect: unknown command %q\n", args[0])
		printUsage(stderr)
		return exitUsage
	}
	if err == nil {
		return exitOK
	}
	fmt.Fprintf(stderr, "tsinspect: %v\n", err)
	var ue *usageError
	if errors.As(err, &ue) || errors.Is(err, flag.ErrHelp) {
		return exitUsage
	}
	return exitFailure
}

func printUsage(w io.Writer) {
	fmt.Fprint(w, `tsinspect - MPEG-TS inspection, program-table rebuild and slicing

usage:
  tsinspect generate --out PATH [--scenario NAME]
  tsinspect inspect  --input PATH [--offset N] [--json]
  tsinspect slice    --input PATH --program N --out PATH [--offset N]

scenarios:
  baseline       two programs, one continuity error, truncated tail
  updates-loss   version updates, cross-packet tables, packet loss
  corrupt        invalid CRC, transport error and unknown stream types
  all            every above case in one stream

options:
  --offset N     skip N bytes at the start of the input
  --json         emit the stable JSON summary instead of text
`)
}

func newFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}

func parseFlags(fs *flag.FlagSet, args []string) error {
	if err := fs.Parse(args); err != nil {
		return usagef("%v", err)
	}
	if fs.NArg() > 0 {
		return usagef("unexpected argument %q", fs.Arg(0))
	}
	return nil
}

func runGenerate(args []string, stdout io.Writer) error {
	fs := newFlagSet("generate")
	out := fs.String("out", "", "output TS path")
	scenario := fs.String("scenario", fixture.ScenarioUpdatesLoss, "fixture scenario")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if strings.TrimSpace(*out) == "" {
		return usagef("generate: --out is required")
	}
	valid := false
	for _, s := range fixture.KnownScenarios {
		if s == *scenario {
			valid = true
		}
	}
	if !valid {
		return usagef("generate: unknown scenario %q (want %s)", *scenario, strings.Join(fixture.KnownScenarios, ", "))
	}
	if err := fixture.Generate(*out, *scenario); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "generated %s (scenario %s)\n", *out, *scenario)
	return nil
}

func runInspect(args []string, stdout io.Writer) error {
	fs := newFlagSet("inspect")
	input := fs.String("input", "", "input TS path")
	offset := fs.Int64("offset", 0, "leading byte offset")
	asJSON := fs.Bool("json", false, "emit JSON")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if strings.TrimSpace(*input) == "" {
		return usagef("inspect: --input is required")
	}
	if *offset < 0 {
		return usagef("inspect: --offset must be non-negative")
	}
	s, r, err := demux.ParseFile(*input, *offset)
	if err != nil {
		return fmt.Errorf("inspect: %w", err)
	}
	defer r.Close()
	summary := report.Build(*input, s, r)
	if *asJSON {
		return report.WriteJSON(stdout, summary)
	}
	return report.WriteText(stdout, summary)
}

func runSlice(args []string, stdout io.Writer) error {
	fs := newFlagSet("slice")
	input := fs.String("input", "", "input TS path")
	out := fs.String("out", "", "output TS path")
	program := fs.Int("program", -1, "program number to extract")
	offset := fs.Int64("offset", 0, "leading byte offset")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if strings.TrimSpace(*input) == "" {
		return usagef("slice: --input is required")
	}
	if strings.TrimSpace(*out) == "" {
		return usagef("slice: --out is required")
	}
	if *program < 0 || *program > 0xffff {
		return usagef("slice: --program must be between 0 and 65535")
	}
	if *offset < 0 {
		return usagef("slice: --offset must be non-negative")
	}
	res, err := demux.Slice(demux.SliceRequest{
		InputPath:  *input,
		OutputPath: *out,
		Program:    uint16(*program),
		Offset:     *offset,
	})
	if err != nil {
		return fmt.Errorf("slice: %w", err)
	}
	fmt.Fprintf(stdout, "wrote %s: program %d, PMT version %d, %d packets (%d bytes)\n",
		*out, *program, res.PMTVersion, res.Packets, res.Bytes)
	return nil
}
