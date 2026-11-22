// Command mediapack is the terminal entry point of the demo media
// processing kernel. Exit codes: 0 ok, 2 parameter error, 3 corrupt
// asset/graph, 4 resume validation failure.
package main

import (
	"flag"
	"fmt"
	"os"

	"mediapack/demo"
	"mediapack/merr"
)

func main() { os.Exit(run(os.Args[1:])) }

func run(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: mediapack <demo|verify-fixtures> [flags]")
		return 2
	}
	switch args[0] {
	case "demo":
		fs := flag.NewFlagSet("demo", flag.ContinueOnError)
		seed := fs.Int64("seed", 17, "deterministic input seed")
		out := fs.String("out", "", "output directory (required)")
		resume := fs.Bool("resume", false, "simulate a crash and resume from checkpoints")
		if err := fs.Parse(args[1:]); err != nil {
			return 2
		}
		if err := demo.Run(*seed, *out, *resume); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			return merr.ExitCode(err)
		}
		return 0
	case "verify-fixtures":
		fs := flag.NewFlagSet("verify-fixtures", flag.ContinueOnError)
		out := fs.String("out", "", "work directory (default: temp dir)")
		if err := fs.Parse(args[1:]); err != nil {
			return 2
		}
		if err := demo.VerifyFixtures(*out); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			return merr.ExitCode(err)
		}
		return 0
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", args[0])
		fmt.Fprintln(os.Stderr, "usage: mediapack <demo|verify-fixtures> [flags]")
		return 2
	}
}
