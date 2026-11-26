package main

import (
	"encoding/json"
	"fmt"
	"io"

	"streamcheck/fixture"
)

// printReport renders every completed stage's findings to the terminal.
func printReport(w io.Writer, plan *fixture.Plan, arts map[string]string) {
	fmt.Fprintf(w, "streamcheck report seed=%d fault-set=%s\n", plan.Seed, plan.FaultSet)
	if s, ok := arts["manifest"]; ok {
		var a manifestArtifact
		if json.Unmarshal([]byte(s), &a) == nil {
			fmt.Fprintf(w, "manifest: dash-representations=%d dash-segments-per-rep=%d hls-segments=%d\n",
				a.DashReps, a.DashSegments, a.HLSSegments)
			fmt.Fprintf(w, "manifest: discontinuities=%d continuity-intervals=%d unexplained-timing-jumps=%d\n",
				a.Discontinuities, a.Intervals, len(a.TimingJumps))
			for _, j := range a.TimingJumps {
				fmt.Fprintf(w, "manifest: jump rep=%s segment=%d want=%dms got=%dms\n",
					j.Rep, j.Index, j.WantMs, j.GotMs)
			}
			fmt.Fprintf(w, "manifest: cross-representation-first-mismatch=%s\n", a.CrossRep)
		}
	}
	if s, ok := arts["packet"]; ok {
		var a packetArtifact
		if json.Unmarshal([]byte(s), &a) == nil {
			fmt.Fprintf(w, "packet: received=%d delivered=%d duplicates=%d reordered-in-window=%d\n",
				a.Received, a.Delivered, a.Duplicates, a.Reordered)
			fmt.Fprintf(w, "packet: burst-lost=%d max-burst=%d contiguous-gaps=%d\n",
				a.Lost, a.MaxBurst, a.Gaps)
			fmt.Fprintf(w, "packet: sustained-bandwidth min=%.0fkbps max=%.0fkbps\n",
				a.MinKbps, a.MaxKbps)
		}
	}
	if s, ok := arts["inspect"]; ok {
		var a inspectArtifact
		if json.Unmarshal([]byte(s), &a) == nil {
			if a.CorruptSegment >= 0 {
				diff := a.CorruptOffset - plan.CorruptOffset
				if diff < 0 {
					diff = -diff
				}
				fmt.Fprintf(w, "inspect: corrupt-segment=%d corrupt-offset=%d offset-error=%dB\n",
					a.CorruptSegment, a.CorruptOffset, diff)
			} else {
				fmt.Fprintln(w, "inspect: corrupt-segment=none")
			}
			fmt.Fprintf(w, "inspect: keyframe-times-ms=%v\n", a.KeyframeTimesMs)
			fmt.Fprintf(w, "inspect: gop-max-normal=%dms gop-max-after-injection=%dms\n",
				a.GOPNormalMaxMs, a.GOPMaxMs)
			fmt.Fprintf(w, "inspect: cross-representation-keyframes-match=%t\n", a.CrossRepKeyframes)
			if a.TruncatedError != "" {
				fmt.Fprintf(w, "inspect: truncated-box structured-error offset=%d\n", a.TruncatedOffset)
			}
		}
	}
	if s, ok := arts["migrate"]; ok {
		var a migrateArtifact
		if json.Unmarshal([]byte(s), &a) == nil {
			if a.RollbackChecked {
				fmt.Fprintf(w, "migrate: rollback-bytes-identical=%t\n", a.RollbackIdentical)
			}
			fmt.Fprintf(w, "migrate: recovered=%s bytes=%d\n", a.RecoveredFile, a.RecoveredBytes)
			fmt.Fprintf(w, "sha256=%s  %s\n", a.RecoveredSHA256, a.RecoveredFile)
		}
	}
}

// verify compares the stage artifacts against the fixture plan's ground
// truth and returns every mismatch.
func verify(plan *fixture.Plan, arts map[string]string) []string {
	var bad []string
	check := func(ok bool, format string, args ...any) {
		if !ok {
			bad = append(bad, fmt.Sprintf(format, args...))
		}
	}
	var m manifestArtifact
	if err := json.Unmarshal([]byte(arts["manifest"]), &m); err != nil {
		bad = append(bad, "manifest artifact missing")
	} else {
		check(m.Discontinuities == plan.Discontinuities,
			"discontinuities got %d want %d", m.Discontinuities, plan.Discontinuities)
		check(len(m.TimingJumps) == plan.TimingJumps,
			"timing jumps got %d want %d", len(m.TimingJumps), plan.TimingJumps)
		check(m.CrossRep == "none", "cross-representation mismatch: %s", m.CrossRep)
	}
	var p packetArtifact
	if err := json.Unmarshal([]byte(arts["packet"]), &p); err != nil {
		bad = append(bad, "packet artifact missing")
	} else {
		check(p.Duplicates == plan.Duplicates, "duplicates got %d want %d", p.Duplicates, plan.Duplicates)
		check(p.Reordered == plan.Reordered, "reordered got %d want %d", p.Reordered, plan.Reordered)
		check(p.Lost == plan.BurstLost, "burst lost got %d want %d", p.Lost, plan.BurstLost)
	}
	var in inspectArtifact
	if err := json.Unmarshal([]byte(arts["inspect"]), &in); err != nil {
		bad = append(bad, "inspect artifact missing")
	} else {
		check(in.CorruptSegment == plan.CorruptSegment,
			"corrupt segment got %d want %d", in.CorruptSegment, plan.CorruptSegment)
		check(in.CorruptOffset == plan.CorruptOffset,
			"corrupt offset got %d want %d", in.CorruptOffset, plan.CorruptOffset)
		check(in.GOPNormalMaxMs == plan.NormalGOPMaxMs,
			"normal GOP max got %d want %d", in.GOPNormalMaxMs, plan.NormalGOPMaxMs)
		check(in.GOPMaxMs == plan.InjectedGOPMaxMs,
			"injected GOP max got %d want %d", in.GOPMaxMs, plan.InjectedGOPMaxMs)
		check(in.CrossRepKeyframes, "keyframe intervals differ across representations")
	}
	var mg migrateArtifact
	if err := json.Unmarshal([]byte(arts["migrate"]), &mg); err != nil {
		bad = append(bad, "migrate artifact missing")
	} else {
		check(!mg.RollbackChecked || mg.RollbackIdentical, "rollback bytes differ")
		check(mg.RecoveredSHA256 != "", "recovered output missing")
	}
	return bad
}
