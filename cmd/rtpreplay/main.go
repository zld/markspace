// Command rtpreplay is the terminal acceptance adapter for the offline RTP
// trajectory regression. It generates a fixture from a seed, runs the parse /
// metrics / manifest DAG, and prints the quantitative fields an operator
// checks, optionally writing the full JSON report.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	rtpreplay "github.com/zld/rtpreplay/internal"
)

// Exit codes are part of the CLI contract.
const (
	exitOK      = 0
	exitFailure = 1
	exitUsage   = 2
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 {
		return usageError("missing command")
	}
	switch args[0] {
	case "demo":
		return runDemo(args[1:])
	case "cases":
		for _, c := range rtpreplay.AllCases {
			fmt.Println(c)
		}
		return exitOK
	case "-h", "--help", "help":
		printUsage(os.Stdout)
		return exitOK
	default:
		return usageError(fmt.Sprintf("unknown command %q", args[0]))
	}
}

func printUsage(w io.Writer) {
	fmt.Fprint(w, `rtpreplay - offline RTP trajectory regression

Usage:
  rtpreplay demo [--case NAME] [--seed N] [--out FILE] [--depth N]

Cases:
  normal, wrap-reorder, duplicate, burst-loss, malformed-opus,
  timestamp-jump, blocked-dag

Exit codes: 0 success, 1 input/processing failure, 2 usage error.
`)
}

func usageError(msg string) int {
	fmt.Fprintf(os.Stderr, "rtpreplay: %s\n", msg)
	fmt.Fprintln(os.Stderr, "run `rtpreplay help` for usage")
	return exitUsage
}

func runDemo(args []string) int {
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var (
		caseName = fs.String("case", rtpreplay.CaseNormal, "fixture case")
		seed     = fs.Int64("seed", 17, "fixture seed")
		out      = fs.String("out", "", "write JSON report to this path")
		depth    = fs.Int("depth", 4, "reorder window depth in packets")
	)
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() != 0 {
		return usageError(fmt.Sprintf("unexpected arguments: %v", fs.Args()))
	}
	if *depth < 0 {
		return usageError("--depth must be non-negative")
	}
	known := false
	for _, c := range rtpreplay.AllCases {
		if c == *caseName {
			known = true
		}
	}
	if !known {
		return usageError(fmt.Sprintf("unknown case %q", *caseName))
	}

	res := rtpreplay.Execute(rtpreplay.RunOptions{
		Case:  *caseName,
		Seed:  *seed,
		Depth: *depth,
	})
	fmt.Fprint(os.Stdout, res.Summary)
	if res.Report != nil && *out != "" {
		if err := rtpreplay.WriteReport(*out, res.Report); err != nil {
			fmt.Fprintf(os.Stderr, "rtpreplay: write report: %v\n", err)
			return exitFailure
		}
		fmt.Printf("report_written=%s\n", *out)
	}
	if res.Err != nil {
		fmt.Fprintf(os.Stderr, "rtpreplay: %v\n", res.Err)
	}
	return res.ExitCode
}
