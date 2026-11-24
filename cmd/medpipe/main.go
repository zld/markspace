// Command medpipe is the terminal acceptance entry point for the reusable
// media processing kernel.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"medpipe/internal/demo"
	"medpipe/internal/fault"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: medpipe demo --seed N --faults list [--resume] [--out DIR]")
		return fault.ExitUsage
	}
	switch args[0] {
	case "demo":
		return runDemo(args[1:], stdout, stderr)
	case "-h", "--help", "help":
		fmt.Fprintln(stdout, "medpipe demo [--seed 17] [--faults ...] [--resume] [--out medpipe-output]")
		return fault.ExitOK
	default:
		fmt.Fprintf(stderr, "unknown command %q; expected demo\n", args[0])
		return fault.ExitUsage
	}
}

func runDemo(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	fs.SetOutput(stderr)
	seed := fs.Int64("seed", 17, "deterministic fixture seed")
	faults := fs.String("faults", "", "comma-separated faults: duplicate,reorder,burst-loss,cc-jump,psi-version,truncate")
	resume := fs.Bool("resume", false, "recover the simulated interrupted transaction")
	out := fs.String("out", "medpipe-output", "output directory")
	if err := fs.Parse(args); err != nil {
		return fault.ExitUsage
	}
	list := []string{}
	for _, f := range strings.Split(*faults, ",") {
		f = strings.TrimSpace(f)
		if f != "" {
			list = append(list, f)
		}
	}
	report := demo.Run(demo.Config{OutDir: *out, Seed: *seed, Faults: list, Resume: *resume})
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(report); err != nil {
		fmt.Fprintf(stderr, "encode report: %v\n", err)
		return fault.ExitInternal
	}
	return report.ExitCode
}
