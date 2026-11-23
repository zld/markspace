package main

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"mediapack/internal/fixture"
	"mediapack/internal/index"
	"mediapack/internal/ladder"
	"mediapack/internal/manifest"
	"mediapack/internal/model"
	"mediapack/internal/probe"
	"mediapack/internal/timeline"
)

// runCheck exercises the real data-fault exit path (code 2). It generates a
// fixture set, optionally injects a corruption, then scans every input. A
// detected truncation / illegal length / unreadable input yields exit 2.
func runCheck(argv []string) int {
	work := ""
	mode := ""
	for i := 0; i < len(argv); i++ {
		switch argv[i] {
		case "--work":
			if i+1 >= len(argv) {
				fmt.Fprintln(os.Stderr, "--work needs a value")
				return exitUsage
			}
			work = argv[i+1]
			i++
		case "--truncate-box", "--truncate-mxf", "--unalignable":
			mode = argv[i]
		default:
			fmt.Fprintf(os.Stderr, "unknown flag %q\n", argv[i])
			return exitUsage
		}
	}
	if work == "" {
		fmt.Fprintln(os.Stderr, "--work is required")
		return exitUsage
	}
	if err := os.MkdirAll(work, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "work dir: %v\n", err)
		return exitDataFault
	}
	bundles, err := fixture.Generate(work, fixture.DefaultSpecs(2))
	if err != nil {
		fmt.Fprintf(os.Stderr, "fixture: %v\n", err)
		return exitDataFault
	}
	targets := make([]string, 0, len(bundles))
	for _, b := range bundles {
		targets = append(targets, b.Container)
	}
	switch mode {
	case "--truncate-box":
		f, ferr := fixture.InjectTruncatedBox(targets[0], filepath.Join(work, "corrupt.mp4"))
		if ferr != nil {
			fmt.Fprintf(os.Stderr, "inject: %v\n", ferr)
			return exitDataFault
		}
		targets = []string{f.Path}
	case "--truncate-mxf":
		mxf := targets[0]
		for _, t := range targets {
			if filepath.Ext(t) == ".mxf" {
				mxf = t
			}
		}
		f, ferr := fixture.InjectTruncatedMXF(mxf, filepath.Join(work, "corrupt.mxf"))
		if ferr != nil {
			fmt.Fprintf(os.Stderr, "inject: %v\n", ferr)
			return exitDataFault
		}
		targets = []string{f.Path}
	case "":
		// no injection: a clean scan must succeed
	case "--unalignable":
		return checkUnalignable(work, bundles)
	default:
		fmt.Fprintf(os.Stderr, "unsupported check mode %q\n", mode)
		return exitUsage
	}
	faults := 0
	for _, p := range targets {
		if _, serr := probe.Scan(p, probe.DefaultLimits()); serr != nil {
			if pe, ok := model.AsPipeline(serr); ok {
				fmt.Printf("fault code=%s input=%s offset=%d length=%d\n",
					pe.Code, filepath.Base(pe.Input), pe.ByteRange.Offset, pe.ByteRange.Length)
			}
			faults++
		}
	}
	if faults > 0 {
		return exitDataFault
	}
	fmt.Println("check=ok")
	return exitOK
}

// checkUnalignable runs the align/ladder stages with an impossible tolerance
// so the representation is rejected, exercising the exit-2 alignment path.
func checkUnalignable(work string, bundles []fixture.Bundle) int {
	store, err := index.NewStore(filepath.Join(work, "idx"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "index: %v\n", err)
		return exitDataFault
	}
	tables := make([]*timeline.ClockTable, 0, len(bundles))
	var es model.Errors
	for _, b := range bundles {
		if _, ierr := store.GetOrScan(b.Container, probe.DefaultLimits()); ierr != nil {
			fmt.Fprintf(os.Stderr, "scan: %v\n", ierr)
			return exitDataFault
		}
		raw, _ := os.ReadFile(b.RTPSidecar)
		pkts, _, perr := timeline.ParseRTPPacketStream(raw)
		if perr != nil {
			fmt.Fprintf(os.Stderr, "rtp: %v\n", perr)
			return exitDataFault
		}
		tables = append(tables, &timeline.ClockTable{
			TrackID: b.Spec.ID, ClockRate: b.Spec.ClockRate,
			BaseTS: pkts[0].Timestamp, Packets: pkts,
		})
	}
	mt, aerr := timeline.Align(tables, nil, &es)
	if aerr != nil {
		fmt.Fprintf(os.Stderr, "align: %v\n", aerr)
		return exitDataFault
	}
	// Decodable points land only on whole seconds; a 1.5s target with a 1ns
	// tolerance cannot be met, so non-tail segments must be rejected.
	var le model.Errors
	_, lerr := ladder.Align(
		[]ladder.Representation{{ID: "r0", Timeline: mt}},
		ladder.Config{TargetSegmentNS: int64(1500 * time.Millisecond), ToleranceNS: 1, MaxRenditions: 4},
		&le)
	if lerr != nil {
		if pe, ok := model.AsPipeline(lerr); ok {
			fmt.Printf("fault code=%s stage=%s\n", pe.Code, pe.Stage)
		}
		return exitDataFault
	}
	// Defensive: manifest-level guard should also reject.
	var me model.Errors
	synth := &timeline.MasterTimeline{CommonPoints: mt.CommonPoints}
	if _, merr := manifest.Build(synth, manifest.Config{
		TargetSegmentNS: int64(1500 * time.Millisecond), ToleranceNS: 1,
	}, &me); merr != nil {
		if pe, ok := model.AsPipeline(merr); ok {
			fmt.Printf("fault code=%s stage=%s\n", pe.Code, pe.Stage)
		}
		return exitDataFault
	}
	fmt.Fprintln(os.Stderr, "expected alignment rejection did not occur")
	return exitDataFault
}
