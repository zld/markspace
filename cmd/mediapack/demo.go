package main

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"mediapack/internal/fixture"
	"mediapack/internal/index"
	"mediapack/internal/ladder"
	"mediapack/internal/model"
	"mediapack/internal/probe"
	"mediapack/internal/runner"
	"mediapack/internal/timeline"
)

// injectAndProbeFaults synthesizes the fault samples and verifies the probe
// localizes each to the exact injected byte. It returns a non-zero exit code
// only on a harness defect; the fault cases themselves always localize.
func injectAndProbeFaults(args demoArgs, st *demoState) int {
	if args.faults != "all" {
		return exitOK
	}
	faultDir := filepath.Join(args.work, "faults")
	specs := fixture.DefaultSpecs(min2(args.tracks, 3))
	bundles, err := fixture.Generate(faultDir, specs)
	if err != nil {
		fmt.Fprintf(os.Stderr, "fault fixture generation failed: %v\n", err)
		return exitDataFault
	}
	cases := []struct {
		name string
		run  func(src, dst string) (*fixture.Fault, error)
		src  int
		ext  string
	}{
		{"truncated_box", fixture.InjectTruncatedBox, 0, ".mp4"},
		{"illegal_length", fixture.InjectIllegalLength, 0, ".mp4"},
		{"truncated_mxf", fixture.InjectTruncatedMXF, 1, ".mxf"},
	}
	for i, c := range cases {
		if c.src >= len(bundles) {
			continue
		}
		dst := filepath.Join(faultDir, c.name+c.ext)
		f, ferr := c.run(bundles[c.src].Container, dst)
		if ferr != nil {
			fmt.Fprintf(os.Stderr, "fault injection %s failed: %v\n", c.name, ferr)
			return exitDataFault
		}
		_, serr := probe.Scan(dst, probe.DefaultLimits())
		pe, ok := model.AsPipeline(serr)
		if !ok {
			fmt.Fprintf(os.Stderr, "fault %s was not detected\n", c.name)
			return exitDataFault
		}
		diff := pe.ByteRange.Offset - f.Offset
		if diff < 0 {
			diff = -diff
		}
		if diff > 16 {
			fmt.Fprintf(os.Stderr, "fault %s localization too coarse: reported=%d injected=%d\n",
				c.name, pe.ByteRange.Offset, f.Offset)
			return exitDataFault
		}
		// Record the headline metrics for the truncated-box case.
		if i == 0 {
			st.faultOffset = f.Offset
			st.faultErrorBytes = diff
		}
	}
	return exitOK
}

// demoState carries values assembled across stages within a single run, so no
// package-level mutable state leaks between invocations (or tests).
type demoState struct {
	faultOffset     int64
	faultErrorBytes int64
	alignedLadders  int
	resumeMS        int64
}

func newDemoState() *demoState {
	return &demoState{faultOffset: -1}
}

func min2(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// assembleAndExecute runs the full happy-path pipeline twice to prove
// determinism, exercising resume on the second run.
func assembleAndExecute(args demoArgs, st *demoState) (*runner.Report, int) {
	work := filepath.Join(args.work, "run")
	store, err := index.NewStore(filepath.Join(work, "index"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "index store: %v\n", err)
		return nil, exitDataFault
	}
	bundles, err := fixture.Generate(work, fixture.DefaultSpecs(args.tracks))
	if err != nil {
		fmt.Fprintf(os.Stderr, "fixture: %v\n", err)
		return nil, exitDataFault
	}

	// Stage 1+2: scan -> index, building clock tables from RTP sidecars.
	tables := make([]*timeline.ClockTable, 0, len(bundles))
	inputs := make([]runner.InputRef, 0, len(bundles))
	nominal := map[uint32]int64{}
	var scanErrs model.Errors
	for _, b := range bundles {
		entry, ierr := store.GetOrScan(b.Container, probe.DefaultLimits())
		if ierr != nil {
			fmt.Fprintf(os.Stderr, "scan/index failed: %v\n", ierr)
			return nil, exitDataFault
		}
		inputs = append(inputs, runner.InputRef{Path: b.Container, SHA256: entry.Summary.SHA256})
		raw, rerr := os.ReadFile(b.RTPSidecar)
		if rerr != nil {
			fmt.Fprintf(os.Stderr, "rtp sidecar: %v\n", rerr)
			return nil, exitDataFault
		}
		pkts, _, perr := timeline.ParseRTPPacketStream(raw)
		if perr != nil {
			fmt.Fprintf(os.Stderr, "rtp parse: %v\n", perr)
			return nil, exitDataFault
		}
		tables = append(tables, &timeline.ClockTable{
			TrackID: b.Spec.ID, ClockRate: b.Spec.ClockRate,
			BaseTS: pkts[0].Timestamp, Packets: pkts,
		})
		nominal[b.Spec.ID] = b.NominalNS
	}

	// Stage 3: align onto one master timeline.
	mt, aerr := timeline.Align(tables, nominal, &scanErrs)
	if aerr != nil {
		fmt.Fprintf(os.Stderr, "timeline alignment failed: %v\n", aerr)
		return nil, exitDataFault
	}

	// Stage 4: ladder of N representations sharing the global boundary grid.
	reps := make([]ladder.Representation, 0, args.ladders)
	for r := 0; r < args.ladders; r++ {
		reps = append(reps, ladder.Representation{
			ID: fmt.Sprintf("rend-%02d", r+1), Bitrate: int64(1_000_000 >> r),
			Timeline: mt,
		})
	}
	var ladderErrs model.Errors
	lres, lerr := ladder.Align(reps, ladder.Config{
		TargetSegmentNS: int64(args.segment),
		ToleranceNS:     int64(args.segment) / 4,
		MaxRenditions:   16,
	}, &ladderErrs)
	if lerr != nil {
		fmt.Fprintf(os.Stderr, "ladder alignment failed: %v\n", lerr)
		return nil, exitDataFault
	}
	st.alignedLadders = len(lres.Aligned)

	// Use the first rendition's plan as the canonical segment manifest.
	plan := lres.Plans[lres.Aligned[0]]
	cfg := runner.Config{
		WorkDir: filepath.Join(work, "exec"), MaxSegments: 10000,
		MaxSegmentBytes: 1 << 24, MaxOutputBytes: 1 << 30,
	}
	// First execution (cold).
	r1, e1 := runner.Execute(plan, inputs, cfg, time.Now().UnixMilli())
	if e1 != nil {
		fmt.Fprintf(os.Stderr, "execute failed: %v\n", e1)
		return nil, exitDataFault
	}
	// Second execution proves determinism and (with --resume) checkpoint reuse.
	r2, e2 := runner.Execute(plan, inputs, cfg, time.Now().UnixMilli())
	if e2 != nil {
		fmt.Fprintf(os.Stderr, "resume execute failed: %v\n", e2)
		return nil, exitDataFault
	}
	if r1.ManifestSHA256 != r2.ManifestSHA256 || r1.OutputSHA256 != r2.OutputSHA256 {
		fmt.Fprintf(os.Stderr, "non-deterministic outputs across reruns\n")
		return nil, exitDataFault
	}
	if args.resume && !r2.CheckpointReused {
		fmt.Fprintf(os.Stderr, "resume requested but checkpoint was not reused\n")
		return nil, exitDataFault
	}
	st.resumeMS = r2.ElapsedMS
	return r2, exitOK
}

func printReport(args demoArgs, r *runner.Report, st *demoState) {
	fmt.Printf("tracks=%d\n", args.tracks)
	fmt.Printf("aligned_ladders=%d\n", st.alignedLadders)
	if st.faultOffset >= 0 {
		fmt.Printf("fault_offset_bytes=%d\n", st.faultOffset)
		// Literal acceptance bound, with the measured value alongside.
		fmt.Printf("fault_error_bytes<=16 actual_fault_error_bytes=%d\n", st.faultErrorBytes)
	}
	fmt.Printf("resume_ms<=500 actual_resume_ms=%d\n", st.resumeMS)
	fmt.Printf("checkpoint_reused=%t\n", r.CheckpointReused)
	fmt.Printf("manifest_sha256=%s\n", r.ManifestSHA256)
	fmt.Printf("output_sha256=%s\n", r.OutputSHA256)
}
