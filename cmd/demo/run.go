package main

import (
	"fmt"
	"os"
)

// runPipeline is replaced by the full pipeline in a follow-up commit.
func runPipeline(o options) int {
	fmt.Fprintln(os.Stderr, "demo: pipeline not yet implemented")
	return 1
}
