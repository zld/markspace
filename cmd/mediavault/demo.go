package main

import (
	"fmt"
	"os"

	"example.com/mediavault/internal/fault"
	"example.com/mediavault/internal/pipeline"
)

// runDemo generates the fixtures, drives every stage and seals a manifest.
// Fixtures that are expected to be rejected are counted, not treated as a
// command failure.
func runDemo(args []string) int {
	dir, err := parseDir(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return exitCode(err)
	}
	sum, mf, err := pipeline.RunDemo(dir, os.Stdout)
	if err != nil {
		fmt.Fprintf(os.Stderr, "demo failed: %v\n", err)
		code := exitCode(err)
		if code == 0 {
			code = 2
		}
		return code
	}
	pipeline.PrintDemo(os.Stdout, sum)
	fmt.Printf("manifest=%s\n", "manifest.json")
	fmt.Printf("demo_ok=true\n")
	_ = mf
	_ = fault.CodeArgs
	return 0
}
