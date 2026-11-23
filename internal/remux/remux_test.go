package remux

import (
	"bytes"
	"testing"

	"github.com/broadcast/mediad/internal/evidence"
	"github.com/broadcast/mediad/internal/fixture"
	"github.com/broadcast/mediad/internal/model"
	"github.com/broadcast/mediad/internal/probe"
	"github.com/broadcast/mediad/internal/repair"
)

type pipeline struct {
	graph *model.Graph
	errs  *model.ErrorSet
	dec   *repair.Decisions
	plan  *Plan
	raw   []byte
}

func runPipeline(t *testing.T, spec fixture.Spec) *pipeline {
	t.Helper()
	raw := fixture.MustGenerate(spec)
	errs := &model.ErrorSet{}
	g, _, err := probe.Probe(bytes.NewReader(raw), int64(len(raw)), probe.DefaultConfig(), errs)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if a := g.Tracks[2]; a != nil {
		a.DependsOn = []uint32{1}
	}
	dmg := repair.Locate(g, errs)
	dec, err := repair.Adjudicate(g, dmg, errs, repair.DefaultPolicy())
	if err != nil {
		t.Fatalf("adjudicate: %v", err)
	}
	plan, err := BuildPlan(g, dec)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	return &pipeline{graph: g, errs: errs, dec: dec, plan: plan, raw: raw}
}

func TestNormalRoundTrip(t *testing.T) {
	pl := runPipeline(t, fixture.StandardSet(23)[0])
	out, err := Write(pl.plan, bytes.NewReader(pl.raw))
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	v, err := Verify(out, pl.graph, pl.plan, probe.DefaultConfig())
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if !v.OK || v.MatchRate != 1 || v.KeyframeHitRate != 1 {
		t.Fatalf("normal roundtrip must be perfect: %+v", v)
	}
	if v.TracksChecked < 2 {
		t.Fatalf("tracks_checked = %d, want >= 2", v.TracksChecked)
	}
}

func TestRoundTripDropsIsolatedSamples(t *testing.T) {
	// Sparse-keyframe fixture keeps all payloads but has warnings; it must
	// still round trip with exactly the planned sample counts.
	pl := runPipeline(t, fixture.StandardSet(23)[3])
	out, err := Write(pl.plan, bytes.NewReader(pl.raw))
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	v, err := Verify(out, pl.graph, pl.plan, probe.DefaultConfig())
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	for id, detail := range v.PerTrack {
		if detail.OutputSamples != pl.plan.Tracks[id].KeptCount() {
			t.Fatalf("track %d output samples %d != planned %d",
				id, detail.OutputSamples, pl.plan.Tracks[id].KeptCount())
		}
	}
}

func TestSegmentHashesStableAcrossRuns(t *testing.T) {
	pl := runPipeline(t, fixture.StandardSet(23)[0])
	src := bytes.NewReader(pl.raw)
	h1, err := evidence.HashSegments(src, pl.graph, pl.dec.Segments)
	if err != nil {
		t.Fatalf("hash run 1: %v", err)
	}
	h2, err := evidence.HashSegments(bytes.NewReader(pl.raw), pl.graph, pl.dec.Segments)
	if err != nil {
		t.Fatalf("hash run 2: %v", err)
	}
	if !evidence.StableDigests(h1, h2) {
		t.Fatalf("segment hashes not reproducible:\n%v\n%v", h1, h2)
	}
	// Output hashes must match input hashes for every kept segment.
	out, err := Write(pl.plan, bytes.NewReader(pl.raw))
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	errs := &model.ErrorSet{}
	outGraph, _, err := probe.Probe(bytes.NewReader(out), int64(len(out)), probe.DefaultConfig(), errs)
	if err != nil {
		t.Fatalf("reprobe: %v", err)
	}
	outSegs := map[uint32][]*repair.Segment{}
	for _, id := range pl.plan.Order {
		tp := pl.plan.Tracks[id]
		outSegs[id] = []*repair.Segment{{
			TrackID: id, FirstSample: 0, LastSample: len(tp.Kept) - 1,
			Status: repair.StatusValid,
		}}
	}
	hOut, err := evidence.HashSegments(bytes.NewReader(out), outGraph, outSegs)
	if err != nil {
		t.Fatalf("output hash: %v", err)
	}
	if got := evidence.Compare(h1, hOut); got != 1 {
		t.Fatalf("input/output segment hash match = %.3f, want 1.0", got)
	}
}

func TestDeterministicBytesAcrossWrites(t *testing.T) {
	pl := runPipeline(t, fixture.StandardSet(23)[0])
	out1, err := Write(pl.plan, bytes.NewReader(pl.raw))
	if err != nil {
		t.Fatal(err)
	}
	out2, err := Write(pl.plan, bytes.NewReader(pl.raw))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out1, out2) {
		t.Fatal("identical input and plan must yield byte-identical output")
	}
}

// KeptCount is a small accessor used by tests.
func (tp *TrackPlan) KeptCount() int { return len(tp.Kept) }
