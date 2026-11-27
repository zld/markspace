// Command tracecheck is the acceptance adapter for the tracecheck pipeline. It
// generates synthetic Matroska/WebM fixtures, parses and indexes them, maps
// client events onto the media timeline, reads ranges and resumes batch runs
// from checkpoints. Every result is printed as stable key=value lines.
//
// Exit codes: 0 success, 1 argument or local I/O failure, 2 damaged input that
// was reported but not fatal to the invocation.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

const usage = `usage: tracecheck demo [flags]

  demo            generate fixtures, index, map events and read ranges
  -work DIR       working directory for fixtures and checkpoints (default ./tmp)
  -chunk N        blocks committed per checkpoint chunk (default 2)
  -resume         resume from a checkpoint written by a previous run

  check FILE      validate one Matroska/WebM file (exit 2 if damaged)
`

// run dispatches subcommands and returns the process exit code.
func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 1
	}
	switch args[0] {
	case "demo":
		return runDemo(args[1:], stdout, stderr)
	case "check":
		return runCheck(args[1:], stdout, stderr)
	case "-h", "--help", "help":
		fmt.Fprint(stdout, usage)
		return 0
	default:
		fmt.Fprintf(stderr, "unknown command %q\n%s", args[0], usage)
		return 1
	}
}

// runDemo implements the demo subcommand.
func runDemo(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	fs.SetOutput(stderr)
	work := fs.String("work", "./tmp", "working directory")
	chunk := fs.Int("chunk", 2, "blocks per checkpoint chunk")
	resume := fs.Bool("resume", false, "resume from a prior checkpoint")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if *chunk <= 0 {
		fmt.Fprintf(stderr, "INVALID_ARGS: chunk must be positive, got %d\n", *chunk)
		return 1
	}
	if *resume {
		fmt.Fprint(stderr, "note: --resume requires a prior non-resume run in --work\n")
	}
	out := &output{w: stdout, err: stderr}
	code := demo(*work, *chunk, *resume, out)
	return code
}
