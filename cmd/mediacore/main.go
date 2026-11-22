package main

import (
	"flag"
	"fmt"
	"os"

	"mediacore"
)

// Options holds the parsed demo command-line parameters.
type Options struct {
	Seed     int64
	DriftPPM int64
	WrapAt   uint64
	LateMS   int64
	VFR      bool
}

func parseArgs(args []string) (Options, error) {
	fs := flag.NewFlagSet("mediacore", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var o Options
	fs.Int64Var(&o.Seed, "seed", 42, "deterministic fixture seed")
	fs.Int64Var(&o.DriftPPM, "drift-ppm", 180, "injected capture clock drift in parts per million")
	fs.Uint64Var(&o.WrapAt, "wrap-at", 4294967000, "RTP timestamp near which a 32-bit wrap is forced")
	fs.Int64Var(&o.LateMS, "late-ms", 250, "receive delay injected on one block in milliseconds")
	fs.BoolVar(&o.VFR, "vfr", false, "emit a variable-frame-rate video track")
	// The standard library flag package stops at the first positional
	// argument, so peel the "demo" subcommand out before parsing. This
	// accepts both "demo --flags" and "--flags demo".
	rest := make([]string, 0, len(args))
	sawDemo := false
	for _, a := range args {
		if a == "demo" {
			sawDemo = true
			continue
		}
		rest = append(rest, a)
	}
	if err := fs.Parse(rest); err != nil {
		return o, err
	}
	if !sawDemo || fs.NArg() != 0 {
		return o, fmt.Errorf("usage: mediacore demo [flags]")
	}
	if o.WrapAt > 0xFFFFFFFF {
		return o, fmt.Errorf("wrap-at must fit in uint32")
	}
	if o.Seed < 0 || o.LateMS < 0 {
		return o, fmt.Errorf("seed and late-ms must be non-negative")
	}
	return o, nil
}

// Metrics aggregates every acceptance metric printed at the end of a run.
type Metrics struct {
	RoundtripMatch   float64
	KeyframeHitRate  float64
	SegmentHashMatch float64
	RecoveredLoss    int
	LateReplay       int
	DriftPPM         int64
	Errors           int
}

func (m Metrics) String() string {
	return fmt.Sprintf(
		"roundtrip_match=%.3f keyframe_hit_rate=%.3f segment_hash_match=%.3f "+
			"recovered_loss=%d late_replay=%d drift_ppm=%d errors=%d",
		m.RoundtripMatch, m.KeyframeHitRate, m.SegmentHashMatch,
		m.RecoveredLoss, m.LateReplay, m.DriftPPM, m.Errors)
}

// invariants are the acceptance gates. A zero exit code requires every one.
func (m Metrics) pass() bool {
	return m.RoundtripMatch == 1.0 &&
		m.KeyframeHitRate == 1.0 &&
		m.SegmentHashMatch == 1.0 &&
		m.RecoveredLoss >= 1 &&
		m.LateReplay >= 1 &&
		m.Errors >= 3
}

// classifyError records a failure category and keeps processing independent
// segments instead of aborting the whole run.
type errorLog struct {
	cats []mediacore.Category
}

func (e *errorLog) record(err error) {
	if err == nil {
		return
	}
	if me, ok := mediacore.AsError(err); ok {
		e.cats = append(e.cats, me.Cat)
	} else {
		e.cats = append(e.cats, mediacore.CatCorrupt)
	}
}
