// Command mediadump is the local demonstration and acceptance entry point.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/zld/mediacore/internal/selfcheck"
)

func main() {
	selfCheck := flag.Bool("self-check", false, "generate local fixtures and verify the processing kernel")
	flag.Parse()

	if !*selfCheck {
		fmt.Fprintln(os.Stdout, "mediadump: reusable media timeline kernel")
		fmt.Fprintln(os.Stdout, "run: go run ./cmd/mediadump --self-check")
		return
	}

	if err := selfcheck.Run(os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "mediadump: %v\n", err)
		os.Exit(1)
	}
}
