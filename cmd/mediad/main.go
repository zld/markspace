// Command mediad is the terminal entry point. It only parses arguments and
// invokes the reusable library; all processing lives in internal packages.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"mediad/internal/demo"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}
	switch os.Args[1] {
	case "demo":
		os.Exit(runDemo(os.Args[2:]))
	case "help", "-h", "--help":
		usage()
		os.Exit(0)
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand %q\n", os.Args[1])
		usage()
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `mediad — traceable media ingest/archive pipeline

usage:
  mediad demo --work <dir> --seed <int>   run the end-to-end acceptance demo
  mediad help                             show this help

The demo generates H.264/H.265/AV1 success samples, corrupt variants and an
interruption-residue case under <dir>, and prints one JSON object per case.
It exits 0 only when every expected result is satisfied.
`)
}

func runDemo(args []string) int {
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	work := fs.String("work", "./tmp/acceptance", "working directory for generated artifacts")
	seed := fs.Int64("seed", 17, "deterministic fixture seed")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	rep, err := demo.Run(context.Background(), *work, *seed)
	if err != nil {
		fmt.Fprintf(os.Stderr, "demo failed: %v\n", err)
		return 1
	}
	enc := json.NewEncoder(os.Stdout)
	for _, r := range rep.Rows {
		if err := enc.Encode(r); err != nil {
			fmt.Fprintf(os.Stderr, "encode row: %v\n", err)
			return 1
		}
	}
	// Summary line with per-path counts.
	summary := map[string]any{
		"case":                 "summary",
		"success":              rep.Summary.Success,
		"corrupt":              rep.Summary.Corrupt,
		"restored":             rep.Summary.Restored,
		"duplicate_deliveries": rep.Summary.Duplicates,
		"checks_failed":        rep.Summary.ChecksFailed,
		"manifest":             rep.ManifestPath,
		"all_expectations_met": rep.AllExpectationsMet,
	}
	if err := enc.Encode(summary); err != nil {
		return 1
	}
	if !rep.AllExpectationsMet {
		return 1
	}
	return 0
}
