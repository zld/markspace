// Command mediavault is the acceptance adapter for the offline media
// processing kernel. It generates deterministic MP4/RTP fixtures, runs the
// ingest -> normalize -> fingerprint -> archive stages and prints a JSON
// report. Exit code 0 means success; 2 means corrupt input or an
// unrecoverable error.
package main

import (
	"flag"
	"fmt"
	"os"

	"mediavault/internal/pipeline"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "demo":
		os.Exit(runDemo(os.Args[2:]))
	case "resume":
		os.Exit(runResume(os.Args[2:]))
	case "-h", "--help", "help":
		usage()
		os.Exit(0)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", os.Args[1])
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `mediavault - offline media processing kernel

Usage:
  mediavault demo   --work DIR [--fault none|truncated-box|boundary-jump]
                    [--interrupt-after N]
  mediavault resume --work DIR
`)
}

type commonFlags struct {
	work           string
	fault          string
	interruptAfter int
}

func parseCommon(args string) (*flag.FlagSet, *commonFlags) {
	fs := flag.NewFlagSet(args, flag.ContinueOnError)
	c := &commonFlags{}
	fs.StringVar(&c.work, "work", "./tmp", "working directory")
	fs.StringVar(&c.fault, "fault", pipeline.FaultNone, "fault scenario")
	fs.IntVar(&c.interruptAfter, "interrupt-after", 0, "interrupt after N chunks")
	return fs, c
}

func runDemo(args []string) int {
	fs, c := parseCommon("demo")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if !validFault(c.fault) {
		fmt.Fprintf(os.Stderr, "invalid fault %q\n", c.fault)
		return 2
	}
	eng := pipeline.NewEngine(pipeline.Config{
		WorkDir:        c.work,
		Fault:          c.fault,
		InterruptAfter: c.interruptAfter,
		ChunkSize:      4,
	})
	m, err := eng.Run()
	if m != nil {
		out, _ := m.JSON()
		fmt.Println(string(out))
	}
	if err != nil {
		return handleErr(err)
	}
	return 0
}

func runResume(args []string) int {
	fs := flag.NewFlagSet("resume", flag.ContinueOnError)
	work := fs.String("work", "./tmp", "working directory")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	// Resume re-runs the same deterministic pipeline without an injected
	// interruption; the index dedup and checkpoint logic continue from the
	// last complete chunk and produce an identical manifest.
	eng := pipeline.NewEngine(pipeline.Config{
		WorkDir:   *work,
		Fault:     pipeline.FaultNone,
		ChunkSize: 4,
	})
	m, err := eng.Run()
	if m != nil {
		out, _ := m.JSON()
		fmt.Println(string(out))
	}
	if err != nil {
		return handleErr(err)
	}
	return 0
}

// handleErr maps interruption to a clean zero exit (the run is resumable)
// and any other failure to the required exit code 2.
func handleErr(err error) int {
	if err == pipeline.ErrInterrupted {
		fmt.Fprintf(os.Stderr, "interrupted: %v; run 'resume' to continue\n", err)
		return 0
	}
	fmt.Fprintf(os.Stderr, "run failed: %v\n", err)
	return 2
}

func validFault(f string) bool {
	switch f {
	case pipeline.FaultNone, pipeline.FaultTruncatedBox, pipeline.FaultBoundaryJump:
		return true
	}
	return false
}
