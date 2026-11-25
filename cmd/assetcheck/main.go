// Command assetcheck validates locally generated multi-language assets.
//
// Usage:
//
//	assetcheck demo --seed N --out DIR [--reuse-index]
//	assetcheck check --mp4 FILE --rtp FILE [--index FILE]
//
// Exit codes are 0 for a pass, 1 for an asset that failed validation and 2
// for a usage or run error.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
)

const (
	exitPass = 0
	exitFail = 1
	exitErr  = 2
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, usage)
		return exitErr
	}
	switch args[0] {
	case "demo":
		return runDemo(args[1:], stdout, stderr)
	case "check":
		return runCheck(args[1:], stdout, stderr)
	case "-h", "--help", "help":
		fmt.Fprintln(stdout, usage)
		return exitPass
	default:
		fmt.Fprintf(stderr, "assetcheck: unknown command %q\n%s\n", args[0], usage)
		return exitErr
	}
}

const usage = `usage:
  assetcheck demo  --seed N --out DIR [--reuse-index]
  assetcheck check --mp4 FILE --rtp FILE [--index FILE]

exit codes: 0 pass, 1 asset validation failure, 2 usage or run error`

// usageError marks a flag or argument problem.
type usageError struct{ msg string }

func (e *usageError) Error() string { return e.msg }

func usagef(format string, args ...interface{}) error {
	return &usageError{msg: fmt.Sprintf(format, args...)}
}

func isUsage(err error) bool {
	var u *usageError
	return errors.As(err, &u)
}

// newFlagSet builds a flag set that reports errors instead of exiting.
func newFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}
