// Command archivecheck demonstrates auditable recovery of a local HLS/DASH
// archive. It is a terminal-only tool: every observable result is printed to
// stdout as stable JSON or key=value lines, and the process exit code carries
// the outcome.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"archivecheck/internal/fixture"
	"archivecheck/internal/model"
	"archivecheck/internal/pipeline"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 {
		usage()
		return pipeline.ExitInput
	}
	switch args[0] {
	case "demo":
		return runDemo(args[1:])
	case "check":
		return runCheck(args[1:])
	case "gen":
		return runGen(args[1:])
	case "help", "-h", "--help":
		usage()
		return pipeline.ExitOK
	default:
		fmt.Fprintf(os.Stderr, "archivecheck: unknown command %q\n", args[0])
		usage()
		return pipeline.ExitInput
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `archivecheck - auditable recovery for local HLS/DASH archives

usage:
  archivecheck demo --seed N [--fault NAME] [--root DIR] [--checkpoint FILE] [--resume]
                    [--manifest FILE] [--timebase TPS] [--concurrency N]
  archivecheck gen  --seed N [--fault NAME] [--root DIR]
  archivecheck check --root DIR [--manifest FILE] [--checkpoint FILE] [--resume]
                     [--timebase TPS]

exit codes:
  0  completed and verified
  2  input or format error
  3  damage located in media
  4  dependency blocked or checkpoint inconsistent
`)
}

// demoFlags are shared by demo and check.
type demoFlags struct {
	seed         int64
	fault        string
	root         string
	checkpoint   string
	resume       bool
	manifest     string
	timebase     int64
	concurrency  int
	window       uint64
	allowInexact bool
	jsonOut      bool
}

func bindDemo(fs *flag.FlagSet, d *demoFlags) {
	fs.Int64Var(&d.seed, "seed", 42, "fixture seed")
	fs.StringVar(&d.fault, "fault", "none", "fault to inject (see 'archivecheck gen -h')")
	fs.StringVar(&d.root, "root", "", "fixture/state directory (default: temp dir for demo)")
	fs.StringVar(&d.checkpoint, "checkpoint", "", "checkpoint file path")
	fs.BoolVar(&d.resume, "resume", false, "reuse a verified checkpoint")
	fs.StringVar(&d.manifest, "manifest", "", "manifest file inside the root")
	fs.Int64Var(&d.timebase, "timebase", 0, "override the shared timebase")
	fs.IntVar(&d.concurrency, "concurrency", 4, "DAG parallelism")
	fs.Uint64Var(&d.window, "window", 0, "transport reorder window in packets")
	fs.BoolVar(&d.allowInexact, "allow-inexact", false, "carry sub-tick durations instead of failing")
	fs.BoolVar(&d.jsonOut, "json", false, "emit the full report as JSON")
}

func runDemo(args []string) int {
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var d demoFlags
	bindDemo(fs, &d)
	if err := fs.Parse(args); err != nil {
		return pipeline.ExitInput
	}
	fault, err := fixture.ParseFault(d.fault)
	if err != nil {
		fmt.Fprintf(os.Stderr, "archivecheck: %v\n", err)
		return pipeline.ExitInput
	}
	root := d.root
	if root == "" {
		tmp, err := os.MkdirTemp("", "archivecheck-demo-")
		if err != nil {
			fmt.Fprintf(os.Stderr, "archivecheck: %v\n", err)
			return pipeline.ExitInput
		}
		defer os.RemoveAll(tmp)
		root = tmp
	}
	if _, err := fixture.Generate(fixture.Config{Seed: d.seed, Root: root, Fault: fault}); err != nil {
		fmt.Fprintf(os.Stderr, "archivecheck: generate fixture: %v\n", err)
		return pipeline.ExitInput
	}
	return execute(d, root)
}

func runCheck(args []string) int {
	fs := flag.NewFlagSet("check", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var d demoFlags
	bindDemo(fs, &d)
	if err := fs.Parse(args); err != nil {
		return pipeline.ExitInput
	}
	if d.root == "" {
		fmt.Fprintln(os.Stderr, "archivecheck: check requires --root")
		return pipeline.ExitInput
	}
	return execute(d, d.root)
}

func runGen(args []string) int {
	fs := flag.NewFlagSet("gen", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var seed int64
	var fault, root string
	fs.Int64Var(&seed, "seed", 42, "fixture seed")
	fs.StringVar(&fault, "fault", "none", "fault name")
	fs.StringVar(&root, "root", "", "output directory (required)")
	if err := fs.Parse(args); err != nil {
		return pipeline.ExitInput
	}
	if root == "" {
		fmt.Fprintln(os.Stderr, "archivecheck: gen requires --root")
		return pipeline.ExitInput
	}
	f, err := fixture.ParseFault(fault)
	if err != nil {
		fmt.Fprintf(os.Stderr, "archivecheck: %v\n", err)
		return pipeline.ExitInput
	}
	res, err := fixture.Generate(fixture.Config{Seed: seed, Root: root, Fault: f})
	if err != nil {
		fmt.Fprintf(os.Stderr, "archivecheck: %v\n", err)
		return pipeline.ExitInput
	}
	fmt.Printf("root=%s\n", res.Root)
	fmt.Printf("seed=%d\nfault=%s\n", res.Manifest.Seed, res.Manifest.Fault)
	fmt.Printf("manifest_segments=%d\n", res.Manifest.Segments*len(res.Manifest.Reps))
	fmt.Printf("representations=%d\n", len(res.Manifest.Reps))
	return pipeline.ExitOK
}

// execute runs the pipeline for an already generated fixture.
func execute(d demoFlags, root string) int {
	cfg := pipeline.Config{
		Root:           root,
		Seed:           d.seed,
		FaultName:      d.fault,
		ManifestFile:   d.manifest,
		CheckpointPath: d.checkpoint,
		Timebase:       d.timebase,
		AllowInexact:   d.allowInexact,
		Resume:         d.resume,
		Concurrency:    d.concurrency,
		ReorderWindow:  d.window,
	}
	rep, err := pipeline.Run(context.Background(), cfg)
	if err != nil {
		f, ok := model.AsFault(err)
		if ok {
			fmt.Fprintf(os.Stderr, "archivecheck: %s\n", f.Error())
		} else {
			fmt.Fprintf(os.Stderr, "archivecheck: %v\n", err)
		}
		return pipeline.ExitInput
	}
	if d.jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(rep)
	} else {
		printSummary(rep)
	}
	if rep.CheckpointMismatch {
		fmt.Fprintln(os.Stderr, "archivecheck: checkpoint is inconsistent; refusing to resume")
		for _, f := range rep.Faults {
			fmt.Fprintf(os.Stderr, "  %s\n", f.Error())
		}
	}
	if rep.ExitCode == pipeline.ExitInput {
		for _, f := range rep.Faults {
			fmt.Fprintf(os.Stderr, "archivecheck: %s\n", f.Error())
		}
	}
	return rep.ExitCode
}

// printSummary emits the stable key=value contract consumed by the archive
// audit tooling. Lines are always printed in the same order.
func printSummary(rep *pipeline.Report) {
	fmt.Printf("manifest_segments=%d\n", rep.ManifestSegments)
	fmt.Printf("representations=%d\n", rep.Representations)
	fmt.Printf("tracks=%d\n", rep.Tracks)
	if rep.KeyframeP50Ms >= 0 {
		fmt.Printf("keyframe_interval_ms_p50=%d\n", rep.KeyframeP50Ms)
	} else {
		fmt.Printf("keyframe_interval_ms_p50=unavailable\n")
	}
	fmt.Printf("damage_ranges=%d\n", len(rep.DamageRanges))
	for _, dr := range rep.DamageRanges {
		fmt.Printf("damage_range=%d-%d confidence=%s segments=%s\n",
			dr.Start, dr.End, dr.Confidence, strings.Join(dr.Segments, ","))
	}
	fmt.Printf("missing_packets=%d\n", rep.MissingPackets)
	fmt.Printf("duplicate_packets=%d\n", rep.DuplicatePackets)
	if rep.FirstBadSegment != "" {
		fmt.Printf("first_bad_segment=%s\n", rep.FirstBadSegment)
	}
	if rep.FirstBlocked != "" {
		fmt.Printf("first_blocked_dependency=%s\n", rep.FirstBlocked)
	}
	for _, r := range rep.Reps {
		fmt.Printf("representation=%s bandwidth=%d aligned=%t keyframe_interval_ms_p50=%d\n",
			r.ID, r.Bandwidth, r.Aligned, r.KeyframeP50Ms)
	}
	fmt.Printf("resumed_nodes=%d\n", rep.ResumedNodes)
	fmt.Printf("resume_ms=%d\n", rep.ResumeMS)
	fmt.Printf("output_sha256=%s\n", rep.OutputSHA256)
	fmt.Printf("exit_code=%d\n", rep.ExitCode)
}
