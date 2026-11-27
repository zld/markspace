// Command replay is the terminal adapter for the offline RTP replay
// kernel. It generates a self-contained scenario, runs the pipeline and
// prints stage transitions, counts, deficits, conflicts and the manifest
// digest. Exit codes: 0 manifest verified, 1 input or merge failure,
// 2 argument error.
package main

import (
	"fmt"
	"os"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr *os.File) int {
	if len(args) == 0 {
		printUsage(stderr)
		return 2
	}
	switch args[0] {
	case "demo":
		return runDemo(args[1:], stdout, stderr)
	case "verify":
		return runVerify(args[1:], stdout, stderr)
	case "help", "-h", "--help":
		printUsage(stdout)
		return 0
	default:
		fmt.Fprintf(stderr, "replay: unknown command %q\n", args[0])
		printUsage(stderr)
		return 2
	}
}
