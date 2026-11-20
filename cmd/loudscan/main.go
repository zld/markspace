// Command loudscan measures EBU R128 loudness and true peak of local audio
// files and fails builds whose assets exceed the delivery limits.
package main

import (
	"fmt"
	"os"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr *os.File) int {
	fmt.Fprintln(stderr, "loudscan: not implemented yet")
	return 2
}
