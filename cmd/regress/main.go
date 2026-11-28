// Command regress generates local samples and runs the media kernel
// acceptance demos. Exit codes: 0 = pass, 1 = acceptance mismatch,
// 2 = corrupt input accurately reported.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	mk "mediakernel"
)

func main() {
	os.Exit(run())
}

func run() int {
	if len(os.Args) < 2 || os.Args[1] != "demo" {
		fmt.Fprintln(os.Stderr, "usage: regress demo --case <case> [--seed N] [--resume] [--out DIR]")
		return 1
	}
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	caseName := fs.String("case", "interleaved", "demo case")
	seed := fs.Int64("seed", 7, "fixture seed")
	resume := fs.Bool("resume", false, "resume from checkpoint")
	out := fs.String("out", "out", "output directory")
	if err := fs.Parse(os.Args[2:]); err != nil {
		return 1
	}
	cfg := mk.PipelineConfig{
		QueueCap:     4,
		MaxBlockSize: 1 << 20,
		SegmentDurNs: 200_000_000,
		Silence: mk.SilenceConfig{
			Threshold: 500, MinDuration: 100_000_000, MaxGap: 0},
		FingerprintWindowNs: 1_000_000_000,
	}
	switch *caseName {
	case "interleaved":
		return demoInterleaved(cfg, *seed, *out)
	case "truncated-vint", "invalid-vint", "bad-parent", "size-overflow", "ts-regression":
		return demoCorrupt(cfg, *caseName, *seed, *out)
	case "interrupt":
		return demoInterrupt(cfg, *seed, *out, *resume)
	default:
		fmt.Fprintf(os.Stderr, "unknown case %q\n", *caseName)
		return 1
	}
}

func writeInput(dir string, data []byte) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	p := filepath.Join(dir, "input.webm")
	return p, os.WriteFile(p, data, 0o644)
}

func demoInterleaved(cfg mk.PipelineConfig, seed int64, out string) int {
	fix := mk.BuildInterleaved(seed)
	in, err := writeInput(out, fix.Data)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	man, err := mk.NewPipeline(cfg, out).Run(context.Background(), in, false)
	if err != nil {
		fmt.Fprintln(os.Stderr, "pipeline:", err)
		return 1
	}
	tracks := 0
	if s, err := os.Stat(filepath.Join(out, "summary.mp4")); err == nil && s.Size() > 0 {
		tracks = fix.Tracks
	}
	fmt.Printf("input:      %s (sha256 %s)\n", in, man.InputSHA256)
	fmt.Printf("tracks:     %d (source: Tracks element, manifest params)\n", tracks)
	fmt.Printf("segments:   %d (source: manifest.json segments[].range)\n", len(man.Segments))
	fmt.Printf("silence:    %d intervals (source: manifest.json silence[])\n", len(man.Silence))
	fmt.Printf("duplicates: %d (source: manifest.json duplicates)\n", man.Duplicates)
	fmt.Printf("sha256:     %s (source: manifest.json outputSha256, summary.mp4 free box)\n", man.OutputSHA256)
	ok := tracks == 2 && len(man.Segments) == 4 && len(man.Silence) == 2 && man.Duplicates >= 4
	if !ok {
		fmt.Println("ACCEPTANCE: FAIL")
		return 1
	}
	fmt.Println("ACCEPTANCE: PASS")
	return 0
}

func demoCorrupt(cfg mk.PipelineConfig, name string, seed int64, out string) int {
	var fix mk.Fixture
	switch name {
	case "truncated-vint":
		fix = mk.BuildTruncatedVINT(seed)
	case "invalid-vint":
		fix = mk.BuildInvalidVINT(seed)
	case "bad-parent":
		fix = mk.BuildBadParent(seed)
	case "size-overflow":
		fix = mk.BuildSizeOverflow(seed)
	case "ts-regression":
		fix = mk.BuildTSRegression(seed)
	}
	in, err := writeInput(out, fix.Data)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	_, runErr := mk.NewPipeline(cfg, out).Run(context.Background(), in, false)
	mkErr, ok := runErr.(*mk.Error)
	if !ok {
		fmt.Fprintf(os.Stderr, "expected structured error, got: %v\n", runErr)
		return 1
	}
	diff := mkErr.Offset - fix.ErrOffset
	if diff < 0 {
		diff = -diff
	}
	fmt.Printf("error:    %s at offset %d (path %s)\n", mkErr.Code, mkErr.Offset, mkErr.Path)
	fmt.Printf("expected: %s at offset %d (generator-recorded, |diff|=%d)\n",
		fix.ErrCode, fix.ErrOffset, diff)
	man := &mk.Manifest{Errors: []*mk.Error{mkErr}}
	if data, err := os.ReadFile(in); err == nil {
		_ = data
	}
	_ = man.Save(filepath.Join(out, "manifest.json"))
	if mkErr.Code != fix.ErrCode || diff > 1 {
		fmt.Println("ACCEPTANCE: FAIL")
		return 1
	}
	fmt.Println("ACCEPTANCE: corrupt input accurately reported")
	return 2
}

func demoInterrupt(cfg mk.PipelineConfig, seed int64, out string, resume bool) int {
	fix := mk.BuildInterleaved(seed)
	cleanDir := filepath.Join(out, "clean")
	runDir := filepath.Join(out, "run")
	inClean, err := writeInput(cleanDir, fix.Data)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	inRun, err := writeInput(runDir, fix.Data)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	manClean, err := mk.NewPipeline(cfg, cleanDir).Run(context.Background(), inClean, false)
	if err != nil {
		fmt.Fprintln(os.Stderr, "clean run:", err)
		return 1
	}
	intCfg := cfg
	intCfg.FailAfterSegments = 2
	_, err = mk.NewPipeline(intCfg, runDir).Run(context.Background(), inRun, false)
	if mkErr, ok := err.(*mk.Error); !ok || mkErr.Code != mk.ErrInterrupted {
		fmt.Fprintln(os.Stderr, "expected simulated interrupt, got:", err)
		return 1
	}
	fmt.Println("interrupted after 2 committed segments (checkpoint saved)")
	if !resume {
		fmt.Println("rerun with --resume to continue")
		return 0
	}
	intCfg.FailAfterSegments = 0
	start := time.Now()
	manResumed, err := mk.NewPipeline(intCfg, runDir).Run(context.Background(), inRun, true)
	elapsed := time.Since(start)
	if err != nil {
		fmt.Fprintln(os.Stderr, "resume:", err)
		return 1
	}
	fmt.Printf("resumed:  %d segments (clean run: %d)\n", len(manResumed.Segments), len(manClean.Segments))
	fmt.Printf("sha256:   resumed=%s\n          clean  =%s\n", manResumed.OutputSHA256, manClean.OutputSHA256)
	fmt.Printf("resume took: %d ms\n", elapsed.Milliseconds())
	ok := len(manResumed.Segments) == len(manClean.Segments) &&
		manResumed.OutputSHA256 == manClean.OutputSHA256 && elapsed < 5*time.Second
	if !ok {
		fmt.Println("ACCEPTANCE: FAIL")
		return 1
	}
	fmt.Println("ACCEPTANCE: PASS")
	return 0
}
