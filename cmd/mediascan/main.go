// Command mediascan is the acceptance entry point for the media ingestion,
// fingerprint dedup and long-term archive recovery pipeline. It generates
// its own fixtures, runs the chunked validation/indexing pipeline, and
// prints the required terminal summary.
//
// Exit codes:
//
//	0 - success (including a verify that matches)
//	1 - invalid arguments or index structure errors
//	2 - corrupt input was encountered and recorded per the rules
package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"mediascan/internal/scan"
)

func main() {
	code := run(os.Args[1:])
	os.Exit(code)
}

func run(args []string) int {
	if len(args) == 0 {
		printUsage()
		return 1
	}
	switch args[0] {
	case "demo":
		return runDemo(args[1:])
	case "verify":
		return runVerify(args[1:])
	case "-h", "--help", "help":
		printUsage()
		return 0
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", args[0])
		printUsage()
		return 1
	}
}

func runDemo(args []string) int {
	fs := newFlagSet("demo")
	out := fs.string("out", "", "output directory for fixtures and index")
	chunk := fs.int("chunk", 2, "chunk size for batch processing")
	resume := fs.bool("resume", false, "run a second pass reusing committed checkpoints")
	if err := fs.parse(args); err != nil {
		fmt.Fprintf(os.Stderr, "argument error: %v\n", err)
		return 1
	}
	if *out == "" {
		fmt.Fprintln(os.Stderr, "demo requires --out <dir>")
		return 1
	}
	if *chunk <= 0 {
		fmt.Fprintf(os.Stderr, "--chunk must be positive, got %d\n", *chunk)
		return 1
	}
	result, err := scan.Run(*out, *chunk, *resume)
	if err != nil {
		fmt.Fprintf(os.Stderr, "scan failed: %v\n", err)
		return 1
	}
	fmt.Print(result.PrintSummary())
	return result.ExitCode
}

func runVerify(args []string) int {
	fs := newFlagSet("verify")
	out := fs.string("out", "", "output directory containing the index")
	if err := fs.parse(args); err != nil {
		fmt.Fprintf(os.Stderr, "argument error: %v\n", err)
		return 1
	}
	if *out == "" {
		fmt.Fprintln(os.Stderr, "verify requires --out <dir>")
		return 1
	}
	code, report, err := scan.Verify(*out)
	fmt.Print(report)
	if err != nil {
		fmt.Fprintf(os.Stderr, "verify failed: %v\n", err)
	}
	return code
}

func printUsage() {
	fmt.Fprintln(os.Stderr, strings.TrimSpace(`
mediascan - DASH/HLS/MP4 validation, fingerprint and recovery scanner

usage:
  mediascan demo   --out <dir> [--chunk N] [--resume]
  mediascan verify --out <dir>

exit codes:
  0  success
  1  invalid arguments or index structure error
  2  corrupt input encountered and recorded per rules
`))
}

// ---- tiny flag parser (stdlib flags would also work, kept local for
// explicit --name value and --bool handling) ----

type flagSet struct {
	strings_ map[string]*string
	ints     map[string]*int
	bools    map[string]*bool
}

func newFlagSet(_ string) *flagSet {
	return &flagSet{
		strings_: map[string]*string{},
		ints:     map[string]*int{},
		bools:    map[string]*bool{},
	}
}

func (f *flagSet) string(name, def, _ string) *string {
	p := &def
	f.strings_[name] = p
	return p
}

func (f *flagSet) int(name string, def int, _ string) *int {
	p := &def
	f.ints[name] = p
	return p
}

func (f *flagSet) bool(name string, def bool, _ string) *bool {
	p := &def
	f.bools[name] = p
	return p
}

func (f *flagSet) parse(args []string) error {
	for i := 0; i < len(args); i++ {
		tok := strings.TrimPrefix(args[i], "--")
		key, inline, hasInline := strings.Cut(tok, "=")
		if bp, ok := f.bools[key]; ok && !hasInline {
			*bp = true
			continue
		}
		var val string
		if hasInline {
			val = inline
		} else {
			if i+1 >= len(args) {
				return fmt.Errorf("flag --%s needs a value", key)
			}
			val = args[i+1]
			i++
		}
		if sp, ok := f.strings_[key]; ok {
			*sp = val
			continue
		}
		if ip, ok := f.ints[key]; ok {
			n, err := strconv.Atoi(val)
			if err != nil {
				return fmt.Errorf("flag --%s expects an integer, got %q", key, val)
			}
			*ip = n
			continue
		}
		return fmt.Errorf("unknown flag --%s", key)
	}
	return nil
}
