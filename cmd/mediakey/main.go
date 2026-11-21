// Command mediakey is the local publish-verification tool for encrypted,
// on-demand media shards. It simulates key rotation and client-side license
// validation entirely in-process and leaves a per-shard audit report.
//
// Exit codes:
//
//	0 - run completed and matched the injected/expected scenario
//	1 - internal or file error
//	2 - bad command-line arguments
//	3 - actual results differ from the expected scenario
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"mediakey/internal/model"
)

// Exit codes forming the tool's stable contract.
const (
	exitOK       = 0
	exitInternal = 1
	exitUsage    = 2
	exitMismatch = 3
)

const baseTime = "2026-11-01T09:00:00Z"

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printRootUsage(stderr)
		return exitUsage
	}
	switch args[0] {
	case "demo":
		return runDemo(args[1:], stdout, stderr)
	case "bootstrap":
		return runBootstrap(args[1:], stdout, stderr)
	case "process":
		return runProcess(args[1:], stdout, stderr)
	case "-h", "--help", "help":
		printRootUsage(stdout)
		return exitOK
	default:
		fmt.Fprintf(stderr, "unknown subcommand: %s\n", args[0])
		printRootUsage(stderr)
		return exitUsage
	}
}

func printRootUsage(w io.Writer) {
	fmt.Fprint(w, `mediakey - local media shard publish verification

usage:
  mediakey demo --scenario <ok|mixed|chaos> --shards N --rotate-every N
                --workers N [--report path] [--seed N]
  mediakey bootstrap --dir path [--shards N] [--scenario name] [--seed N]
  mediakey process --dir path --rotate-every N --workers N [--report path]

exit codes: 0 ok, 1 internal/file error, 2 usage error, 3 result mismatch
`)
}

// parseFlagSet creates a flag set that reports errors to stderr instead of
// exiting the process, so main can translate them into exit code 2.
func parseFlagSet(name string, stderr io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprintf(stderr, "usage: mediakey %s [flags]\n", name)
		fs.PrintDefaults()
	}
	return fs
}

// baseClock returns the fixed, reproducible clock shared by every command.
func baseClock() model.Clock {
	t, err := time.Parse(time.RFC3339, baseTime)
	if err != nil {
		panic(err)
	}
	return model.FixedClock{T: t}
}
