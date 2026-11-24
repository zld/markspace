package main

import (
	"fmt"
	"os"

	"mediapack/internal/fault"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 {
		usage(os.Stderr)
		return 2
	}
	switch args[0] {
	case "demo":
		return runDemo(args[1:])
	case "verify":
		return runVerify(args[1:])
	case "recover", "process":
		return runRecover(args[1:])
	case "-h", "--help", "help":
		usage(os.Stdout)
		return 0
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", args[0])
		usage(os.Stderr)
		return 2
	}
}

func usage(w *os.File) {
	fmt.Fprintln(w, `mediapack commands:
  demo     --out ./tmp/demo [--chunk-samples 8]
  verify   --dir ./tmp/demo
  recover  --dir ./tmp/demo`)
}

func exitError(err error) int {
	if err == nil {
		return 0
	}
	fmt.Fprintln(os.Stderr, "error:", err)
	return fault.ExitCode(err)
}
