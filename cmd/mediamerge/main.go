// mediamerge is the terminal adapter for the local media-merge kernel.
//
// Usage:
//
//	mediamerge verify --case <name> [--policy strict|mark-gaps]
//	                 [--interrupt-after N] --out <dir>
//	mediamerge resume --out <dir>
//
// Exit codes: 0 completed (or damage recorded under policy), 1 local
// read/write failure, 2 input rejected by the rules.
package main

import (
	"fmt"
	"os"
	"path/filepath"

	"mediamerge/internal/fault"
	"mediamerge/internal/pipeline"
	"mediamerge/internal/report"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 {
		usage()
		return 2
	}
	switch args[0] {
	case "verify":
		return runVerify(args[1:])
	case "resume":
		return runResume(args[1:])
	case "-h", "--help", "help":
		usage()
		return 0
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand %q\n", args[0])
		usage()
		return 2
	}
}

type flags struct {
	caseName       string
	policy         string
	out            string
	interruptAfter int
}

func parseFlags(args []string) (flags, error) {
	f := flags{policy: "mark-gaps"}
	for i := 0; i < len(args); i++ {
		a := args[i]
		next := func() (string, error) {
			if i+1 >= len(args) {
				return "", fmt.Errorf("flag %s requires a value", a)
			}
			i++
			return args[i], nil
		}
		switch a {
		case "--case":
			v, err := next()
			if err != nil {
				return f, err
			}
			f.caseName = v
		case "--policy":
			v, err := next()
			if err != nil {
				return f, err
			}
			f.policy = v
		case "--out":
			v, err := next()
			if err != nil {
				return f, err
			}
			f.out = v
		case "--interrupt-after":
			v, err := next()
			if err != nil {
				return f, err
			}
			n, err := parseInt(v)
			if err != nil {
				return f, fmt.Errorf("--interrupt-after: %w", err)
			}
			f.interruptAfter = n
		default:
			return f, fmt.Errorf("unknown flag %q", a)
		}
	}
	if f.out == "" {
		return f, fmt.Errorf("--out is required")
	}
	if f.policy != pipeline.PolicyStrict && f.policy != pipeline.PolicyMarkGaps {
		return f, fmt.Errorf("policy must be strict or mark-gaps, got %q", f.policy)
	}
	return f, nil
}

func runVerify(args []string) int {
	f, err := parseFlags(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "verify: %v\n", err)
		return 2
	}
	if f.caseName == "" {
		fmt.Fprintln(os.Stderr, "verify: --case is required")
		return 2
	}
	m, err := pipeline.Run(pipeline.Config{
		Case: f.caseName, OutDir: f.out, Policy: f.policy,
		InterruptAfter: f.interruptAfter,
	})
	if m != nil {
		emit(f.out, m)
	}
	if err != nil {
		return exitFor(err)
	}
	return 0
}

func runResume(args []string) int {
	f, err := parseFlags(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "resume: %v\n", err)
		return 2
	}
	m, err := pipeline.Resume(f.out)
	if m != nil {
		emit(f.out, m)
	}
	if err != nil {
		return exitFor(err)
	}
	return 0
}

// exitFor maps kernel errors onto the documented terminal exit codes.
func exitFor(err error) int {
	if err == pipeline.ErrInterrupted {
		return 0 // an interrupted run is a valid state awaiting resume
	}
	fe := fault.As(err)
	if fe == nil {
		return 1
	}
	switch fe.Kind {
	case fault.KindIO, fault.KindChecksum, fault.KindState:
		return 1
	default:
		return 2 // rule rejection: encoding, timecode, conflict, bounds, ...
	}
}

func emit(outDir string, m *report.Metrics) {
	m.WriteText(os.Stdout)
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "report: %v\n", err)
		return
	}
	path := filepath.Join(outDir, "report.json")
	f, err := os.Create(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "report: %v\n", err)
		return
	}
	defer f.Close()
	if err := m.WriteJSON(f); err != nil {
		fmt.Fprintf(os.Stderr, "report: %v\n", err)
	}
}

func parseInt(s string) (int, error) {
	n := 0
	if s == "" {
		return 0, fmt.Errorf("empty integer")
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("not a non-negative integer: %q", s)
		}
		n = n*10 + int(c-'0')
	}
	return n, nil
}

func usage() {
	fmt.Fprintln(os.Stderr, `mediamerge - local media merge kernel

  verify --case <normal|damaged|conflict|recovery>
         [--policy strict|mark-gaps] [--interrupt-after N] --out <dir>
  resume --out <dir>

exit codes: 0 ok/damage-recorded, 1 io failure, 2 rule rejection`)
}
