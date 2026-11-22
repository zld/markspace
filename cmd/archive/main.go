// Command archive is the terminal entry point of the media archive
// recovery pipeline. Exit codes: 0 success, 1 input/processing
// failure, 2 argument error.
package main

import (
	"flag"
	"fmt"
	"os"

	"archive/internal/fixtures"
	"archive/internal/model"
	"archive/internal/parse"
	"archive/internal/pipeline"
	"archive/internal/timeline"
)

func main() { os.Exit(run(os.Args[1:])) }

func run(args []string) int {
	if len(args) < 1 {
		usage()
		return 2
	}
	switch args[0] {
	case "demo":
		return cmdDemo(args[1:])
	case "verify":
		return cmdVerify(args[1:])
	default:
		usage()
		return 2
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: archive <command> [flags]")
	fmt.Fprintln(os.Stderr, "  demo   --out <dir> [--seed N]   generate samples, run pipeline, print metrics")
	fmt.Fprintln(os.Stderr, "  verify --dir <dir>              independently verify a previous output")
}

func cmdDemo(args []string) int {
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	out := fs.String("out", "", "output directory")
	seed := fs.Int64("seed", 17, "deterministic sample seed")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *out == "" {
		fmt.Fprintln(os.Stderr, "demo: --out is required")
		return 2
	}
	if err := os.MkdirAll(*out, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "demo:", err)
		return 1
	}
	_, corrupt, err := fixtures.Generate(*out, *seed)
	if err != nil {
		fmt.Fprintln(os.Stderr, "demo: generate fixtures:", err)
		return 1
	}
	evalCorruptSamples(corrupt)

	pol := timeline.DefaultPolicy()
	// Phase 1: run parse+compose and checkpoint, simulating an
	// interruption before the render stage.
	if _, err := pipeline.Run(pipeline.Config{Out: *out, Policy: pol, StopAfter: "compose"}); err != nil {
		fmt.Fprintln(os.Stderr, "demo: phase 1:", err)
		return 1
	}
	// Phase 2: resume — valid checkpoints are reused, render re-executes.
	res, err := pipeline.Run(pipeline.Config{Out: *out, Policy: pol})
	if err != nil {
		fmt.Fprintln(os.Stderr, "demo: phase 2:", err)
		return 1
	}
	rep, err := pipeline.Verify(*out)
	if err != nil {
		fmt.Fprintln(os.Stderr, "demo: verify:", err)
		return 1
	}
	printMetrics(rep, res.ReusedStages)
	if !rep.OK() {
		for _, p := range rep.Problems {
			fmt.Fprintln(os.Stderr, "demo: problem:", p)
		}
		return 1
	}
	return 0
}

func cmdVerify(args []string) int {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	dir := fs.String("dir", "", "output directory to verify")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *dir == "" {
		fmt.Fprintln(os.Stderr, "verify: --dir is required")
		return 2
	}
	rep, err := pipeline.Verify(*dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "verify:", err)
		return 1
	}
	printMetrics(rep, -1)
	if !rep.OK() {
		for _, p := range rep.Problems {
			fmt.Fprintln(os.Stderr, "verify: problem:", p)
		}
		return 1
	}
	return 0
}

func printMetrics(rep *pipeline.VerifyReport, reusedStages int) {
	rate := func(num, den int) float64 {
		if den == 0 {
			return 0
		}
		return float64(num) / float64(den)
	}
	fmt.Printf("roundtrip_rate=%.3f\n", rate(rep.RoundtripOK, rep.Segments))
	fmt.Printf("keyframe_hit_rate=%.3f\n", rate(rep.KeyframeHits, rep.Frames))
	fmt.Printf("segments=%d\n", rep.Segments)
	fmt.Printf("hash_mismatches=%d\n", rep.HashMismatches)
	fmt.Printf("frames=%d\n", rep.Frames)
	if reusedStages >= 0 {
		fmt.Printf("resume_reused_stages=%d\n", reusedStages)
	}
}

// evalCorruptSamples runs each fault-injection sample through the
// pipeline stage that must catch it and prints the located fault.
func evalCorruptSamples(corrupt map[string]string) {
	for _, class := range fixtures.CorruptClasses {
		p := corrupt[class]
		switch class {
		case "truncated_stbl", "oob_offset", "bad_crop":
			_, err := parse.ParseMP4File(p)
			if f, ok := model.AsFault(err); ok {
				fmt.Printf("sample=%s stage=%s error_offset=%d\n", class, f.Stage, f.Offset)
			} else {
				fmt.Printf("sample=%s unexpected_err=%v\n", class, err)
			}
		case "conflict_color":
			tr, err := parse.ParseMXFFile(p)
			if err != nil {
				fmt.Printf("sample=%s unexpected_err=%v\n", class, err)
				continue
			}
			aligned, err := timeline.AlignTrack(0, tr)
			if err != nil {
				fmt.Printf("sample=%s unexpected_err=%v\n", class, err)
				continue
			}
			segs, err := timeline.Compose([]*parse.Track{tr}, [][]timeline.AlignedSample{aligned}, timeline.DefaultPolicy())
			if err != nil {
				fmt.Printf("sample=%s unexpected_err=%v\n", class, err)
				continue
			}
			c := segs[0].Transform.Color
			fmt.Printf("sample=%s stage=compose conflict=%v chosen_primaries=%d policy=%s originals=%d\n",
				class, c.Conflict, c.Chosen.Primaries, c.PolicyVersion, len(c.Original))
		case "overflow_ts":
			tr, err := parse.ParseMXFFile(p)
			if err != nil {
				fmt.Printf("sample=%s unexpected_err=%v\n", class, err)
				continue
			}
			_, err = timeline.AlignTrack(6, tr)
			if f, ok := model.AsFault(err); ok {
				t := ""
				if f.Time != nil {
					t = f.Time.String()
				}
				fmt.Printf("sample=%s stage=%s track=%d time=%s\n", class, f.Stage, f.Track, t)
			} else {
				fmt.Printf("sample=%s unexpected_err=%v\n", class, err)
			}
		}
	}
}
