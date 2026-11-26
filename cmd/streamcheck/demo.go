package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"streamcheck/core"
	"streamcheck/fixture"
	"streamcheck/pipeline"
)

// demoCmd generates fixtures and runs the pipeline over them.
func demoCmd(args []string, w io.Writer) int {
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	fs.SetOutput(w)
	seed := fs.Int64("seed", 17, "fixture seed")
	faultSet := fs.String("fault-set", "all", "fault set to inject")
	work := fs.String("work", "", "work directory (required)")
	stopAfter := fs.String("stop-after", "", "checkpoint and exit 75 after this stage")
	if err := fs.Parse(args); err != nil {
		return core.ExitUsage
	}
	if *work == "" {
		fmt.Fprintln(w, "demo: --work is required")
		return core.ExitUsage
	}
	plan, err := fixture.Generate(*work, *seed, *faultSet)
	if err != nil {
		fmt.Fprintln(w, "error:", err)
		return core.ExitCode(err)
	}
	eng := buildEngine(*work, plan)
	if *stopAfter != "" && eng.StageIndex(*stopAfter) < 0 {
		fmt.Fprintln(w, "demo: unknown stage", *stopAfter)
		return core.ExitUsage
	}
	digest, err := eng.InputDigest()
	if err != nil {
		fmt.Fprintln(w, "error:", err)
		return core.ExitCode(err)
	}
	arts, err := eng.Execute(0, nil, digest, *stopAfter)
	printReport(w, plan, arts)
	if errors.Is(err, core.ErrStop) {
		fmt.Fprintf(w, "checkpoint: saved after stage %q\n", *stopAfter)
		return core.ExitCheckpoint
	}
	if err != nil {
		fmt.Fprintln(w, "error:", err)
		return core.ExitCode(err)
	}
	return finish(w, plan, arts)
}

// resumeCmd continues from the last valid checkpoint in the work dir.
func resumeCmd(args []string, w io.Writer) int {
	fs := flag.NewFlagSet("resume", flag.ContinueOnError)
	fs.SetOutput(w)
	work := fs.String("work", "", "work directory (required)")
	if err := fs.Parse(args); err != nil {
		return core.ExitUsage
	}
	if *work == "" {
		fmt.Fprintln(w, "resume: --work is required")
		return core.ExitUsage
	}
	plan, err := fixture.LoadPlan(*work)
	if err != nil {
		fmt.Fprintln(w, "error:", err)
		return core.ExitCode(err)
	}
	if _, err := os.Stat(filepath.Join(*work, pipeline.CheckpointFile)); err != nil {
		fmt.Fprintln(w, "error: no checkpoint in", *work)
		return core.ExitUsage
	}
	cp, err := pipeline.LoadCheckpoint(*work)
	if err != nil {
		fmt.Fprintln(w, "error:", err)
		return core.ExitCode(err)
	}
	eng := buildEngine(*work, plan)
	start, err := eng.ResumePoint(cp)
	if err != nil {
		fmt.Fprintln(w, "error:", err)
		return core.ExitCode(err)
	}
	t0 := time.Now()
	arts, err := eng.Execute(start, cp.Artifacts, cp.InputDigest, "")
	elapsed := time.Since(t0)
	if err != nil {
		fmt.Fprintln(w, "error:", err)
		return core.ExitCode(err)
	}
	printReport(w, plan, arts)
	fmt.Fprintf(w, "resume: elapsed=%dms limit=5000ms\n", elapsed.Milliseconds())
	if elapsed > 5*time.Second {
		fmt.Fprintln(w, "MISMATCH: resume exceeded 5000ms")
		return core.ExitMismatch
	}
	return finish(w, plan, arts)
}

// finish verifies results against the plan and sets the exit code.
func finish(w io.Writer, plan *fixture.Plan, arts map[string]string) int {
	if bad := verify(plan, arts); len(bad) > 0 {
		for _, b := range bad {
			fmt.Fprintln(w, "MISMATCH:", b)
		}
		fmt.Fprintln(w, "result: FAIL")
		return core.ExitMismatch
	}
	fmt.Fprintln(w, "result: OK")
	return core.ExitOK
}
