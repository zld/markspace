package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"tracecheck/internal/clock"
	"tracecheck/internal/fixture"
	"tracecheck/internal/pipeline"
	"tracecheck/internal/rtptrace"
	"tracecheck/internal/segment"
)

// demoCase is one generated fixture and its expected acceptance behaviour.
type demoCase struct {
	name       string
	kind       string
	build      func() ([]byte, *fixture.BuiltFile)
	wantOK     bool
	wantStages int
	wantCategory clock.Category
}

func twoTrackSpec() fixture.MediaSpec {
	return fixture.MediaSpec{
		Brand: "isom",
		Tracks: []fixture.TrackSpec{
			{TrackID: 1, Timescale: 48000, SampleCount: 16, SampleDelta: 480,
				PayloadSize: 12, SyncInterval: 4, HandlerType: "soun"},
			{TrackID: 2, Timescale: 24000, SampleCount: 16, SampleDelta: 240,
				PayloadSize: 16, SyncInterval: 4, HandlerType: "vide"},
		},
	}
}

func demoCases() []demoCase {
	return []demoCase{
		{
			name: "normal", kind: "intact index, two representations", wantOK: true, wantStages: 6,
			build: func() ([]byte, *fixture.BuiltFile) {
				f := fixture.BuildFile(twoTrackSpec())
				return f.Data, f
			},
		},
		{
			name: "truncated", kind: "mdat box truncated past EOF", wantOK: false, wantStages: 2,
			wantCategory: clock.CategoryTruncated,
			build: func() ([]byte, *fixture.BuiltFile) {
				f := fixture.BuildFile(twoTrackSpec())
				cut := f.TrackMdat[2][12].Offset
				return f.Truncate(cut), f
			},
		},
		{
			name: "corrupt_sample_table", kind: "corrupt sample, resume at next sync", wantOK: true, wantStages: 6,
			build: func() ([]byte, *fixture.BuiltFile) {
				f := fixture.BuildFile(twoTrackSpec())
				// Sample 5 is non-sync; the next verifiable sync is sample 8.
				return f.CorruptSample(1, 5), f
			},
		},
		{
			name: "unrecoverable", kind: "corrupt final sample, no later sync", wantOK: false, wantStages: 3,
			wantCategory: clock.CategoryNoSync,
			build: func() ([]byte, *fixture.BuiltFile) {
				spec := twoTrackSpec()
				spec.Tracks[0].SyncInterval = 0
				spec.Tracks[1].SyncInterval = 0
				f := fixture.BuildFile(spec)
				return f.CorruptSample(1, 15), f
			},
		},
	}
}

func runDemo(args []string) int {
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	out := fs.String("out", "", "output directory")
	if err := fs.Parse(args); err != nil {
		return exitArgs
	}
	if *out == "" {
		fmt.Fprintln(os.Stderr, "demo requires --out <dir>")
		return exitArgs
	}
	if err := os.MkdirAll(*out, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "cannot create output dir: %v\n", err)
		return exitArgs
	}

	plan := rtptrace.Plan{
		Seed: 13, LossRate: 0.2,
		DuplicateAt: []int{2}, ReorderAt: []int{4},
	}
	allMatched := true
	for _, tc := range demoCases() {
		data, _ := tc.build()
		dir := filepath.Join(*out, tc.name)
		inputPath := filepath.Join(dir, "input.mp4")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			fmt.Fprintf(os.Stderr, "mkdir %s: %v\n", dir, err)
			return exitArgs
		}
		if err := os.WriteFile(inputPath, data, 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "write fixture: %v\n", err)
			return exitArgs
		}

		fmt.Printf("\n=== case %s (%s) ===\n", tc.name, tc.kind)
		first, err1 := pipeline.Run(inputPath, dir, plan)
		if first != nil {
			printReport(first)
		}
		diff := -1
		if err1 == nil {
			second, err2 := pipeline.Run(inputPath, dir, plan)
			if err2 != nil {
				fmt.Fprintf(os.Stderr, "rerun failed: %v\n", err2)
			} else {
				diff = segment.DiffCount(first.Manifest, second.Manifest)
				fmt.Printf("rerun manifest diff: %d (want 0)\n", diff)
			}
		}

		ok := evaluate(tc, err1, first, diff)
		allMatched = allMatched && ok
		fmt.Printf("expectation: %s\n", map[bool]string{true: "MATCH", false: "MISMATCH"}[ok])
	}

	fmt.Println()
	if allMatched {
		fmt.Println("DEMO RESULT: all cases behaved as specified")
		return exitOK
	}
	fmt.Println("DEMO RESULT: at least one case diverged from specification")
	return exitUnrecoverable
}

// evaluate checks the observed outcome against the case's contract.
func evaluate(tc demoCase, err error, rep *pipeline.Report, diff int) bool {
	if rep == nil {
		return false
	}
	succeeded := err == nil
	if succeeded != tc.wantOK {
		fmt.Printf("  success=%v want %v\n", succeeded, tc.wantOK)
		return false
	}
	if !tc.wantOK {
		if rep.StageFailure == nil {
			fmt.Println("  failure recorded without a stage failure")
			return false
		}
		if rep.Transitions != tc.wantStages {
			fmt.Printf("  transitions=%d want %d\n", rep.Transitions, tc.wantStages)
			return false
		}
		if rep.StageFailure.Category != tc.wantCategory {
			fmt.Printf("  category=%s want %s\n", rep.StageFailure.Category, tc.wantCategory)
			return false
		}
		fmt.Printf("  failed stage=%s offset=%d category=%s\n",
			rep.StageFailure.Stage, rep.StageFailure.Offset, rep.StageFailure.Category)
		return true
	}
	if rep.Transitions != tc.wantStages {
		fmt.Printf("  transitions=%d want %d\n", rep.Transitions, tc.wantStages)
		return false
	}
	if diff != 0 || !rep.Verification.OK() || rep.MaxBoundaryDelta > 1 {
		fmt.Printf("  diff=%d verify=%v maxDelta=%d\n",
			diff, rep.Verification.OK(), rep.MaxBoundaryDelta)
		return false
	}
	return true
}

func printReport(rep *pipeline.Report) {
	fmt.Printf("input digest: %s\n", rep.InputDigest)
	fmt.Printf("stage transitions: %d\n", rep.Transitions)
	for _, ev := range rep.RecoveryEvents {
		fmt.Printf("recovery: %s at byte %d: %s\n", ev.Category, ev.Offset, ev.Detail)
	}
	fmt.Printf("rtp lost=%d duplicated=%d reordered=%d delivered=%d\n",
		rep.RTPStats.Lost, rep.RTPStats.Duplicated,
		rep.RTPStats.Reordered, rep.RTPStats.Delivered)
	if rep.Manifest != nil {
		fmt.Printf("max representation boundary delta: %d tick(s)\n", rep.MaxBoundaryDelta)
		fmt.Printf("manifest segments: %d digest: %s\n",
			len(rep.Manifest.Segments), rep.Manifest.SegmentDigest)
	}
}
