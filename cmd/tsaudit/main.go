// tsaudit audits MPEG-TS recordings offline: it tracks PAT/PMT
// versions, attributes packets to the effective program snapshot and
// reports continuity and input anomalies.
//
// Usage:
//
//	tsaudit fixture --out FILE
//	tsaudit analyze --input FILE [--json FILE|-] [--tolerant]
//
// Exit codes: 0 success, 2 corrupt input without --tolerant, 64 usage.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"

	"tsaudit/internal/analysis"
	"tsaudit/internal/fixture"
	"tsaudit/internal/report"
	"tsaudit/internal/transport"
)

const (
	exitOK      = 0
	exitError   = 1
	exitCorrupt = 2
	exitUsage   = 64
)

func main() { os.Exit(run(os.Args[1:])) }

func run(args []string) int {
	if len(args) == 0 {
		usage()
		return exitUsage
	}
	switch args[0] {
	case "fixture":
		return runFixture(args[1:])
	case "analyze":
		return runAnalyze(args[1:])
	default:
		usage()
		return exitUsage
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: tsaudit <command> [flags]")
	fmt.Fprintln(os.Stderr, "  fixture --out FILE")
	fmt.Fprintln(os.Stderr, "  analyze --input FILE [--json FILE|-] [--tolerant]")
}

func runFixture(args []string) int {
	fs := flag.NewFlagSet("fixture", flag.ContinueOnError)
	out := fs.String("out", "", "output file path (required)")
	if err := fs.Parse(args); err != nil || *out == "" {
		usage()
		return exitUsage
	}
	f, err := os.Create(*out)
	if err != nil {
		fmt.Fprintln(os.Stderr, "fixture:", err)
		return exitError
	}
	defer f.Close()
	if err := fixture.Generate(f); err != nil {
		fmt.Fprintln(os.Stderr, "fixture:", err)
		return exitError
	}
	fmt.Fprintf(os.Stderr, "wrote %s\n", *out)
	return exitOK
}

func runAnalyze(args []string) int {
	fs := flag.NewFlagSet("analyze", flag.ContinueOnError)
	in := fs.String("input", "", "input TS file (required)")
	js := fs.String("json", "", "JSON report output path, or - for stdout")
	tolerant := fs.Bool("tolerant", false, "skip corrupt packets and continue")
	if err := fs.Parse(args); err != nil || *in == "" {
		usage()
		return exitUsage
	}
	f, err := os.Open(*in)
	if err != nil {
		fmt.Fprintln(os.Stderr, "analyze:", err)
		return exitError
	}
	defer f.Close()
	rep, err := analysis.Analyze(f, *tolerant)
	if errors.Is(err, transport.ErrCorrupt) {
		fmt.Fprintln(os.Stderr, "analyze: corrupt input structure (use --tolerant to skip bad packets)")
		return exitCorrupt
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "analyze:", err)
		return exitError
	}
	if *js != "" {
		if code := writeJSON(*js, rep); code != exitOK {
			return code
		}
	}
	report.PrintSummary(os.Stdout, rep)
	return exitOK
}

func writeJSON(path string, rep *report.Report) int {
	if path == "-" {
		if err := report.WriteJSON(os.Stdout, rep); err != nil {
			fmt.Fprintln(os.Stderr, "analyze:", err)
			return exitError
		}
		return exitOK
	}
	f, err := os.Create(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "analyze:", err)
		return exitError
	}
	defer f.Close()
	if err := report.WriteJSON(f, rep); err != nil {
		fmt.Fprintln(os.Stderr, "analyze:", err)
		return exitError
	}
	return exitOK
}
