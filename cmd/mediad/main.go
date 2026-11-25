// mediad is the terminal entry point that drives the reusable media-processing
// library. It only orchestrates fixtures and prints an acceptance summary; all
// real logic lives in the internal packages.
//
// Usage:
//
//	mediad demo --out <dir> [--scenario all|normal|boundary|corrupt]
//
// Exit codes: 0 success, 1 usage error, 2 corrupt input, 3 recovery mismatch.
package main

import (
	"fmt"
	"os"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 {
		usage()
		return 1
	}
	switch args[0] {
	case "demo":
		return runDemoCmd(args[1:])
	case "-h", "--help", "help":
		usage()
		return 0
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", args[0])
		usage()
		return 1
	}
}

func runDemoCmd(args []string) int {
	var out, scenario string
	scenario = "all"
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--out":
			i++
			if i >= len(args) {
				fmt.Fprintln(os.Stderr, "--out requires a directory argument")
				return 1
			}
			out = args[i]
		case "--scenario":
			i++
			if i >= len(args) {
				fmt.Fprintln(os.Stderr, "--scenario requires an argument")
				return 1
			}
			scenario = args[i]
		default:
			fmt.Fprintf(os.Stderr, "unknown flag %q\n", args[i])
			return 1
		}
	}
	if out == "" {
		fmt.Fprintln(os.Stderr, "missing required --out directory")
		return 1
	}
	switch scenario {
	case "all", "normal", "boundary", "corrupt":
	default:
		fmt.Fprintf(os.Stderr, "invalid --scenario %q\n", scenario)
		return 1
	}
	rep, code := runDemo(out, scenario)
	if err := printJSON(rep); err != nil {
		fmt.Fprintf(os.Stderr, "failed to write report: %v\n", err)
		return 1
	}
	return code
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: mediad demo --out <dir> [--scenario all|normal|boundary|corrupt]")
}
