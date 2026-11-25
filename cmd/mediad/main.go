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
	"strconv"
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
	opts := defaultDemoOptions()
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--out":
			i++
			if i >= len(args) {
				fmt.Fprintln(os.Stderr, "--out requires a directory argument")
				return 1
			}
			opts.out = args[i]
		case "--scenario":
			i++
			if i >= len(args) {
				fmt.Fprintln(os.Stderr, "--scenario requires an argument")
				return 1
			}
			opts.scenario = args[i]
		case "--max-file-bytes":
			i++
			v, ok := parseIntFlag(args, i, &opts.maxFileBytes)
			if !ok {
				return 1
			}
			_ = v
		case "--max-box-bytes":
			i++
			if _, ok := parseIntFlag(args, i, &opts.maxBoxBytes); !ok {
				return 1
			}
		case "--max-samples":
			i++
			if _, ok := parseIntFlag(args, i, &opts.maxSamples); !ok {
				return 1
			}
		default:
			fmt.Fprintf(os.Stderr, "unknown flag %q\n", args[i])
			return 1
		}
	}
	if opts.out == "" {
		fmt.Fprintln(os.Stderr, "missing required --out directory")
		return 1
	}
	switch opts.scenario {
	case "all", "normal", "boundary", "corrupt":
	default:
		fmt.Fprintf(os.Stderr, "invalid --scenario %q\n", opts.scenario)
		return 1
	}
	if opts.maxFileBytes <= 0 || opts.maxBoxBytes <= 0 || opts.maxSamples <= 0 {
		fmt.Fprintln(os.Stderr, "limit flags must be positive integers")
		return 1
	}
	rep, code := runDemo(opts)
	if err := printJSON(rep); err != nil {
		fmt.Fprintf(os.Stderr, "failed to write report: %v\n", err)
		return 1
	}
	return code
}

func parseIntFlag(args []string, idx int, dst *int64) (int64, bool) {
	if idx >= len(args) {
		fmt.Fprintf(os.Stderr, "flag requires a numeric argument\n")
		return 0, false
	}
	v, err := strconv.ParseInt(args[idx], 10, 64)
	if err != nil {
		fmt.Fprintf(os.Stderr, "invalid numeric argument %q: %v\n", args[idx], err)
		return 0, false
	}
	*dst = v
	return v, true
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: mediad demo --out <dir> [--scenario all|normal|boundary|corrupt]")
	fmt.Fprintln(os.Stderr, "       [--max-file-bytes N] [--max-box-bytes N] [--max-samples N]")
}
