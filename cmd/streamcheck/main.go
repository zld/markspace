package main

import (
	"fmt"
	"os"

	"streamcheck/core"
)

func main() { os.Exit(run(os.Args[1:], os.Stdout)) }

// run is replaced by the full command dispatch in a follow-up patch.
func run(args []string, w *os.File) int {
	fmt.Fprintln(w, "streamcheck: command dispatch not implemented yet")
	return core.ExitUsage
}
