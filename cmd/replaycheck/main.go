package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
)

func main() {
	os.Exit(realMain(os.Args[1:]))
}

func realMain(args []string) int {
	fs := flag.NewFlagSet("replaycheck", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	selfTest := fs.Bool("self-test", false, "build fixtures, run both scenarios and print the JSON report")
	out := fs.String("out", "", "directory for fixtures, fragments and state (required with --self-test)")
	task := fs.String("task", "replaycheck/selftest", "task key used for the run")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if !*selfTest {
		fmt.Fprintln(os.Stderr, "replaycheck: only --self-test is implemented; run with --self-test --out <dir>")
		return 2
	}
	if *out == "" {
		fmt.Fprintln(os.Stderr, "replaycheck: --out is required")
		return 2
	}
	absOut, err := filepath.Abs(*out)
	if err != nil {
		fmt.Fprintf(os.Stderr, "replaycheck: %v\n", err)
		return 2
	}
	res, err := runSelfTest(absOut, *task)
	if err != nil {
		fmt.Fprintf(os.Stderr, "replaycheck: self-test failed: %v\n", err)
		return 1
	}
	if err := res.emit(os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "replaycheck: %v\n", err)
		return 1
	}
	if !res.OK() {
		fmt.Fprintln(os.Stderr, "replaycheck: one or more assertions failed")
		return 1
	}
	return 0
}
