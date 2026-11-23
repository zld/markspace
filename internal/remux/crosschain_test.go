package remux

import (
	"bytes"
	"testing"

	"github.com/broadcast/mediad/internal/fixture"
	"github.com/broadcast/mediad/internal/model"
	"github.com/broadcast/mediad/internal/probe"
	"github.com/broadcast/mediad/internal/repair"
)

// fullChain runs one fixture through every subsystem and returns the
// resulting graph, decisions, output bytes and verification.
func fullChain(t *testing.T, idx int) (*model.Graph, *repair.Decisions, []byte, *Verification, *model.ErrorSet) {
	t.Helper()
	raw := fixture.MustGenerate(fixture.StandardSet(23)[idx])
	errs := &model.ErrorSet{}
	g, _, err := probe.Probe(bytes.NewReader(raw), int64(len(raw)), probe.DefaultConfig(), errs)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	for _, id := range g.IDs() {
		if g.Tracks[id].Kind == model.KindAudio {
			g.Tracks[id].DependsOn = videoIDsLocal(g)
		}
	}
	dmg := repair.Locate(g, errs, int64(len(raw)))
	dec, err := repair.Adjudicate(g, dmg, errs, repair.DefaultPolicy())
	if err != nil {
		t.Fatalf("adjudicate: %v", err)
	}
	plan, err := BuildPlan(g, dec)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	out, err := Write(plan, bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	v, err := Verify(out, g, plan, probe.DefaultConfig())
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	return g, dec, out, v, errs
}

func videoIDsLocal(g *model.Graph) []uint32 {
	var out []uint32
	for _, id := range g.IDs() {
		if g.Tracks[id].Kind == model.KindVideo {
			out = append(out, id)
		}
	}
	return out
}

// Fault class 1: backward/duplicate timestamps must be detected in probe,
// adjudicated as recoverable in repair, and survive a full remux round trip.
func TestCrossChainBackwardTimestamps(t *testing.T) {
	g, dec, _, v, errs := fullChain(t, 2) // backward_ts
	if !errs.Has(model.KindTimestampDuplicate) {
		t.Fatal("probe must surface the timestamp defect")
	}
	recovered := 0
	for _, segs := range dec.Segments {
		for _, s := range segs {
			if s.Status == repair.StatusRecovered {
				recovered++
			}
		}
	}
	if recovered == 0 {
		t.Fatal("repair must mark the ordering defect recoverable")
	}
	if !v.OK {
		t.Fatalf("remux verification must pass after recovery: %+v", v)
	}
	// Recovered samples keep their place: output counts match the plan.
	for _, id := range g.IDs() {
		if v.PerTrack[id].OutputSamples != len(BuildPlanKept(t, g, dec, id)) {
			t.Fatalf("track %d sample count mismatch", id)
		}
	}
}

// Fault class 2: truncated mdat must be located per-track in repair and the
// unreadable tail excluded from the remux output while the valid flank stays.
func TestCrossChainTruncatedMdat(t *testing.T) {
	g, dec, _, v, errs := fullChain(t, 1) // truncated_mdat
	if !errs.Has(model.KindTruncatedBox) {
		t.Fatal("probe must detect the truncated box")
	}
	audioID := uint32(2)
	totalDamage := 0
	for _, rs := range dec.IsolatedRanges {
		totalDamage += len(rs)
	}
	if totalDamage == 0 {
		t.Fatal("repair must isolate the unreadable truncated span")
	}
	// The damaged track keeps fewer output samples than its source had.
	srcCount := g.Tracks[audioID].SampleCount()
	if v.PerTrack[audioID].OutputSamples >= srcCount {
		t.Fatalf("truncated track output %d must be < source %d",
			v.PerTrack[audioID].OutputSamples, srcCount)
	}
	// But the unaffected video track round-trips completely.
	if v.PerTrack[1].OutputSamples != g.Tracks[1].SampleCount() {
		t.Fatal("valid video flank must be preserved in full")
	}
	if !v.OK {
		t.Fatalf("verification of the surviving material must pass: %+v", v)
	}
}

// BuildPlanKept returns the planned kept indices for a track.
func BuildPlanKept(t *testing.T, g *model.Graph, dec *repair.Decisions, id uint32) []int {
	plan, err := BuildPlan(g, dec)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	return plan.Tracks[id].Kept
}
