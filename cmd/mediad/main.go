// Command mediad generates deterministic media fixtures and runs the
// traceable, resumable processing pipeline from the terminal.
//
// Usage:
//
//	mediad fixture --out <dir>
//	mediad process --input <fixture-dir> --state <state-dir> [--json]
//
// Exit codes: 0 success (partial item failures still print a full report),
// 2 usage or fatal input error, 3 one or more cases failed processing.
package main

import (
	"flag"
	"fmt"
	"os"

	"mediad/internal/fixture"
	"mediad/internal/pipeline"
)

const (
	exitOK       = 0
	exitUsage    = 2
	exitFailed   = 3
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(exitUsage)
	}
	var err error
	switch os.Args[1] {
	case "fixture":
		err = runFixture(os.Args[2:])
	case "process":
		err = runProcess(os.Args[2:])
	case "-h", "--help", "help":
		usage()
		os.Exit(exitOK)
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand %q\n", os.Args[1])
		usage()
		os.Exit(exitUsage)
	}
	if err != nil {
		if pe, ok := err.(*procError); ok {
			fmt.Fprintln(os.Stderr, pe.Error())
			os.Exit(pe.code)
		}
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(exitUsage)
	}
}

type procError struct {
	code int
	msg  string
}

func (e *procError) Error() string { return e.msg }

func usage() {
	fmt.Fprint(os.Stderr, `mediad - deterministic media processing kernel

  fixture --out DIR
        Generate the full fixture tree (DASH/HLS, damaged variants, RTP).

  process --input DIR --state DIR [--json]
        Process every fixture case, resume committed work, print the report.

Exit codes: 0 ok, 2 usage/fatal input, 3 one or more cases failed.
`)
}

func runFixture(args []string) error {
	fs := flag.NewFlagSet("fixture", flag.ContinueOnError)
	out := fs.String("out", "", "output directory for generated fixtures")
	if err := fs.Parse(args); err != nil {
		return &procError{exitUsage, err.Error()}
	}
	if *out == "" {
		return &procError{exitUsage, "fixture requires --out"}
	}
	idx, err := fixture.Generate(*out)
	if err != nil {
		return &procError{exitUsage, "fixture generation failed: " + err.Error()}
	}
	fmt.Printf("generated %d cases under %s\n", len(idx.Cases), *out)
	for _, c := range idx.Cases {
		fmt.Printf("  - %s (%s, expect_ok=%v)\n", c.ID, c.Kind, c.ExpectOK)
	}
	return nil
}

func runProcess(args []string) error {
	fs := flag.NewFlagSet("process", flag.ContinueOnError)
	input := fs.String("input", "", "fixture directory containing cases.json")
	state := fs.String("state", "", "state directory for checkpoints")
	asJSON := fs.Bool("json", false, "emit the full report as JSON")
	if err := fs.Parse(args); err != nil {
		return &procError{exitUsage, err.Error()}
	}
	if *input == "" || *state == "" {
		return &procError{exitUsage, "process requires --input and --state"}
	}
	report, err := pipeline.Run(*input, *state, pipeline.DefaultParameters())
	if err != nil {
		return &procError{exitUsage, "cannot start processing: " + err.Error()}
	}
	if *asJSON {
		raw, mErr := pipeline.MarshalReport(report)
		if mErr != nil {
			return &procError{exitUsage, mErr.Error()}
		}
		fmt.Println(string(raw))
	} else {
		printHuman(report)
	}
	if len(report.Errors) > 0 {
		return &procError{exitFailed,
			fmt.Sprintf("%d case error(s) recorded; partial results written", len(report.Errors))}
	}
	return nil
}

func printHuman(r *pipeline.Report) {
	fmt.Printf("cases=%d resumed=%d errors=%d\n", len(r.Cases), r.ResumedCount, len(r.Errors))
	fmt.Printf("roundtrip_consistency=%.4f keyframe_hit_rate=%.4f\n",
		r.RoundtripConsistency, r.KeyframeHitRate)
	fmt.Printf("silence_count=%d deduplicated_count=%d unique_hashes=%d\n",
		r.SilenceCount, r.DeduplicatedCount, len(r.SegmentHashes))
	for _, c := range r.Cases {
		status := "ok"
		if !c.OK {
			status = "FAILED"
		}
		fmt.Printf("  [%s] %s rt=%.3f kf=%.3f hashes=%d silence=%d dedup=%d errs=%d\n",
			status, c.ID, c.RoundtripConsistency, c.KeyframeHitRate,
			len(c.SegmentHashes), c.SilenceCount, c.DeduplicatedCount, len(c.Errors))
	}
}
