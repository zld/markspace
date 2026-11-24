// Command mediasync drives the reusable media kernel. It is a thin terminal
// front end: every function it exposes is implemented in internal packages and
// is also tested directly.
package main

import (
	"fmt"
	"os"

	"mediasync/internal/cli"
	"mediasync/internal/output"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: mediasync <demo|verify|resume> [flags]")
		os.Exit(output.ExitFailure)
	}
	var code int
	var err error
	switch os.Args[1] {
	case "demo":
		code, err = cli.Demo(os.Args[2:])
	case "verify":
		code, err = cli.Verify(os.Args[2:])
	case "resume":
		code, err = cli.Resume(os.Args[2:])
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", os.Args[1])
		os.Exit(output.ExitFailure)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
	}
	os.Exit(code)
}
