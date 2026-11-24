// Command mediarc drives the reusable media recovery kernel and prints an
// acceptance-oriented key=value summary.
package main

import (
	"fmt"
	"io"
	"os"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(argv []string, stdout, stderr io.Writer) int {
	if len(argv) == 0 {
		usage(stderr)
		return 1
	}
	var err error
	var code int
	switch argv[0] {
	case "demo":
		code, err = runDemo(argv[1:], stdout)
	case "resume":
		code, err = runResume(argv[1:], stdout)
	case "validate", "verify":
		code, err = runValidate(argv[1:], stdout)
	case "-h", "--help", "help":
		usage(stdout)
		return 0
	default:
		fmt.Fprintf(stderr, "unknown command: %s\n", argv[0])
		usage(stderr)
		return 1
	}
	if err != nil {
		fmt.Fprintf(stderr, "mediarc: %v\n", err)
	}
	return code
}

func usage(w io.Writer) {
	fmt.Fprintln(w, `usage:
  mediarc demo --workdir DIR [--seed N] [--frames N] [--audio-packets N] [--chunk N]
               [--inject bad-sha,truncated,late,duplicate] [--strict-corrupt]
  mediarc resume --workdir DIR [--interrupt-after N] [--av-offset-ms MS]
                 [--source-layout NAME] [--target-layout NAME]
  mediarc validate --workdir DIR

Exit codes: 0 ok, 2 corrupt input, 3 sync/channel configuration, 10 checkpoint interrupt.`)
}
