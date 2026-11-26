// Command regress is the terminal adapter over the pkg/media libraries. It
// generates CMAF fixtures, runs the full multi-bitrate repair pipeline with
// resumable checkpoints, and verifies artifacts and cache invalidation.
//
// Exit codes:
//
//	0   completed successfully
//	1   verification mismatch
//	2   invalid input
//	3   cannot safely repair
//	75  injected interrupt (--interrupt-after)
package main

import (
	"fmt"
	"os"
)

// Exit codes mandated by the command contract.
const (
	exitOK           = 0
	exitVerifyFailed = 1
	exitInvalidInput = 2
	exitUnsafe       = 3
	exitInterrupted  = 75
)

// exitError carries a process exit code alongside the message.
type exitError struct {
	code int
	msg  string
}

func (e *exitError) Error() string { return e.msg }

func fail(code int, format string, args ...any) {
	fmt.Fprintf(os.Stderr, "error: "+format+"\n", args...)
	os.Exit(code)
}

// flags is a tiny positional/flag parser sufficient for this CLI.
type flags struct {
	values map[string]string
	bools  map[string]bool
}

func parseFlags(args []string) *flags {
	f := &flags{values: map[string]string{}, bools: map[string]bool{}}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if len(a) < 2 || a[0] != '-' {
			continue
		}
		name := a
		for len(name) > 0 && name[0] == '-' {
			name = name[1:]
		}
		if i+1 < len(args) && args[i+1][0] != '-' {
			f.values[name] = args[i+1]
			i++
		} else {
			f.bools[name] = true
		}
	}
	return f
}

func (f *flags) string_(name string) (string, bool) {
	v, ok := f.values[name]
	return v, ok
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(exitInvalidInput)
	}
	cmd := os.Args[1]
	rest := os.Args[2:]
	var err error
	switch cmd {
	case "fixture":
		err = runFixture(rest)
	case "repair":
		err = runRepair(rest)
	case "verify":
		err = runVerify(rest)
	case "-h", "--help", "help":
		usage()
		return
	default:
		usage()
		os.Exit(exitInvalidInput)
	}
	if err != nil {
		if ee, ok := err.(*exitError); ok {
			fmt.Fprintf(os.Stderr, "error: %s\n", ee.msg)
			os.Exit(ee.code)
		}
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(exitUnsafe)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `regress - CMAF/fMP4 timestamp repair regression tool

Usage:
  regress fixture --out DIR --seed N
  regress repair  --in DIR --out DIR [--interrupt-after N] [--resume]
  regress verify  --in DIR --out DIR --compare DIR --exercise-invalidation

Exit codes: 0 ok | 1 verify mismatch | 2 invalid input | 3 unsafe | 75 interrupted
`)
}
