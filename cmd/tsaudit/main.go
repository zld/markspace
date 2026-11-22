// Command tsaudit rebuilds versioned PAT/PMT mappings from a raw
// MPEG-TS stream, slices packets by mapping interval and reports
// per-stream packet counts and continuity anomalies.
//
// Usage:
//
//	tsaudit demo [--gap] [--json]      run a generated demo scenario
//	tsaudit audit <file.ts> [--json]   audit a TS file
//	tsaudit fixture <out.ts> [--gap]   write a demo fixture to disk
//
// Exit codes: 0 = parsed (incomplete updates may be flagged),
// 2 = argument or input error, 3 = no valid PAT/PMT found.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"tsaudit/internal/fixture"
	"tsaudit/internal/report"
	"tsaudit/internal/slicer"
)

const (
	exitOK = iota
	_
	exitUsage
	exitNoTables
)

func main() { os.Exit(run(os.Args[1:])) }

func run(args []string) int {
	if len(args) == 0 {
		usage()
		return exitUsage
	}
	switch args[0] {
	case "demo":
		fs := flag.NewFlagSet("demo", flag.ContinueOnError)
		gap := fs.Bool("gap", false, "use the fragmented-PMT scenario with a continuity gap")
		asJSON := fs.Bool("json", false, "emit JSON instead of a terminal summary")
		if err := fs.Parse(args[1:]); err != nil {
			return exitUsage
		}
		data, name := fixture.Remap(), "demo:remap"
		if *gap {
			data, name = fixture.RemapWithGap(), "demo:remap-gap"
		}
		return audit(name, bytes.NewReader(data), *asJSON)
	case "audit":
		fs := flag.NewFlagSet("audit", flag.ContinueOnError)
		asJSON := fs.Bool("json", false, "emit JSON instead of a terminal summary")
		if err := fs.Parse(flagsFirst(args[1:])); err != nil || fs.NArg() != 1 {
			usage()
			return exitUsage
		}
		f, err := os.Open(fs.Arg(0))
		if err != nil {
			fmt.Fprintf(os.Stderr, "tsaudit: %v\n", err)
			return exitUsage
		}
		defer f.Close()
		return audit(fs.Arg(0), f, *asJSON)
	case "fixture":
		fs := flag.NewFlagSet("fixture", flag.ContinueOnError)
		gap := fs.Bool("gap", false, "write the fragmented-PMT scenario")
		if err := fs.Parse(flagsFirst(args[1:])); err != nil || fs.NArg() != 1 {
			usage()
			return exitUsage
		}
		data := fixture.Remap()
		if *gap {
			data = fixture.RemapWithGap()
		}
		if err := os.WriteFile(fs.Arg(0), data, 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "tsaudit: %v\n", err)
			return exitUsage
		}
		fmt.Fprintf(os.Stderr, "wrote %d bytes (%d packets) to %s\n",
			len(data), len(data)/188, fs.Arg(0))
		return exitOK
	default:
		usage()
		return exitUsage
	}
}

// audit runs the pipeline over r and renders the result.
func audit(input string, r io.Reader, asJSON bool) int {
	res, err := slicer.Run(r)
	if err != nil {
		fmt.Fprintf(os.Stderr, "tsaudit: %v\n", err)
		return exitUsage
	}
	doc := report.Build(input, res)
	if asJSON {
		if err := report.WriteJSON(os.Stdout, doc); err != nil {
			fmt.Fprintf(os.Stderr, "tsaudit: %v\n", err)
			return exitUsage
		}
	} else {
		report.WriteSummary(os.Stdout, doc)
	}
	if !res.HasTables {
		return exitNoTables
	}
	return exitOK
}

// flagsFirst reorders args so flags are parsed even when they follow
// positional arguments (flag.Parse stops at the first positional).
func flagsFirst(args []string) []string {
	var flags, positional []string
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			flags = append(flags, a)
		} else {
			positional = append(positional, a)
		}
	}
	return append(flags, positional...)
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage:
  tsaudit demo [--gap] [--json]     run a generated demo scenario
  tsaudit audit <file.ts> [--json]  audit a TS file
  tsaudit fixture <out.ts> [--gap]  write a demo fixture to disk
exit codes: 0 ok, 2 argument/input error, 3 no valid PAT/PMT`)
}
