package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
)

// exitOK means every checked path matched expectations.
const (
	exitOK       = 0
	exitMismatch = 1
	exitError    = 2
)

// reporter accumulates check results and the human-readable transcript.
type reporter struct {
	w      io.Writer
	failed int
	lines  int
}

func newReporter(w io.Writer) *reporter { return &reporter{w: w} }

func (r *reporter) printf(format string, args ...any) {
	fmt.Fprintf(r.w, format+"\n", args...)
	r.lines++
}

// check records a named expectation. It prints OK/FAIL and returns the result
// so callers can also gate later output.
func (r *reporter) check(name string, ok bool, detail string) bool {
	status := "OK  "
	if !ok {
		status = "FAIL"
		r.failed++
	}
	if detail == "" {
		r.printf("[%s] %s", status, name)
	} else {
		r.printf("[%s] %s: %s", status, name, detail)
	}
	return ok
}

// failf records an unexpected runtime result as a mismatch and prints it.
func (r *reporter) failf(format string, args ...any) {
	r.failed++
	r.printf("[FAIL] "+format, args...)
}

// section prints a heading so the transcript stays readable.
func (r *reporter) section(title string) { r.printf("\n== %s ==", title) }

func (r *reporter) exitCode() int {
	if r.failed > 0 {
		return exitMismatch
	}
	return exitOK
}

// printJSON renders an indented JSON document to the reporter.
func (r *reporter) printJSON(label string, v any) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		r.printf("%s: <encode error: %v>", label, err)
		return
	}
	r.printf("%s:\n%s", label, data)
}

// fatalf reports a runtime/argument error and exits with code 2.
func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "replaypipe: "+format+"\n", args...)
	os.Exit(exitError)
}
